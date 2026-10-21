package lan

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 端到端：server + agent 走真实 TCP/握手/心跳/指令。
func TestRoundTrip(t *testing.T) {
	srv, stopSrv := startTestServer(t)
	defer stopSrv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := NewAgent("pc-01", "TEST-PC01")
	a.Label = "A-01"
	a.IP = "127.0.0.1"
	a.MAC = "00:11:22:33:44:55"
	a.PinnedPub = srv.ID.Pub()
	a.DirectAddr = fmt.Sprintf("127.0.0.1:%d", srv.boundPort.Load())
	go a.Run(ctx)

	// 等上线
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, ok := srv.GetDevice("pc-01")
		if ok && d.Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent 未上线")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 新设备先进 pending：未批准前 SendCommand 必须被拒（enrollment 边界）。
	if d, _ := srv.GetDevice("pc-01"); !d.Pending {
		t.Fatal("新注册设备应为 pending")
	}
	if _, err := srv.SendCommand("pc-01", OpSetHelper, SetHelperArgs{On: false}); err == nil {
		t.Fatal("待审批设备竟能收到课堂指令")
	}
	if n := srv.Approve([]string{"pc-01"}); n != 1 {
		t.Fatalf("审批失败: %d", n)
	}

	// 下发 helper.set false
	ack, err := srv.SendCommand("pc-01", OpSetHelper, SetHelperArgs{On: false})
	if err != nil {
		t.Fatal(err)
	}
	if !ack.OK {
		t.Fatalf("ack 失败: %s", ack.Err)
	}
	// agent 先推 status 再回 ack：SendCommand 返回时注册表必须已更新。
	d, _ := srv.GetDevice("pc-01")
	if d.HelperOn {
		t.Fatal("注册表未更新 helperOn=false")
	}

	// 下发 features.set
	keys := []string{"usb_storage", "wallpaper"}
	if _, err := srv.SendCommand("pc-01", OpSetFeatures, SetFeaturesArgs{Keys: keys}); err != nil {
		t.Fatal(err)
	}
	d, _ = srv.GetDevice("pc-01")
	if fmt.Sprint(d.Features) != fmt.Sprint(keys) {
		t.Fatalf("features 不符: %v", d.Features)
	}

	// 篡改签名应失败：用错误公钥握手
	bad := NewAgent("pc-evil", "EVIL")
	bad.PinnedPub = mustGenOtherPub(t)
	bad.DirectAddr = a.directAddr()
	// clientHandshake 会因签名不匹配失败 → session 不建立，server 无设备
	if err := bad.session(ctx, bad.DirectAddr, bad.PinnedPub); err == nil {
		t.Fatal("伪造公钥的握手竟成功了")
	}
}

// 组播广播端到端：切片发送 → 加入组 → 重组解密回原帧（走本机组播回环）。
func TestCastMulticastRoundTrip(t *testing.T) {
	p, err := NewCastSession()
	if err != nil {
		t.Fatal(err)
	}
	sender, err := NewCastSender(p, nil)
	if err != nil {
		t.Skipf("本机无法建组播发送端: %v", err)
	}
	defer func() { sender.Close() }()
	rcv, err := JoinCast(p)
	if err != nil {
		t.Skipf("本机无法加入组播组: %v", err)
	}
	defer rcv.Close()

	got := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rcv.Run(ctx, func(jpeg []byte) { got <- jpeg })

	// 发一帧横跨多个分片的假 JPEG（GCM 密文，对端重组+解密校验）。
	frame := make([]byte, castChunkLen*3+777)
	for i := range frame {
		frame[i] = byte(i * 31)
	}
	// 默认路由口可能把组播丢进虚拟网卡。换出口再试，包括回环。
	outs := []net.IP{nil, net.ParseIP("127.0.0.1")}
	for _, ip := range LocalIPv4s() {
		outs = append(outs, net.ParseIP(ip))
	}
	var tried []string
	for i, ifIP := range outs {
		label := "default"
		if ifIP != nil {
			label = ifIP.String()
		}
		if i > 0 {
			sender.Close()
			var err error
			sender, err = NewCastSender(p, ifIP)
			if err != nil {
				tried = append(tried, label+": "+err.Error())
				continue
			}
		}
		if jpeg, ok := waitCastFrame(t, sender, frame, got, 1500*time.Millisecond); ok {
			if len(jpeg) != len(frame) {
				t.Fatalf("帧长不符: %d != %d", len(jpeg), len(frame))
			}
			for i := range jpeg {
				if jpeg[i] != frame[i] {
					t.Fatalf("帧内容在 %d 处不符", i)
				}
			}
			return
		}
		tried = append(tried, label+": 发送成功但没有回环")
	}
	// 发送端能建起来，但本机一张网卡都没有把组播送回来。GitHub 的
	// Windows 虚拟网卡和防火墙会这样；协议在 Linux CI 上有回环覆盖。
	t.Skipf("本机没有回环任何组播数据报（%s）", strings.Join(tried, "; "))
}

