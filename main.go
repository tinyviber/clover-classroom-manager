package main

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/tinyviber/clover-classroom-manager/internal/capture"
	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

// Client package scripts are shared by in-app export and GitHub release bundles.
//
//go:embed packaging/windows/client-package/install.bat packaging/windows/client-package/uninstall.bat packaging/windows/client-package/verify-config.ps1 packaging/windows/client-package/README.txt
var clientPackageAssets embed.FS

// 三叶草托盘图标（tools/genicon 生成）。
//
//go:embed resources/tray.png
var trayIconPNG []byte

//go:embed resources/tray-template.png
var trayIconTemplatePNG []byte

// ---- 功能目录 ---------------------------------------------------------------

// Feature describes one protection/policy switch a client can enforce.
// Ready=false 表示该策略在客户端还没有执行实现（规划中）——
// UI 禁用勾选，也不计入「生效」数，避免把占位功能显示成已保护。
type Feature struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Ready bool   `json:"ready"`
}

// FeatureGroup is a section of the per-device feature checklist.
type FeatureGroup struct {
	Title    string    `json:"title"`
	Features []Feature `json:"features"`
}

func catalog() []FeatureGroup {
	return []FeatureGroup{
		{Title: "脱控防护", Features: []Feature{
			{Key: "guard_offline", Name: "断网/拔网线检测", Desc: "断线超过约 15 秒自动锁屏，重连后恢复", Ready: true},
			{Key: "guard_process", Name: "防终止/挂起进程", Desc: "保护电子教室与小助手进程不被结束或挂起"},
			{Key: "guard_ip", Name: "IP 地址篡改恢复", Desc: "学生修改 IP 后自动恢复并上报"},
			{Key: "guard_firewall", Name: "防火墙阻止检测", Desc: "防止勾选「阻止所有传入连接」脱控"},
			{Key: "guard_vdesk", Name: "虚拟桌面防护", Desc: "阻止用 Win+Tab 虚拟桌面脱离控制"},
			{Key: "guard_safemode", Name: "安全模式防护", Desc: "开机进入安全模式时蓝屏锁定"},
		}},
		{Title: "系统限制", Features: []Feature{
			{Key: "lock_taskmgr", Name: "禁用任务管理器", Desc: "HKCU 策略项 DisableTaskMgr（学生自助可改，正式版需服务级防护）", Ready: true},
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
	Pending  bool     `json:"pending"` // 待审批的新注册设备
	Locked   bool     `json:"locked"`  // 学生端真实锁定状态（status 上报）
	// FeatErrs 是 agent 落地失败的策略键（applied≠desired 时点名显示）
	FeatErrs []string `json:"featErrs,omitempty"`
	CastTCP  bool     `json:"castTcp"` // 广播时该设备走 TCP 兜底而非组播
	LastSeen string   `json:"lastSeen"`
}

func toView(d lan.Device) Device {
	return Device{
		ID: d.ID, Name: d.Name, Label: d.Label, IP: d.IP, MAC: d.MAC,
		Online: d.Online, HelperOn: d.HelperOn, Version: d.Version,
		Alerts: d.Alerts, Features: d.Features, Pending: d.Pending,
		Locked: d.Locked, FeatErrs: d.FeatErrs, CastTCP: d.CastTCP,
		LastSeen: d.LastSeenText(),
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
	castQuality string // 画质档："perfect"|"better"|"normal"|"low"
	castTitle   string // 窗口广播钉住的共享窗口标题（""=非窗口模式）
	castBar     *mygo.Window // 广播悬浮控制条（Panel 小窗）

	seatMu   sync.Mutex
	seatMap  map[string][2]int // 设备 ID → [行,列] 座位布局（持久化 seats.json）
	seatCols int               // 布局列数（默认 8）
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
	CastQuality    string                  `json:"castQuality"`
	CastTitle      string                  `json:"castTitle"` // 窗口模式共享的窗口标题
	FocusID        string                  `json:"focusId"`   // 正在高清查看的设备
	FocusScreen    *ScreenFrame            `json:"focusScreen,omitempty"`
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
	return l.broadcast(ids, lan.OpSetHelper, lan.SetHelperArgs{On: on})
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
	return l.broadcast(ids, lan.OpSetFeatures, lan.SetFeaturesArgs{Keys: keys})
}

// broadcast 向多台设备并发下发同一指令，返回成功（ack.OK）的台数。
func (l *Lab) broadcast(ids []string, op string, args any) int {
	var mu sync.Mutex
	n := 0
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			ack, err := l.srv.SendCommand(id, op, args)
			if err == nil && ack.OK {
				mu.Lock()
				n++
				mu.Unlock()
			} else {
				log.Printf("lab: 下发 %s 到 %s 失败: %v %s", op, id, err, ack.Err)
			}
		}(id)
	}
	wg.Wait()
	return n
}

// ScreenLock 锁定/解锁学生机屏幕（黑屏 + 锁键鼠，学生端无退出途径）。
func (l *Lab) ScreenLock(ids []string, on bool) int {
	if on {
		return l.broadcast(ids, lan.OpLockScreen, lan.LockScreenArgs{Reason: "教师锁定了屏幕"})
	}
	return l.broadcast(ids, lan.OpUnlockScreen, nil)
}

// PowerAction 远程重启/关机学生机。action: "restart" | "shutdown"。
// force=false 为普通关机（60s 倒计时、可取消）；force=true 5s 强关
// （有数据丢失风险，UI 须二次确认）。
func (l *Lab) PowerAction(ids []string, action string, force bool) int {
	op := lan.OpRestart
	switch action {
	case "shutdown":
		op = lan.OpShutdown
	case "restart":
	default:
		return 0
	}
	return l.broadcast(ids, op, lan.PowerArgs{Force: force})
}

// PowerCancel 取消学生机上进行中的关机/重启倒计时（shutdown /a）。
func (l *Lab) PowerCancel(ids []string) int {
	return l.broadcast(ids, lan.OpPowerCancel, nil)
}

// NotifyText 给学生机发教师消息（托盘气泡）。返回成功台数。
func (l *Lab) NotifyText(ids []string, text string) int {
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 200 {
		return 0
	}
	return l.broadcast(ids, lan.OpNotify, lan.NotifyArgs{Text: text})
}

// OpenURLFor 让所选学生机用默认浏览器打开教学网址（仅 http/https）。
func (l *Lab) OpenURLFor(ids []string, rawURL string) int {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return 0
	}
	return l.broadcast(ids, lan.OpOpenURL, lan.OpenURLArgs{URL: u.String()})
}

// LaunchAppFor 让所选学生机启动一个 .exe（绝对路径，学生端再做校验）。
func (l *Lab) LaunchAppFor(ids []string, path string) int {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasSuffix(strings.ToLower(path), ".exe") {
		return 0
	}
	return l.broadcast(ids, lan.OpLaunchApp, lan.LaunchArgs{Path: path})
}

// ViewDevice 开关某台学生机的高清大图查看（双击卡片）。返回是否有目标。
func (l *Lab) ViewDevice(id string, on bool) bool {
	if id == "" {
		return false
	}
	l.srv.ViewDevice(id, on)
	scheduleClassroomPush(l)
	return true
}

// Seats 返回座位布局（设备 ID → [行,列]）。
func (l *Lab) Seats() map[string][2]int {
	l.seatMu.Lock()
	defer l.seatMu.Unlock()
	out := make(map[string][2]int, len(l.seatMap))
	for id, rc := range l.seatMap {
		out[id] = rc
	}
	return out
}

// SeatCols 布局列数（教室常见 6-8 列）。
func (l *Lab) SeatCols() int {
	l.seatMu.Lock()
	defer l.seatMu.Unlock()
	return l.seatCols
}

// SetSeatCols 设置布局列数（会影响把 index 还原成行列）。
func (l *Lab) SetSeatCols(cols int) {
	if cols < 2 || cols > 20 {
		return
	}
	l.seatMu.Lock()
	l.seatCols = cols
	l.seatMu.Unlock()
	l.saveSeats()
}

// SaveSeats 保存座位布局（拖拽结果整体回写）。
func (l *Lab) SaveSeats(seats map[string][2]int) {
	l.seatMu.Lock()
	l.seatMap = seats
	l.seatMu.Unlock()
	l.saveSeats()
}

func (l *Lab) seatsPath() string { return filepath.Join(l.dataDir, "seats.json") }

func (l *Lab) loadSeats() {
	b, err := os.ReadFile(l.seatsPath())
	if err != nil {
		return
	}
	var saved struct {
		Cols  int              `json:"cols"`
		Seats map[string][2]int `json:"seats"`
	}
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	l.seatMu.Lock()
	if saved.Cols >= 2 && saved.Cols <= 20 {
		l.seatCols = saved.Cols
	}
	if saved.Seats != nil {
		l.seatMap = saved.Seats
	}
	l.seatMu.Unlock()
}

func (l *Lab) saveSeats() {
	l.seatMu.Lock()
	saved := struct {
		Cols  int              `json:"cols"`
		Seats map[string][2]int `json:"seats"`
	}{Cols: l.seatCols, Seats: l.seatMap}
	l.seatMu.Unlock()
	b, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(l.seatsPath(), b, 0o600)
}

// ApproveDevices 批准待注册设备（enrollment）。返回批准的台数。
func (l *Lab) ApproveDevices(ids []string) int { return l.srv.Approve(ids) }

// RejectDevices 拒绝待注册设备：移出注册表并断开连接。
func (l *Lab) RejectDevices(ids []string) int { return l.srv.Reject(ids) }

// ClearAlerts resets a device's alert counter.
func (l *Lab) ClearAlerts(id string) { l.srv.ClearAlerts(id) }

// Scan re-discovers devices on the LAN. 发现是被动的（agent 自己找来），
// 这里只做一次注册表刷新。
func (l *Lab) Scan() []Device { return l.Devices() }

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
func (l *Lab) ExportClientPackage(addr string) string {
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
	if err := writeClientPackage(out, l.srv, addr); err != nil {
		log.Printf("导出安装包失败: %v", err)
		return ""
	}
	return out
}

// writeClientPackage 生成安装包；addr 是教师选定的管理端 LAN 地址
// （空串退回第一张网卡——多网卡/VPN 机器上默认项未必是学生可达的）。
func writeClientPackage(dir string, srv *lan.Server, addr string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ip := addr
	if ip == "" {
		if ips := lan.LocalIPv4s(); len(ips) > 0 {
			ip = ips[0]
		}
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
	demoAgents := demoAgentCount()
	srv.AutoApprove = demoAgents > 0 // 演示模式跳过 enrollment
	srv.SetPersistPath(filepath.Join(dataDir, "registry.json"))
	recvDir := filepath.Join(dataDir, "received")
	if err := os.MkdirAll(recvDir, 0o700); err != nil {
		log.Fatal(err)
	}
	srv.SetRecvDir(recvDir)
	if len(srv.Devices()) == 0 && demoAgents > 0 {
		seedRegistry(srv) // 首启种子；之后以持久化注册表为准
	}
	lab := &Lab{srv: srv, dataDir: dataDir, seatMap: map[string][2]int{}, seatCols: 8}
	lab.loadSeats()
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
		// 托盘常驻应用：关主窗只隐藏不销毁，否则 FocusAdmin/托盘/悬浮条
		// 的「管理端」全变成死按钮（窗口销毁后 Show/Focus 是 no-op）。
		// 退出走托盘菜单的 Quit。
		lab.win.OnClose(func(e *mygo.CloseEvent) {
			e.PreventDefault()
			lab.win.Hide()
		})
		// 三叶草托盘图标（macOS 用模板剪影着色，Windows/Linux 用彩色）。
		icon, template := trayIconPNG, false
		if runtime.GOOS == "darwin" {
			icon, template = trayIconTemplatePNG, true
		}
		tray, err := mygo.NewTray(mygo.TrayOptions{
			Icon:           icon,
			IconIsTemplate: template,
			ToolTip:        "三叶草课堂管理 · 管理端",
			Menu: mygo.NewMenu([]*mygo.MenuItem{
				{ID: "show", Label: "显示管理端", Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
					lab.FocusAdmin()
				}},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			}),
		})
		if err != nil {
			log.Printf("lab: 托盘图标创建失败: %v", err)
		} else {
			tray.OnClick(func() { lab.FocusAdmin() })
		}
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
		if w.IsPanel() {
			// 悬浮条只关心广播状态，不推整屏 base64（每次 ~1-2MB）。
			light := state
			light.Screens = nil
			ClassroomChanged.Emit(w, light)
			continue
		}
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
		CastQuality:    l.quality(),
		CastTitle:     l.castTitleForUI(),
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

	state.FocusID = l.srv.FocusDeviceID()
	if f, ok := l.srv.FocusScreen(); ok {
		state.FocusScreen = &ScreenFrame{
			DeviceID: f.DeviceID, Label: f.Label, Name: f.Name,
			JPEG: base64.StdEncoding.EncodeToString(f.JPEG), At: f.At.UnixMilli(),
		}
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

// 广播画质档（仿极域四档画质）：分辨率上限 / JPEG 质量 / 采帧间隔。
// 帧画面没变化时跳过发送（见 capture.Fingerprinter），静止桌面流量趋近零。
var castQualityLevels = map[string]capture.Params{
	"perfect": {MaxWidth: 0, Quality: 82, IntervalMs: 350},    // 极好：原生分辨率
	"better":  {MaxWidth: 1920, Quality: 72, IntervalMs: 450}, // 较好
	"normal":  {MaxWidth: 1440, Quality: 58, IntervalMs: 600}, // 普通
	"low":     {MaxWidth: 960, Quality: 42, IntervalMs: 800},  // 低质（弱网/教学提醒用）
}

func (l *Lab) castTitleForUI() string {
	l.castMu.Lock()
	defer l.castMu.Unlock()
	return l.castTitle
}

func (l *Lab) quality() string {
	l.castMu.Lock()
	defer l.castMu.Unlock()
	if l.castQuality == "" {
		l.castQuality = "better"
	}
	return l.castQuality
}

// CastSetQuality 切换广播画质档；广播中途改也即时生效。
func (l *Lab) CastSetQuality(level string) {
	if _, ok := castQualityLevels[level]; !ok {
		return
	}
	l.castMu.Lock()
	l.castQuality = level
	l.castMu.Unlock()
	scheduleClassroomPush(l)
}

// CastStart 开始对所选学生机的屏幕广播：
// 组播会话（UDP，AES-GCM 帧）为主，组播失败的客户端自动走 TCP 兜底。
// 已广播时调用即切换模式（全屏⇄窗口）。
func (l *Lab) CastStart(mode string, ids []string) bool {
	l.classroomMu.Lock()
	defer l.classroomMu.Unlock()
	if mode != "window" && mode != "fullscreen" {
		return false
	}
	if demo := l.srv.DemoState(); demo.State == "pending" || demo.State == "active" {
		return false
	}
	online := l.srv.OnlineIDs(ids)
	if len(online) == 0 {
		return false
	}
	// 目标集合记全量选择（含离线）：离线的设备重连时会被补发广播。
	targets := lan.NormalizeIDs(ids)
	if len(targets) == 0 {
		targets = online
	}

	l.castMu.Lock()
	if l.castCancel != nil {
		l.castCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.castCancel = cancel
	l.castMu.Unlock()

	oldIDs, oldGen := l.srv.StopCast() // 结束旧会话（含组播发送端与兜底 worker）
	stale := make([]string, 0, len(oldIDs))
	for _, id := range oldIDs {
		if !contains(targets, id) {
			stale = append(stale, id)
		}
	}
	if len(stale) > 0 {
		l.srv.Broadcast(lan.Msg{Type: lan.MsgCommand, Cmd: &lan.CommandMsg{
			ID: fmt.Sprintf("cast-stop-%d", time.Now().UnixNano()),
			Op: lan.OpCastStop, Args: lan.CastStopArgs{Gen: oldGen},
		}}, stale)
	}

	params, err := lan.NewCastSession()
	if err != nil {
		params = lan.CastSessionParams{} // 密钥生成都失败了 → 纯 TCP
	}
	l.srv.StartCast(params, mode, targets)
	for _, id := range online {
		go func(id string) {
			args, ok := l.srv.CastArgs(id)
			if !ok {
				return // 会话已被停/换，不发陈旧参数
			}
			ack, err := l.srv.SendCommand(id, lan.OpCastStart, args)
			// "stale" 是客户端认出过期代数——不是失败，不该标 TCP 兜底。
			if ack.Detail == "tcp" || (ack.Detail != "stale" && (err != nil || !ack.OK)) {
				l.srv.MarkCastTCP(id, args.Gen)
			}
			// 广播期间被停/换代：补对应代数的 cast.stop 防僵尸全屏窗。
			if cur, still := l.srv.CastArgs(id); !still || cur.Gen != args.Gen {
				l.srv.Broadcast(lan.Msg{Type: lan.MsgCommand, Cmd: &lan.CommandMsg{
					ID: fmt.Sprintf("cast-stop-%d", time.Now().UnixNano()),
					Op: lan.OpCastStop, Args: lan.CastStopArgs{Gen: args.Gen},
				}}, []string{id})
			}
		}(id)
	}
	go l.castLoop(ctx, mode)
	l.openCastBar()
	if l.win != nil {
		l.win.Minimize() // 极域行为：广播时收起管理端主界面，顶部悬浮条接管
	}
	scheduleClassroomPush(l)
	return true
}

func (l *Lab) CastStop() {
	l.classroomMu.Lock()
	defer l.classroomMu.Unlock()
	l.castMu.Lock()
	cancel := l.castCancel
	l.castCancel = nil
	l.castMu.Unlock()
	if cancel != nil {
		cancel()
	}
	oldIDs, gen := l.srv.StopCast()
	if len(oldIDs) > 0 {
		l.srv.Broadcast(lan.Msg{Type: lan.MsgCommand, Cmd: &lan.CommandMsg{
			ID: fmt.Sprintf("cast-stop-%d", time.Now().UnixNano()),
			Op: lan.OpCastStop, Args: lan.CastStopArgs{Gen: gen},
		}}, oldIDs)
	}
	l.closeCastBar()
	if l.win != nil {
		l.win.Show() // 恢复开播时最小化的管理端
	}
	scheduleClassroomPush(l)
}

func (l *Lab) CastMode() string { return l.srv.CastMode() }

// CollectFiles 发起文件收取。dir 为空=学生手动选择提交（弹互动页）；
// dir 非空=按 glob 从学生机指定目录自动收取（学生无需操作）。
func (l *Lab) CollectFiles(ids []string, dir, glob string) int {
	session := "c-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	dir = strings.TrimSpace(dir)
	if len(dir) > 512 || len(glob) > 128 {
		return 0
	}
	args := lan.CollectOpenArgs{Session: session, Dir: dir, Glob: strings.TrimSpace(glob)}
	var sent atomic.Int32
	var wg sync.WaitGroup
	for _, id := range l.srv.OnlineIDs(ids) {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := l.srv.SendCommand(id, lan.OpCollectOpen, args); err == nil {
				sent.Add(1)
			}
		}(id)
	}
	wg.Wait()
	return int(sent.Load())
}

// FocusAdmin 唤起管理端主窗口（悬浮条的「显示管理端」按钮）。
func (l *Lab) FocusAdmin() {
	if l.win != nil {
		l.win.Show()
		l.win.Focus()
	}
}

// castLoop 定时抓屏 → 组播+TCP 兜底分发。画质档随 castQuality 即时生效；
// 画面静止时跳帧（心跳帧 4s 一保）。开播强制连发前 3 帧，让刚加入组播的
// 客户端尽快拿到画面（它们可能错过了第 1 帧的分片）。
func (l *Lab) castLoop(ctx context.Context, mode string) {
	if mode == "window" {
		// 钉住开播这一刻的前台窗：之后教师切换焦点不影响共享对象，
		// 标题也告诉教师在播什么（想换共享窗口需停广播重开）。
		l.castMu.Lock()
		l.castTitle = capture.PinWindow()
		l.castMu.Unlock()
		defer func() {
			capture.UnpinWindow()
			l.castMu.Lock()
			l.castTitle = ""
			l.castMu.Unlock()
			scheduleClassroomPush(l)
		}()
	}
	fp := &capture.Fingerprinter{}
	var lastSend time.Time
	var lastInterval time.Duration
	var sent int
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		p := castQualityLevels[l.quality()]
		if p.IntervalMs != int(lastInterval.Milliseconds()) {
			lastInterval = time.Duration(p.IntervalMs) * time.Millisecond
			ticker.Reset(lastInterval)
		}
		img, err := capture.Grab(mode)
		if err != nil {
			continue
		}
		if sent >= 3 && !fp.Changed(img) && time.Since(lastSend) < 4*time.Second {
			continue
		}
		jpeg := capture.Encode(img, p)
		if len(jpeg) == 0 || len(jpeg) > lan.MaxCastJPEG {
			continue
		}
		l.srv.CastFrame(jpeg)
		lastSend = time.Now()
		sent++
	}
}

// ---- 广播悬浮控制条（管理端，仿极域顶栏）----

func (l *Lab) openCastBar() {
	l.castMu.Lock()
	defer l.castMu.Unlock()
	if l.castBar != nil && !l.castBar.IsDestroyed() {
		l.castBar.Show()
		return
	}
	const bw, bh = 372, 46
	x, y := 0, 0
	if d := mygo.Screen.PrimaryDisplay(); d.WorkArea.Width > 0 {
		x = d.WorkArea.X + (d.WorkArea.Width-bw)/2
		y = d.WorkArea.Y + 6
	}
	l.castBar = mygo.NewWindow(mygo.WindowOptions{
		Title:       "屏幕广播",
		URL:         "/castbar.html",
		Width:       bw,
		Height:      bh,
		X:           x,
		Y:           y,
		Frameless:   true,
		Transparent: true,
		Panel:       true,
	})
	// Panel 窗默认会被 Esc 关掉——广播中必须一直挂着，只许 CastStop 销毁。
	l.castBar.OnClose(func(e *mygo.CloseEvent) { e.PreventDefault() })
}

func (l *Lab) closeCastBar() {
	l.castMu.Lock()
	bar := l.castBar
	l.castBar = nil
	l.castMu.Unlock()
	if bar != nil {
		bar.Destroy()
	}
}

func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
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
