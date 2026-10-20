package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"lab-assistant/internal/lan"
)

// ---- 功能目录 ---------------------------------------------------------------

// Feature describes one protection/policy switch a client can enforce.
type Feature struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// FeatureGroup is a section of the per-device feature checklist.
type FeatureGroup struct {
	Title    string    `json:"title"`
	Features []Feature `json:"features"`
}

func catalog() []FeatureGroup {
	return []FeatureGroup{
		{Title: "脱控防护", Features: []Feature{
			{Key: "guard_offline", Name: "断网/拔网线检测", Desc: "检测到断网时自动锁定并告警"},
			{Key: "guard_process", Name: "防终止/挂起进程", Desc: "保护电子教室与小助手进程不被结束或挂起"},
			{Key: "guard_ip", Name: "IP 地址篡改恢复", Desc: "学生修改 IP 后自动恢复并上报"},
			{Key: "guard_firewall", Name: "防火墙阻止检测", Desc: "防止勾选「阻止所有传入连接」脱控"},
			{Key: "guard_vdesk", Name: "虚拟桌面防护", Desc: "阻止用 Win+Tab 虚拟桌面脱离控制"},
			{Key: "guard_safemode", Name: "安全模式防护", Desc: "开机进入安全模式时蓝屏锁定"},
		}},
		{Title: "系统限制", Features: []Feature{
			{Key: "lock_taskmgr", Name: "禁用任务管理器", Desc: "含性能页的资源监视器"},
			{Key: "lock_regedit", Name: "禁用注册表", Desc: "regedit 与策略编辑器"},
			{Key: "lock_cmd", Name: "禁用命令行", Desc: "cmd / PowerShell / 脚本宿主"},
			{Key: "lock_settings", Name: "禁用控制面板与设置", Desc: "防止修改网络与系统配置"},
		}},
		{Title: "外设与存储", Features: []Feature{
			{Key: "usb_storage", Name: "禁用 U 盘/移动存储", Desc: "保留 USB 键鼠，仅拦截存储设备"},
			{Key: "usb_bootguard", Name: "PE 启动防护提醒", Desc: "检测到 PE 工具痕迹时上报告警"},
		}},
		{Title: "网络与浏览", Features: []Feature{
			{Key: "net_download", Name: "禁止浏览器下载", Desc: "阻止下载破解工具与安装包"},
			{Key: "net_netdisk", Name: "禁止网盘网站", Desc: "屏蔽常见网盘登录页"},
			{Key: "net_gamesite", Name: "禁止游戏网站", Desc: "如 poki.com、digdig.io"},
		}},
		{Title: "游戏与小程序", Features: []Feature{
			{Key: "game_builtin", Name: "禁系统小游戏", Desc: "扫雷、纸牌、拼图板、放大镜"},
			{Key: "game_browser", Name: "禁浏览器小游戏", Desc: "Chrome 小恐龙、Edge 冲浪"},
		}},
		{Title: "桌面", Features: []Feature{
			{Key: "wallpaper", Name: "机房行为规范壁纸", Desc: "统一桌面壁纸，附行为规范"},
			{Key: "show_id", Name: "右上角显示机器编号", Desc: "取计算机名后 6 位"},
		}},
	}
}

func allFeatureKeys() []string {
	var keys []string
	for _, g := range catalog() {
		for _, f := range g.Features {
			keys = append(keys, f.Key)
		}
	}
	return keys
}

// ---- 前端服务 -----------------------------------------------------------------

// Device 是给 UI 的设备视图。
type Device struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	IP       string   `json:"ip"`
	MAC      string   `json:"mac"`
	Online   bool     `json:"online"`
	HelperOn bool     `json:"helperOn"`
	Version  string   `json:"version"`
	Alerts   int      `json:"alerts"`
	Features []string `json:"features"`
	LastSeen string   `json:"lastSeen"`
}

// ClientStatus is what the student-side helper window shows about itself.
type ClientStatus struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	IP       string   `json:"ip"`
	HelperOn bool     `json:"helperOn"`
	Features []string `json:"features"`
}