func waitCastFrame(t *testing.T, sender *CastSender, frame []byte, got <-chan []byte, d time.Duration) ([]byte, bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if _, err := sender.Send(frame); err != nil {
			t.Logf("组播发送失败: %v", err)
			return nil, false
		}
		select {
		case jpeg := <-got:
			return jpeg, true
		case <-time.After(200 * time.Millisecond):
			if time.Now().After(deadline) {
				return nil, false
			}
		}
	}
}

// pending 设备的上行数据（屏幕帧/求助/文件）必须被丢弃——审批是双向边界，
// 不只是下行指令。
func TestPendingUpstreamBlocked(t *testing.T) {
	srv, stopSrv := startTestServer(t)
	defer stopSrv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := NewAgent("pc-pending", "TEST-PC99")
	a.PinnedPub = srv.ID.Pub()
	a.DirectAddr = fmt.Sprintf("127.0.0.1:%d", srv.boundPort.Load())
	go a.Run(ctx)

	waitFor(t, 5*time.Second, "agent 上线", func() bool {
		d, ok := srv.GetDevice("pc-pending")
		return ok && d.Online
	})

	s := a.cur.Load()
	if s == nil {
		t.Fatal("agent 会话未建立")
	}
	jpeg := base64.StdEncoding.EncodeToString([]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3})
	if err := s.send(Msg{Type: MsgScreen, Screen: &ScreenMsg{JPEG: jpeg}}); err != nil {
		t.Fatal(err)
	}
	if err := s.send(Msg{Type: MsgHelp, Help: &HelpMsg{Kind: "raise"}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(srv.Screens()) != 0 {
		t.Fatal("pending 设备的屏幕帧竟被收下")
	}
	if len(srv.HelpRequests()) != 0 {
		t.Fatal("pending 设备的求助竟被收下")
	}

	if n := srv.Approve([]string{"pc-pending"}); n != 1 {
		t.Fatalf("审批失败: %d", n)
	}
	if err := s.send(Msg{Type: MsgScreen, Screen: &ScreenMsg{JPEG: jpeg}}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(srv.Screens()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("批准后屏幕帧仍未入库")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func mustGenOtherPub(t *testing.T) []byte {
	t.Helper()
	other, err := GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return other.Pub()
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startTestServer 用 TCPPort=0 让内核分配空闲端口——固定端口在并行/
// 快速重跑时会撞上旧监听还没退出。启动后从 srv.TCPPort 读真实端口。
func startTestServer(t *testing.T) (*Server, context.CancelFunc) {
	t.Helper()
	id, err := GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(id, 0)
	srv.disableDiscover = true // 用例走直连；UDP :47822 全局只有一个，不能互相抢
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Run(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "TCP 监听就绪", func() bool { return srv.boundPort.Load() != 0 })
	return srv, cancel
}

func startTestAgent(t *testing.T, ctx context.Context, srv *Server, id, name string) *Agent {
	t.Helper()
	a := NewAgent(id, name)
	a.PinnedPub = srv.ID.Pub()
	a.DirectAddr = fmt.Sprintf("127.0.0.1:%d", srv.boundPort.Load())
	go a.Run(ctx)
	waitFor(t, 5*time.Second, "agent 上线", func() bool {
		d, ok := srv.GetDevice(id)
		return ok && d.Online
	})
	if n := srv.Approve([]string{id}); n != 1 {
		t.Fatalf("审批失败: %d", n)
	}
	return a
}

func (a *Agent) guardIsLocked() bool {
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	return a.guardLocked
}

// 断线守护生命周期：断线超时锁屏 → 本机紧急解锁（守护标记清零、
// 告警离线缓存、本次断线期内不再上锁——管理员已接管）→ 重连后
// 告警补发，且下一次断线守护正常再上锁（不会被永久豁免）。
func TestOfflineGuardEmergencyUnlock(t *testing.T) {
	old := offlineGuardDelay
	offlineGuardDelay = 60 * time.Millisecond
	defer func() { offlineGuardDelay = old }()

	srv, stopSrv := startTestServer(t)
	defer stopSrv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := startTestAgent(t, ctx, srv, "pc-g", "PC-G")
	if _, err := srv.SendCommand("pc-g", OpSetFeatures, SetFeaturesArgs{Keys: []string{"guard_offline"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "guard_offline 生效", func() bool {
		d, _ := srv.GetDevice("pc-g")
		return fmt.Sprint(d.Features) == "[guard_offline]"
	})

	// 断线：关掉服务端侧连接，并把 agent 的直连地址指到死端口让它重连不上。
	goodAddr := a.directAddr()
	killConn := func() {
		srv.mu.Lock()
		cc := srv.sessions["pc-g"]
		srv.mu.Unlock()
		if cc == nil {
			t.Fatal("服务端会话缺失")
		}
		a.setDirectAddr("127.0.0.1:1")
		_ = cc.sess.conn.Close()
	}
	killConn()

	waitFor(t, 3*time.Second, "断线守护锁屏", a.guardIsLocked)

	// 紧急解锁：守护标记清零，告警进离线缓存（管理端此刻不可达）。
	a.onEmergencyUnlock()
	if a.guardIsLocked() {
		t.Fatal("紧急解锁后守护标记未清零")
	}
	a.pendMu.Lock()
	cached := len(a.pendAlerts)
	a.pendMu.Unlock()
	if cached != 1 {
		t.Fatalf("离线告警未缓存: %d", cached)
	}
	// 本次断线期内不再上锁（管理员已接管这台机器）。
	time.Sleep(3 * offlineGuardDelay)
	if a.guardIsLocked() {
		t.Fatal("紧急解锁后本次断线期又被锁上")
	}

	// 重连：告警补发，守护解锁且下轮断线可重新上弦。
	a.setDirectAddr(goodAddr)
	// 断线后 agent 先做最多 3s 的 UDP 回退，再 sleep 2s 才拨新地址。
	waitFor(t, 8*time.Second, "重连后告警送达", func() bool {
		d, _ := srv.GetDevice("pc-g")
		return d.Alerts > 0
	})
	waitFor(t, 3*time.Second, "重连上线", func() bool {
		d, _ := srv.GetDevice("pc-g")
		return d.Online
	})

	// 再断一次：守护没被永久豁免，重新上锁。
	killConn()
	waitFor(t, 3*time.Second, "再次断线重新上锁", a.guardIsLocked)
}

// 关机语义：普通关机是 agent 侧可取消倒计时，到期执行非强制关机；
// force 立即执行；power.cancel 停掉倒计时后不再执行。
func TestPowerScheduleSemantics(t *testing.T) {
	fired := make(chan bool, 4)
	old := doPowerAction
	doPowerAction = func(op string, force bool) error { fired <- force; return nil }
	oldDelay := powerDelay
	powerDelay = 120 * time.Millisecond
	defer func() { doPowerAction = old; powerDelay = oldDelay }()

	a := NewAgent("pc-pw", "PC-PW")

	// 普通模式：不立即执行
	if err := a.schedulePower(OpShutdown, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
		t.Fatal("普通关机竟立即执行")
	case <-time.After(50 * time.Millisecond):
	}
	// 取消后到期不执行；重复取消如实返回 false（没有进行中的倒计时）。
	if !a.cancelScheduledPower() {
		t.Fatal("进行中的倒计时取消失败")
	}
	if a.cancelScheduledPower() {
		t.Fatal("空取消竟报成功")
	}
	select {
	case <-fired:
		t.Fatal("取消的倒计时仍执行了")
	case <-time.After(200 * time.Millisecond):
	}
	// 重新排程 → 到期以非强制语义执行；执行后取消必须返回 false
	// （已进入执行阶段，无法再撤回）。
	if err := a.schedulePower(OpShutdown, false); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-fired:
		if f {
			t.Fatal("普通关机竟走了强制路径")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("倒计时到期未执行")
	}
	waitFor(t, time.Second, "执行阶段完成", func() bool {
		a.powerMu.Lock()
		defer a.powerMu.Unlock()
		return a.powerTimer == nil
	})
	if a.cancelScheduledPower() {
		t.Fatal("已执行的关机竟还能取消")
	}
	// 强制模式立即执行且带 force
	if err := a.schedulePower(OpShutdown, true); err != nil {
		t.Fatal(err)
	}
	select {
	case f := <-fired:
		if !f {
			t.Fatal("force 标记丢失")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("强制关机未立即执行")
	}
}

// 高清查看不被缩略图监看重发覆盖 + 单焦点：重发 screenWatch 后
// A 机仍保持 hi 订阅；切到 B 机查看时 A 的 hi 关闭。
func TestHiViewSurvivesScreenWatch(t *testing.T) {
	srv, stopSrv := startTestServer(t)
	defer stopSrv()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a1 := startTestAgent(t, ctx, srv, "pc-a", "PC-A")
	startTestAgent(t, ctx, srv, "pc-b", "PC-B")

	srv.SetScreenWatch([]string{"pc-a", "pc-b"}, true)
	waitFor(t, 3*time.Second, "pc-a 缩略图订阅", func() bool {
		a1.watchMu.Lock()
		defer a1.watchMu.Unlock()
		return a1.watchOff != nil && !a1.watchHi
	})

	// 双击看大图 → 再因其他设备重连重发全班监看 → A 必须保持 hi。
	srv.ViewDevice("pc-a", true)
	waitFor(t, 3*time.Second, "pc-a hi 订阅", func() bool {
		a1.watchMu.Lock()
		defer a1.watchMu.Unlock()
		return a1.watchHi
	})
	srv.SetScreenWatch([]string{"pc-a", "pc-b"}, true)
	time.Sleep(300 * time.Millisecond) // 让重发命令抵达 agent
	a1.watchMu.Lock()
	hi := a1.watchHi
	a1.watchMu.Unlock()
	if !hi {
		t.Fatal("缩略图监看重发覆盖了高清查看")
	}

	// 单焦点：切去 B 查看时 A 的 hi 订阅被关闭。
	srv.ViewDevice("pc-b", true)
	waitFor(t, 3*time.Second, "pc-a hi 关闭", func() bool {
		a1.watchMu.Lock()
		defer a1.watchMu.Unlock()
		return !a1.watchHi
	})
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.hiView["pc-a"] || !srv.hiView["pc-b"] || srv.focusID != "pc-b" {
		t.Fatal("单焦点高清查看状态错误")
	}
}

// 目录收取：路径校验、配额与逐设备结果回执。
func TestCollectDirConstraints(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("homework"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 稀疏大文件：超过单文件上限应被跳过。
	big := filepath.Join(dir, "big.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxStudentUploadSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	a := NewAgent("pc-c", "PC-C")
	alertResult := func() (string, string) {
		a.pendMu.Lock()
		defer a.pendMu.Unlock()
		if len(a.pendAlerts) == 0 {
			return "", ""
		}
		last := a.pendAlerts[len(a.pendAlerts)-1]
		a.pendAlerts = nil
		return last.Stat, last.Detail
	}

	// 不在白名单的绝对路径被拒（白名单在 agent 侧，不只管理端）
	outside := t.TempDir()
	a.collectDir(nil, CollectOpenArgs{Dir: outside, Glob: "*"})
	if stat, d := alertResult(); stat != "denied" {
		t.Fatalf("白名单外路径未被拒: stat=%q %q", stat, d)
	}

	// 注入临时根目录模拟学校预设作业目录（TempDir 在 macOS 上经
	// /var→/private/var 链接，白名单校验用的是解析后的真实路径）。
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldRoots := AllowedCollectRoots
	AllowedCollectRoots = []string{resolvedDir, dir}
	defer func() { AllowedCollectRoots = oldRoots }()

	// 目录链接逃逸：白名单目录里的符号链接指向外面 → 拒绝。
	// Windows 上创建符号链接需要管理员或开发人员模式。权限不足时跳过
	// 这一段，目录 junction 逃逸由 TestCollectWindowsJunctionAndSymlink 覆盖。
	escape := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		if !symlinkPrivilegeDenied(err) {
			t.Fatalf("建符号链接失败: %v", err)
		}
		t.Logf("跳过符号链接逃逸（当前进程不能创建符号链接）: %v", err)
	} else {
		a.collectDir(nil, CollectOpenArgs{Dir: escape, Glob: "*"})
		if stat, d := alertResult(); stat != "denied" {
			t.Fatalf("目录符号链接逃逸未被拒: stat=%q %q", stat, d)
		}

		// 文件级逃逸：目录合法但文件是链接 → 跳过不计尝试。
		secret := filepath.Join(outside, "secret.txt")
		if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(secret, filepath.Join(dir, "linked.txt")); err != nil {
			if !symlinkPrivilegeDenied(err) {
				t.Fatalf("建文件链接失败: %v", err)
			}
			t.Logf("跳过文件符号链接（当前进程不能创建符号链接）: %v", err)
		}
	}

	// 越界语法被拒
	a.collectDir(nil, CollectOpenArgs{Dir: "..\\Windows", Glob: "*"})
	if stat, d := alertResult(); stat != "denied" || !strings.Contains(d, "路径无效") {
		t.Fatalf("越界路径未被拒: stat=%q %q", stat, d)
	}
	// 无匹配
	a.collectDir(nil, CollectOpenArgs{Dir: dir, Glob: "*.zzz"})
	if stat, d := alertResult(); stat != "empty" || !strings.Contains(d, "无匹配文件") {
		t.Fatalf("无匹配未上报: stat=%q %q", stat, d)
	}
	// 有匹配但离线：上传全部失败也要如实上报，大文件与链接文件被
	// 跳过——只剩 a.txt 一个真实文件进入尝试。
	a.collectDir(nil, CollectOpenArgs{Dir: dir, Glob: "*.txt"})
	if stat, d := alertResult(); stat != "fail" || !strings.Contains(d, "上传失败") {
		t.Fatalf("离线上传失败未上报: stat=%q %q", stat, d)
	} else if !strings.Contains(d, "1 个文件") {
		t.Fatalf("链接文件竟被计入尝试: %q", d)
	}
}

func TestNormalizeCollectPath(t *testing.T) {
	old := AllowedCollectRoots
	AllowedCollectRoots = []string{`C:\Users\Public`, `C:\LabHelper\Homework`}
	t.Cleanup(func() { AllowedCollectRoots = old })

	for _, p := range []string{
		`C:\Users\Public\Homework`,
		`c:\users\public\homework`,
		`\\?\C:\Users\Public\Homework`,
		`\??\C:\LabHelper\Homework\a.txt`,
	} {
		if !collectPathInRoots(p) {
			t.Fatalf("合法路径被拒: %s", p)
		}
	}
	for _, p := range []string{
		`C:\Users\PublicX`,
		`\\?\C:\Users\PublicX`,
		`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\Users\Public`,
		`\\server\share\Users\Public`,
	} {
		if collectPathInRoots(p) {
			t.Fatalf("白名单外路径被接受: %s", p)
		}
	}
}

// symlinkPrivilegeDenied 只在 Windows 上把「没有创建符号链接的权限」当成环境限制。
// 其他错误（以及非 Windows）仍是测试失败。
func symlinkPrivilegeDenied(err error) bool {
	if err == nil || runtime.GOOS != "windows" {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "privilege") || os.IsPermission(err)
}

// udpDiscoveryBound 探测 UDP 发现端口是否已被占用。探测本身会短暂 bind，
// 调用方不要在已经指望自己持有该端口的紧循环里把它当成无副作用的查询。
func udpDiscoveryBound() bool {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{Port: UDPPort})
	if err != nil {
		return true
	}
	_ = c.Close()
	return false
}

// 发现套接字必须随 Server 取消立刻释放。旧实现要等最多 2s 的读超时，
// 热重启和下一个监听方会空等，这段时间旧公钥信标还会污染发现结果。
func TestDiscoverReleasesPortPromptly(t *testing.T) {
	if udpDiscoveryBound() {
		t.Skip("UDP 47822 已被占用")
	}
	id, err := GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(id, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Run(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "UDP 发现端口被占用", udpDiscoveryBound)
	cancel()
	waitFor(t, time.Second, "取消后立即释放 UDP 发现端口", func() bool { return !udpDiscoveryBound() })
}
