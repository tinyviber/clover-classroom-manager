package main

import (
	"context"
	"embed"
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/tinyviber/clover-classroom-manager/internal/capture"
	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

// Client package scripts are shared by in-app export and GitHub release bundles.
//
//go:embed packaging/windows/client-package/install.bat packaging/windows/client-package/uninstall.bat packaging/windows/client-package/verify-config.ps1 packaging/windows/client-package/README.txt
var clientPackageAssets embed.FS

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

	castMu      sync.Mutex
	classroomMu sync.Mutex // 序列化教师广播与学生演示的启停/冲突检查。
	castCancel  context.CancelFunc
	castMode    string // "" | "fullscreen" | "window"
	castIDs     []string
}

// ScreenFrame is a base64 JPEG thumbnail for the management view.
type ScreenFrame struct {
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
	Name     string `json:"name"`
	JPEG     string `json:"jpeg"`
	At       int64  `json:"at"` // Unix milliseconds
}

type ClassroomSignReply struct {
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
	Name     string `json:"name"`
	At       string `json:"at"`
}

type ClassroomSignIn struct {
	Open      bool                 `json:"open"`
	ID        string               `json:"id"`
	Deadline  int64                `json:"deadline"`
	Sent      int                  `json:"sent"`
	TargetIDs []string             `json:"targetIds"`
	Replies   []ClassroomSignReply `json:"replies"`
	Online    int                  `json:"online"`
}

type ClassroomFileProgress struct {
	ID       string `json:"id"`
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Sent     int64  `json:"sent"`
	Done     bool   `json:"done"`
	In       bool   `json:"in"`
	Err      string `json:"err,omitempty"`
	At       string `json:"at"`
}

type ClassroomHelpRequest struct {
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
	Name     string `json:"name"`
	At       string `json:"at"`
}

type ClassroomPollReply struct {
	DeviceID string `json:"deviceId"`
	Label    string `json:"label"`
	Option   string `json:"option"`
	At       string `json:"at"`
}

type ClassroomPoll struct {
	Open      bool                 `json:"open"`
	ID        string               `json:"id"`
	Question  string               `json:"question"`
	Options   []string             `json:"options"`
	Deadline  int64                `json:"deadline"`
	Sent      int                  `json:"sent"`
	TargetIDs []string             `json:"targetIds"`
	Replies   []ClassroomPollReply `json:"replies"`
}

type ClassroomDemo struct {
	State       string   `json:"state"`
	SessionID   string   `json:"sessionId"`
	SourceID    string   `json:"sourceId"`
	SourceLabel string   `json:"sourceLabel"`
	TargetIDs   []string `json:"targetIds"`
	RequestedAt string   `json:"requestedAt"`
	Err         string   `json:"err,omitempty"`
}

// ClassroomState is the push snapshot for classroom activities.
type ClassroomState struct {
	Screens        []ScreenFrame           `json:"screens"`
	ScreenWatchIDs []string                `json:"screenWatchIds"`
	SignIn         ClassroomSignIn         `json:"signIn"`
	Files          []ClassroomFileProgress `json:"files"`
	Helps          []ClassroomHelpRequest  `json:"helps"`
	Poll           ClassroomPoll           `json:"poll"`
	Demo           ClassroomDemo           `json:"demo"`
	CastMode       string                  `json:"castMode"`
	CastTargetIDs  []string                `json:"castTargetIds"`
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
	// agent.exe：优先取当前版本的交叉编译产物，再兼容旧路径。
	agentSrc := ""
	candidates := []string{"./build/windows-amd64/agent.exe", "./cmd/client/agent.exe", "./agent.exe"}
	if executable, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(executable)
		candidates = append([]string{filepath.Join(exeDir, "agent.exe"), filepath.Join(exeDir, "build", "windows-amd64", "agent.exe")}, candidates...)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			agentSrc = p
			break
		}
	}
	agentNote := "agent.exe 未随包导出（未发现已构建的客户端，请先：mkdir -p build/windows-amd64 && GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o build/windows-amd64/agent.exe ./cmd/client）"
	if agentSrc != "" {
		b, err := os.ReadFile(agentSrc)
		if err != nil {
			return fmt.Errorf("读取客户端程序 %s: %w", agentSrc, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agent.exe"), b, 0o755); err != nil {
			return fmt.Errorf("写入客户端程序: %w", err)
		}
		agentNote = "agent.exe 已随包导出"
	}
	for _, name := range []string{"install.bat", "uninstall.bat", "verify-config.ps1"} {
		if err := writeClientPackageAsset(dir, name); err != nil {
			return err
		}
	}
	readme, err := clientPackageAssets.ReadFile("packaging/windows/client-package/README.txt")
	if err != nil {
		return err
	}
	readmeText := strings.ReplaceAll(string(readme), "@VERSION@", "随当前管理端版本")
	readmeText = strings.ReplaceAll(readmeText, "@AGENT_STATUS@", agentNote)
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(readmeText), 0o644)
}