func toView(d lan.Device) Device {
	return Device{
		ID: d.ID, Name: d.Name, Label: d.Label, IP: d.IP, MAC: d.MAC,
		Online: d.Online, HelperOn: d.HelperOn, Version: d.Version,
		Alerts: d.Alerts, Features: d.Features, LastSeen: d.LastSeenText(),
	}
}

// Lab 是绑定给前端的门面：UI 操作 → LAN 指令下发；LAN 变更 → UI 事件。
type Lab struct {
	srv     *lan.Server
	dataDir string
	win     *mygo.Window // 管理端窗口（对话框 parent）
}

// Devices returns the current device list.
func (l *Lab) Devices() []Device {
	devs := l.srv.Devices()
	out := make([]Device, 0, len(devs))
	for _, d := range devs {
		out = append(out, toView(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Catalog returns the feature checklist definition.
func (l *Lab) Catalog() []FeatureGroup { return catalog() }

// SetHelper 下发启用/停用指令。返回成功下达的台数（离线设备不计）。
// 并发下发，避免离线设备的超时拖慢整批。
func (l *Lab) SetHelper(ids []string, on bool) int {
	var mu sync.Mutex
	n := 0
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ack, err := l.srv.SendCommand(id, lan.OpSetHelper, lan.SetHelperArgs{On: on})
			if err == nil && ack.OK {
				mu.Lock()
				n++
				mu.Unlock()
			} else {
				log.Printf("lab: 下发 helper.set 到 %s 失败: %v %s", id, err, ack.Err)
			}
		}(id)
	}
	wg.Wait()
	return n
}

// SetFeatures replaces the enabled feature set of one device.
func (l *Lab) SetFeatures(id string, keys []string) Device {
	if _, err := l.srv.SendCommand(id, lan.OpSetFeatures, lan.SetFeaturesArgs{Keys: keys}); err != nil {
		log.Printf("lab: 下发 features.set 到 %s 失败: %v", id, err)
	}
	// 回读注册表（agent ack + status 后已更新）
	d, _ := l.srv.GetDevice(id)
	return toView(d)
}

// ApplyTemplate sets the same feature set on many devices at once.
func (l *Lab) ApplyTemplate(ids []string, keys []string) int {
	var mu sync.Mutex
	n := 0
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := l.srv.SendCommand(id, lan.OpSetFeatures, lan.SetFeaturesArgs{Keys: keys}); err == nil {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	return n
}

// ClearAlerts resets a device's alert counter.
func (l *Lab) ClearAlerts(id string) { l.srv.ClearAlerts(id) }

// Scan re-discovers devices on the LAN. 发现是被动的（agent 自己找来），
// 这里只做一次注册表刷新。
func (l *Lab) Scan() []Device { return l.Devices() }

// Status returns what the client-side helper window should display.
// 演示：本机扮演 pc-02。
func (l *Lab) Status() ClientStatus {
	d, ok := l.srv.GetDevice("pc-02")
	if !ok {
		return ClientStatus{Name: "LAB301-PC02", Label: "A-02"}
	}
	return ClientStatus{
		Name: d.Name, Label: d.Label, IP: d.IP,
		HelperOn: d.HelperOn, Features: d.Features,
	}
}

// Exit checks the admin password before disabling the local helper.
// Mock password: 123456.
func (l *Lab) Exit(password string) bool {
	if password != "123456" {
		return false
	}
	l.SetHelper([]string{"pc-02"}, false)
	return true
}

// ---- 分发 --------------------------------------------------------------------

// AdminInfo 给「分发」对话框展示的管理端信息。
type AdminInfo struct {
	IPs         []string `json:"ips"`
	Fingerprint string   `json:"fingerprint"`
	Port        int      `json:"port"`
	PubB64      string   `json:"pubB64"`
	DataDir     string   `json:"dataDir"`
}

// AdminInfo returns the admin server's addresses and identity.
func (l *Lab) AdminInfo() AdminInfo {
	return AdminInfo{
		IPs:         lan.LocalIPv4s(),
		Fingerprint: l.srv.ID.Fingerprint(),
		Port:        l.srv.TCPPort,
		PubB64:      l.srv.ID.PubB64(),
		DataDir:     l.dataDir,
	}
}

// ExportClientPackage 生成学生机安装包到所选目录，返回实际输出路径。
// 含 client.json（已写入管理端地址+公钥）、install.bat、README.txt，
// 以及 agent.exe（若已用 GOOS=windows go build ./cmd/client 构建过）。
func (l *Lab) ExportClientPackage() string {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:            l.win,
		Title:             "选择客户端安装包输出目录",
		ButtonLabel:       "导出到这里",
		Directory:         true,
		CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return "" // 用户取消
	}
	out := filepath.Join(paths[0], "client-package")
	if err := writeClientPackage(out, l.srv); err != nil {
		log.Printf("导出安装包失败: %v", err)
		return ""
	}
	return out
}

func writeClientPackage(dir string, srv *lan.Server) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ip := ""
	if ips := lan.LocalIPv4s(); len(ips) > 0 {
		ip = ips[0]
	}
	cfg := lan.AgentConfig{
		AdminAddr: fmt.Sprintf("%s:%d", ip, srv.TCPPort),
		AdminPub:  srv.ID.PubB64(),
		Room:      "301",
	}
	if err := cfg.Save(filepath.Join(dir, "client.json")); err != nil {
		return err
	}
	// agent.exe：优先取已交叉编译的 cmd/client 产物
	agentSrc := ""
	for _, p := range []string{"./agent.exe", "./build/windows-amd64/agent.exe", "./cmd/client/agent.exe"} {
		if _, err := os.Stat(p); err == nil {
			agentSrc = p
			break
		}
	}
	agentNote := "agent.exe 未随包导出（未发现已构建的客户端，请先：GOOS=windows go build -o agent.exe ./cmd/client）"
	if agentSrc != "" {
		b, err := os.ReadFile(agentSrc)
		if err == nil {
			os.WriteFile(filepath.Join(dir, "agent.exe"), b, 0o755)
			agentNote = "agent.exe 已随包导出"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "install.bat"), []byte(installBat), 0o644); err != nil {
		return err
	}
	readme := fmt.Sprintf(`机房小助手·客户端安装包
========================

【重要】学生机都有还原软件，必须在「非还原/解冻状态」下安装！

步骤：
  1. 把整个 client-package 拷到学生机（U盘/共享目录）
  2. 确认还原软件处于解冻状态（手动维护模式）
  3. 右键 install.bat → 以管理员身份运行
     - 拷贝 agent.exe + client.json 到 C:\LabHelper
     - 注册开机自启（计划任务，SYSTEM 权限）
     - 对安装目录设 ACL（普通用户只读）
     - 首次运行自动生成机器 id/token 并固化进 client.json
  4. 重启验证管理端能看到这台机器上线
  5. 冻结系统（创建还原点）

教师机换了 IP？agent 连不上配置地址时会自动 UDP 广播找管理端，
公钥匹配即可换地址重连，无需重装。

%s
`, agentNote)
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(readme), 0o644)
}

