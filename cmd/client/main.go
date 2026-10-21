// 三叶草局域网课堂管理·客户端（学生机 daemon）。
//
// 用法（install.bat 做的事）：
//  1. 拷贝本 exe + client.json 到受还原保护的安装目录
//  2. 非还原态运行一次 → 自动生成 id/token 回写 client.json
//  3. 冻结 → 之后每次用户登录后，在交互会话后台运行（系统托盘图标，无窗口）
//
// client.json 形如：
//
//	{"adminAddr":"192.168.31.1:47823","adminPub":"<b64>","room":"301",
//	 "id":"pc-…","token":"…"}
//
// adminAddr/adminPub 均可空（空则纯发现 + TOFU），但建议管理端导出时写上。
// 课堂互动（签到/投票/举手/演示同意/提交文件）通过托盘菜单或气泡通知唤起的本机页面完成。
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/tinyviber/clover-classroom-manager/internal/castview"
	"github.com/tinyviber/clover-classroom-manager/internal/lan"
	"github.com/tinyviber/clover-classroom-manager/internal/studentui"
	"github.com/tinyviber/clover-classroom-manager/internal/trayicon"
)

func main() {
	cfgPath := flag.String("config", "", "client.json 路径（默认 exe 同目录）")
	printID := flag.Bool("id", false, "只打印本机 ID 后退出")
	castMode := flag.Bool("castview", false, "广播窗子进程模式（agent 内部拉起，勿手动运行）")
	flag.Parse()

	// 广播窗子进程入口：先于单实例锁与配置读取（无 client.json 也要能跑），
	// stderr 已由父进程接进 agent.log，panic/log 都留痕。
	if *castMode {
		os.Exit(castview.RunHost())
	}

	// 日志必须在任何 Fatalf 之前建立：windowsgui 下 stderr 无处去，
	// 配置坏了也要在 agent.log 里留痕。
	logFile := setupLog()
	captureStderr(logFile)     // runtime panic 不走 log 包，连 stderr 一起接管
	castview.ErrSink = logFile // viewer 子进程 stderr 进同一个文件

	// 单实例在身份生成之前：两个并发首启不能各写一份 client.json。
	if !singleInstance() {
		log.Printf("agent: 已有实例在运行，退出")
		return
	}

	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	if *cfgPath == "" {
		*cfgPath = filepath.Join(dir, "client.json")
	}

	cfg, err := lan.LoadAgentConfig(*cfgPath)
	if err != nil {
		log.Fatalf("读取配置失败: %v", err)
	}
	if cfg.EnsureIdentity() {
		if err := cfg.Save(*cfgPath); err != nil {
			log.Fatalf("客户端身份无法持久化，拒绝启动: %v", err)
		}
	}
	if *printID {
		fmt.Println(cfg.ID)
		return
	}

	a := cfg.ToAgent()
	userConfig, cfgErr := os.UserConfigDir()
	if cfgErr != nil || userConfig == "" {
		userConfig = dir
	}
	a.RecvDir = filepath.Join(userConfig, "LabHelper", "Received")
	lan.CleanIncompleteFiles(a.RecvDir)
	if a.PinnedPub == nil {
		log.Printf("agent[%s]: 未 pin 管理端公钥，使用 TOFU（信任第一个应答的信标）", cfg.ID)
		// 首个可信管理端握手成功后把公钥回写 client.json——
		// TOFU 只「首次」信任，之后重连重发现都认这把钥匙。
		a.PersistPub = func(pub ed25519.PublicKey) {
			cfg.AdminPub = base64.StdEncoding.EncodeToString(pub)
			if err := cfg.Save(*cfgPath); err != nil {
				log.Printf("agent[%s]: TOFU 公钥回写失败: %v", cfg.ID, err)
			}
		}
	}
	// 防护策略持久化：client.json 在安装目录里被 ACL 收成只读，
	// 可变状态必须写去用户可写的 state.json——否则 install.bat 装好的
	// 机器上策略重启就丢（且失败只进日志教师不可见）。
	statePath := filepath.Join(userConfig, "LabHelper", "state.json")
	if st := loadAgentState(statePath); st != nil {
		if st.HelperOn != nil {
			a.SetHelper(*st.HelperOn)
		}
		if st.Features != nil {
			a.SetFeatures(st.Features)
		}
	}
	a.PersistState = func(helperOn bool, features []string) {
		if err := saveAgentState(statePath, helperOn, features); err != nil {
			log.Printf("agent[%s]: 策略回写失败: %v", cfg.ID, err)
			a.ReportAlert("persist", "防护策略回写失败: "+err.Error())
		}
	}
	log.Printf("agent[%s]: 启动 name=%s label=%s mac=%s ip=%s admin=%s",
		cfg.ID, a.Name, a.Label, a.MAC, a.IP, a.DirectAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ui, _ := studentui.Start(ctx, a)
	// 学生端悬浮条「退出广播」：停掉组播接收（TCP 兜底帧进来也因
	// 窗口隐藏被解码层丢弃，几乎零 CPU）。
	castview.OnExit = func() { a.StopCast() }
	// viewer 子进程异常退出 → 上报管理端（子进程存活需求内会自动重拉，
	// 这里主要给教师留一条可见痕迹）。
	castview.OnChildDied = func(err error) {
		a.ReportAlert("castview", "广播窗进程异常退出: "+err.Error())
	}
	tray, _ := trayicon.New(trayicon.IconICO, "三叶草课堂助手")
	if tray != nil {
		defer tray.Close()
		tray.SetMenu([]trayicon.Item{
			{Label: "课堂互动…", OnClick: func() {
				if ui != nil {
					ui.Open()
				}
			}},
			{Label: "举手求助", OnClick: func() {
				if err := a.RaiseHand(); err == nil {
					tray.Notify("三叶草课堂助手", "已通知老师")
				}
			}},
			{Separator: true},
			{Label: "关于", OnClick: func() {
				tray.Notify("三叶草课堂助手", "Clover 课堂管理客户端 v"+a.Version)
			}},
		})
	}

	// 新的课堂活动 → 气泡提醒并唤起互动页（同一会话只唤一次）。
	seen := map[string]bool{}
	a.OnPrompt = func(kind, session string) {
		titles := map[string]string{
			"signin":  "课堂签到",
			"poll":    "课堂投票",
			"collect": "收取文件",
			"demo":    "学生演示请求",
		}
		title := titles[kind]
		if title == "" {
			return
		}
		if tray != nil {
			tray.Notify("三叶草课堂助手", "老师发起了「"+title+"」，点击托盘图标处理")
		}
		key := kind + ":" + session
		if ui != nil && !seen[key] {
			seen[key] = true
			ui.Open()
		}
	}
	// 教师消息 → 托盘气泡。
	a.OnNotify = func(text string) {
		if tray != nil {
			tray.Notify("教师消息", text)
		}
	}
	a.OnConn = func(connected bool) {
		if tray == nil {
			return
		}
		if connected {
			tray.SetTooltip("三叶草课堂助手 · 已连接管理端")
		} else {
			tray.SetTooltip("三叶草课堂助手 · 未连接")
		}
	}

	a.Run(ctx)
}

// agentState 是可变运行态（与只读 client.json 分开持久化）。
type agentState struct {
	HelperOn *bool    `json:"helperOn,omitempty"`
	Features []string `json:"features,omitempty"`
}

func loadAgentState(path string) *agentState {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st agentState
	if err := json.Unmarshal(b, &st); err != nil {
		log.Printf("agent: 解析 %s 失败（忽略）: %v", path, err)
		return nil
	}
	return &st
}

func saveAgentState(path string, helperOn bool, features []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(agentState{HelperOn: &helperOn, Features: features}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// setupLog 把日志写到用户可写的数据目录（安装目录 ACL 是只读的）。
// 返回打开的日志文件；失败时返回 nil（调用方按无日志继续跑）。
func setupLog() *os.File {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		return nil
	}
	dir := filepath.Join(base, "LabHelper")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	path := filepath.Join(dir, "agent.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 256<<10 {
		_ = os.Truncate(path, 0)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return f
}