func writeClientPackageAsset(dir, name string) error {
	data, err := clientPackageAssets.ReadFile("packaging/windows/client-package/" + name)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}

// DevicesChanged is pushed to every open window when device state changes.
var DevicesChanged = mygo.NewEvent[[]Device]("devices")
var ClassroomChanged = mygo.NewEvent[ClassroomState]("classroom")

var classroomPushMu sync.Mutex
var classroomPushTimer *time.Timer

func scheduleClassroomPush(lab *Lab) {
	classroomPushMu.Lock()
	if classroomPushTimer == nil {
		classroomPushTimer = time.AfterFunc(350*time.Millisecond, func() {
			classroomPushMu.Lock()
			classroomPushTimer = nil
			classroomPushMu.Unlock()
			broadcastClassroom(lab.ClassroomState())
		})
	}
	classroomPushMu.Unlock()
}

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
			Version:  "0.0.2",
			Features: append([]string{}, core...),
			HelperOn: true,
		})
	}
}

func demoAgentCount() int {
	raw := os.Getenv("LAB_DEMO_AGENTS")
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		if err != nil {
			log.Printf("LAB_DEMO_AGENTS 无效，演示设备已关闭: %v", err)
		}
		return 0
	}
	return n
}

// spawnDemoAgents 在进程内跑一批 loopback agent，让管理端吃到真实协议数据。
func spawnDemoAgents(ctx context.Context, srv *lan.Server, n int) {
	if n <= 0 {
		return
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
		a.Token = lan.DemoToken(srv.ID, a.ID)
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
	recvDir := filepath.Join(dataDir, "received")
	if err := os.MkdirAll(recvDir, 0o700); err != nil {
		log.Fatal(err)
	}
	srv.SetRecvDir(recvDir)
	demoAgents := demoAgentCount()
	if len(srv.Devices()) == 0 && demoAgents > 0 {
		seedRegistry(srv) // 首启种子；之后以持久化注册表为准
	}
	lab := &Lab{srv: srv, dataDir: dataDir}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
	srv.OnChange = func() {
		broadcast(lab.Devices())
		scheduleClassroomPush(lab)
	}
	srv.OnScreens = func() { scheduleClassroomPush(lab) }
	spawnDemoAgents(ctx, srv, demoAgents)

	mygo.Bind(lab)
	mygo.App.WhenReady(func() {
		lab.win = mygo.NewWindow(mygo.WindowOptions{
			Title:           "三叶草局域网课堂管理 · 管理端",
			Width:           1080,
			Height:          720,
			MinWidth:        860,
			MinHeight:       540,
			URL:             "/",
			StateKey:        "admin",
			BackgroundColor: "light-dark(#f4f4f6, #1a1a1e)",
		})
		mygo.NewWindow(mygo.WindowOptions{
			Title:           "三叶草局域网课堂管理",
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

func broadcastClassroom(state ClassroomState) {
	for _, w := range mygo.Windows() {
		ClassroomChanged.Emit(w, state)
	}
}

// ---- v0.0.2：屏幕墙 / 广播 / 签到 / 文件 / 课堂互动 ---------------------------

// ClassroomState returns the current classroom snapshot, including the latest frames.
func (l *Lab) ClassroomState() ClassroomState {
	frames := l.srv.Screens()
	state := ClassroomState{
		Screens:        make([]ScreenFrame, 0, len(frames)),
		ScreenWatchIDs: l.srv.ScreenWatchTargets(),
		Files:          make([]ClassroomFileProgress, 0),
		Helps:          make([]ClassroomHelpRequest, 0),
		CastMode:       l.CastMode(),
		CastTargetIDs:  l.srv.CastTargets(),
	}
	for _, frame := range frames {
		state.Screens = append(state.Screens, ScreenFrame{
			DeviceID: frame.DeviceID,
			Label:    frame.Label,
			Name:     frame.Name,
			JPEG:     base64.StdEncoding.EncodeToString(frame.JPEG),
			At:       frame.At.UnixMilli(),
		})
	}
	sort.Slice(state.Screens, func(i, j int) bool { return state.Screens[i].Label < state.Screens[j].Label })

	sign := l.srv.SignInState()
	state.SignIn = ClassroomSignIn{
		Open: sign.Open, ID: sign.ID, Deadline: sign.Deadline,
		Sent: sign.Sent, TargetIDs: append([]string{}, sign.TargetIDs...),
		Online: sign.Online, Replies: make([]ClassroomSignReply, 0, len(sign.Replies)),
	}
	for _, reply := range sign.Replies {
		state.SignIn.Replies = append(state.SignIn.Replies, ClassroomSignReply{
			DeviceID: reply.DeviceID, Label: reply.Label, Name: reply.Name, At: activityTime(reply.At),
		})
	}

	for _, transfer := range l.srv.FileTransfers() {
		state.Files = append(state.Files, ClassroomFileProgress{
			ID: transfer.ID, DeviceID: transfer.DeviceID, Label: transfer.Label, Name: transfer.Name,
			Size: transfer.Size, Sent: transfer.Sent, Done: transfer.Done, In: transfer.In,
			Err: transfer.Err, At: activityTime(transfer.At),
		})
	}
	for _, request := range l.srv.HelpRequests() {
		state.Helps = append(state.Helps, ClassroomHelpRequest{
			DeviceID: request.DeviceID, Label: request.Label, Name: request.Name, At: activityTime(request.At),
		})
	}

	poll := l.srv.PollState()
	state.Poll = ClassroomPoll{
		Open: poll.Open, ID: poll.ID, Question: poll.Question, Options: append([]string{}, poll.Options...),
		Deadline: poll.Deadline, Sent: poll.Sent, TargetIDs: append([]string{}, poll.TargetIDs...),
		Replies: make([]ClassroomPollReply, 0, len(poll.Replies)),
	}
	for _, reply := range poll.Replies {
		state.Poll.Replies = append(state.Poll.Replies, ClassroomPollReply{
			DeviceID: reply.DeviceID, Label: reply.Label, Option: reply.Option, At: activityTime(reply.At),
		})
	}

	demo := l.srv.DemoState()
	state.Demo = ClassroomDemo{
		State: demo.State, SessionID: demo.SessionID, SourceID: demo.SourceID,
		SourceLabel: demo.SourceLabel, TargetIDs: append([]string{}, demo.TargetIDs...),
		RequestedAt: activityTime(demo.RequestedAt), Err: demo.Err,
	}
	return state
}

func activityTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// WatchScreens toggles low-rate thumbnail capture for the selected online devices.
func (l *Lab) WatchScreens(ids []string, on bool) {
	l.srv.SetScreenWatch(ids, on)
	scheduleClassroomPush(l)
}

// CastStart captures the teacher display and sends bounded frames to selected clients.
func (l *Lab) CastStart(mode string, ids []string) bool {
	l.classroomMu.Lock()
	defer l.classroomMu.Unlock()
	if mode != "window" && mode != "fullscreen" {
		return false
	}
	if demo := l.srv.DemoState(); demo.State == "pending" || demo.State == "active" {
		return false
	}
	targets := l.srv.OnlineIDs(ids)
	if len(targets) == 0 {
		return false
	}
	l.castMu.Lock()
	oldIDs := append([]string(nil), l.castIDs...)
	if l.castCancel != nil {
		l.castCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.castCancel, l.castMode, l.castIDs = cancel, mode, targets
	l.castMu.Unlock()
	l.srv.SetCastState(mode, targets)
	if len(oldIDs) > 0 {
		l.srv.Broadcast(lan.Msg{Type: lan.MsgCommand, Cmd: &lan.CommandMsg{
			ID: fmt.Sprintf("cast-stop-%d", time.Now().UnixNano()), Op: lan.OpCastStop,
		}}, oldIDs)
	}
	l.srv.StopCastFrames(oldIDs)
	l.srv.Broadcast(lan.Msg{Type: lan.MsgCommand, Cmd: &lan.CommandMsg{
		ID: fmt.Sprintf("cast-start-%d", time.Now().UnixNano()), Op: lan.OpCastStart,
		Args: lan.CastStartArgs{Mode: mode},
	}}, targets)
	go l.castLoop(ctx, mode, targets)
	scheduleClassroomPush(l)
	return true
}

func (l *Lab) CastStop() {
	l.classroomMu.Lock()
	defer l.classroomMu.Unlock()
	l.castMu.Lock()
	cancel, ids := l.castCancel, append([]string(nil), l.castIDs...)
	l.castCancel, l.castMode, l.castIDs = nil, "", nil
	l.castMu.Unlock()
	l.srv.SetCastState("", nil)
	if cancel != nil {
		cancel()
	}
	l.srv.StopCastFrames(ids)
	if len(ids) > 0 {
		l.srv.Broadcast(lan.Msg{Type: lan.MsgCommand, Cmd: &lan.CommandMsg{
			ID: fmt.Sprintf("cast-stop-%d", time.Now().UnixNano()), Op: lan.OpCastStop,
		}}, ids)
	}
	scheduleClassroomPush(l)
}

func (l *Lab) CastMode() string {
	l.castMu.Lock()
	defer l.castMu.Unlock()
	return l.castMode
}

func (l *Lab) castLoop(ctx context.Context, mode string, targets []string) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		jpeg, err := capture.FrameMode(mode, 800, 45)
		if err != nil {
			continue
		}
		l.srv.QueueCastFrame(targets, jpeg)
	}
}

func (l *Lab) SignInStart(seconds int, ids []string) int {
	_, sent := l.srv.OpenSignIn(seconds, ids)
	return sent
}

func (l *Lab) SignInStop() { l.srv.CloseSignIn() }

func (l *Lab) PickFiles() []string {
	paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: l.win, Title: "选择要分发的文件", Multiple: true,
	})
	return paths
}

func (l *Lab) SendFiles(ids []string, paths []string) int {
	n := 0
	for _, path := range paths {
		n += l.srv.SendFile(ids, path)
	}
	return n
}

func (l *Lab) OpenRecvDir() string {
	dir := l.srv.RecvDir()
	if dir != "" {
		mygo.Shell.OpenPath(dir)
	}
	return dir
}

func (l *Lab) PollStart(question string, options []string, seconds int, ids []string) int {
	_, sent, err := l.srv.StartPoll(question, options, seconds, ids)
	if err != nil {
		log.Printf("lab: 发起随堂投票失败: %v", err)
		return 0
	}
	return sent
}

func (l *Lab) PollStop() { l.srv.StopPoll() }

func (l *Lab) ClearHelp(deviceID string) { l.srv.ClearHelp(deviceID) }

func (l *Lab) DemoStart(sourceID string, targetIDs []string) bool {
	l.classroomMu.Lock()
	defer l.classroomMu.Unlock()
	if l.CastMode() != "" {
		return false
	}
	if _, err := l.srv.StartDemo(sourceID, targetIDs); err != nil {
		log.Printf("lab: 发起学生演示失败: %v", err)
		return false
	}
	return true
}

func (l *Lab) DemoStop() {
	l.classroomMu.Lock()
	defer l.classroomMu.Unlock()
	l.srv.StopDemo()
}