const installBat = `@echo off
rem ============================================================
rem  机房小助手·客户端安装 —— 必须在还原软件解冻状态下运行！
rem ============================================================
set DIR=C:\LabHelper
if not exist %DIR% mkdir %DIR%
copy /y "%~dp0agent.exe" "%DIR%\agent.exe"
copy /y "%~dp0client.json" "%DIR%\client.json"

rem 开机自启：SYSTEM 权限计划任务（先于用户登录启动）
schtasks /create /tn "LabHelper" /tr "\"%DIR%\agent.exe\"" /sc onstart /ru SYSTEM /rl HIGHEST /f

rem 安装目录 ACL：仅 SYSTEM/Administrators 可写，Users 只读执行
icacls %DIR% /inheritance:r /grant:r "SYSTEM:F" "Administrators:F" /grant:r "Users:RX"

rem 首启固化 id/token，然后启动
"%DIR%\agent.exe" -id
start "" "%DIR%\agent.exe"

echo.
echo 安装完成。确认管理端能看到本机上线后，请创建还原点（冻结）。
pause
`

// DevicesChanged is pushed to every open window when device state changes.
var DevicesChanged = mygo.NewEvent[[]Device]("devices")

// ---- 组装 --------------------------------------------------------------------

func seedRegistry(srv *lan.Server) {
	core := []string{
		"guard_offline", "guard_process", "guard_ip",
		"lock_taskmgr", "lock_regedit", "lock_cmd",
		"usb_storage", "net_netdisk", "wallpaper", "show_id",
	}
	for i := 1; i <= 24; i++ {
		srv.Seed(lan.Device{
			ID:       fmt.Sprintf("pc-%02d", i),
			Name:     fmt.Sprintf("LAB301-PC%02d", i),
			Label:    fmt.Sprintf("A-%02d", i),
			IP:       fmt.Sprintf("192.168.31.%d", 100+i),
			MAC:      fmt.Sprintf("54:EE:75:0A:1B:%02X", i),
			Version:  "13.1",
			Features: append([]string{}, core...),
			HelperOn: true,
		})
	}
}

// spawnDemoAgents 在进程内跑一批 loopback agent，让管理端吃到真实协议数据。
// LAB_DEMO_AGENTS=0 可关闭。
func spawnDemoAgents(ctx context.Context, srv *lan.Server) {
	raw := os.Getenv("LAB_DEMO_AGENTS")
	n, _ := strconv.Atoi(raw)
	if raw == "" {
		n = 21 // 默认 21 台，留 3 台离线
	}
	offline := map[int]bool{11: true, 15: true, 20: true}
	helperOff := map[int]bool{4: true, 9: true}
	for i := 1; i <= n && i <= 24; i++ {
		if offline[i] {
			continue
		}
		a := lan.NewAgent(fmt.Sprintf("pc-%02d", i), fmt.Sprintf("LAB301-PC%02d", i))
		a.Label = fmt.Sprintf("A-%02d", i)
		a.IP = fmt.Sprintf("192.168.31.%d", 100+i)
		a.MAC = fmt.Sprintf("54:EE:75:0A:1B:%02X", i)
		a.PinnedPub = srv.ID.Pub()
		a.DirectAddr = fmt.Sprintf("127.0.0.1:%d", srv.TCPPort)
		if helperOff[i] {
			a.SetHelper(false)
		}
		a.SetFeatures([]string{
			"guard_offline", "guard_process", "guard_ip",
			"lock_taskmgr", "lock_regedit", "lock_cmd",
			"usb_storage", "net_netdisk", "wallpaper", "show_id",
		})
		go a.Run(ctx)
	}
	// 演示告警：pc-07 上线后报一条 U 盘插入
	go func() {
		time.Sleep(4 * time.Second)
		srv.InjectAlert("pc-07", "usb_detected", "检测到 U 盘插入（演示）")
		srv.InjectAlert("pc-07", "taskmgr_open", "尝试打开任务管理器被拦截（演示）")
		srv.InjectAlert("pc-18", "net_unplug", "网卡断开 3 秒（演示）")
	}()
}

func main() {
	// 数据目录：~/Library/Application Support/lab-assistant（macOS）/
	// %AppData%\lab-assistant（Windows）。身份密钥与设备注册表持久化于此。
	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		dataDir = "."
	}
	id, err := lan.LoadOrCreateIdentity(filepath.Join(dataDir, "admin.key"))
	if err != nil {
		log.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	srv.SetPersistPath(filepath.Join(dataDir, "registry.json"))
	if len(srv.Devices()) == 0 {
		seedRegistry(srv) // 首启种子；之后以持久化注册表为准
	}
	lab := &Lab{srv: srv, dataDir: dataDir}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
	srv.OnChange = func() { broadcast(lab.Devices()) }
	spawnDemoAgents(ctx, srv)

	mygo.Bind(lab)
	mygo.App.WhenReady(func() {
		lab.win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "机房小助手 · 管理端",
			Width:           1080,
			Height:          720,
			MinWidth:        860,
			MinHeight:       540,
			URL:             "/",
			StateKey:        "admin",
			BackgroundColor: "light-dark(#f4f4f6, #1a1a1e)",
		})
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "机房小助手",
			Width:           400,
			Height:          560,
			MinWidth:        360,
			MinHeight:       480,
			URL:             "/client.html",
			StateKey:        "client",
			BackgroundColor: "light-dark(#f4f4f6, #1a1a1e)",
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func broadcast(devs []Device) {
	for _, w := range mygo.Windows() {
		DevicesChanged.Emit(w, devs)
	}
}
