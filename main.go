package main

import (
	"context"
	_ "embed"
	"log"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/tinyviber/clover-classroom-manager/internal/labsvc"
)

// 三叶草托盘图标（tools/genicon 生成）。
//
//go:embed resources/tray.png
var trayIconPNG []byte

//go:embed resources/tray-template.png
var trayIconTemplatePNG []byte

// mygoHost 把 Lab 的平台副作用落到 mygo 桌面窗口上（labsvc.Host 的桌面实现）。
type mygoHost struct {
	win     *mygo.Window // 管理端窗口（对话框 parent）
	castBar *mygo.Window // 广播悬浮控制条（Panel 小窗）
}

func (h *mygoHost) MinimizeMain() {
	if h.win != nil {
		h.win.Minimize()
	}
}

func (h *mygoHost) ShowMain() {
	if h.win != nil {
		h.win.Show()
	}
}

func (h *mygoHost) FocusAdmin() {
	if h.win != nil {
		h.win.Show()
		h.win.Focus()
	}
}

func (h *mygoHost) OpenCastBar() {
	if h.castBar != nil && !h.castBar.IsDestroyed() {
		h.castBar.Show()
		return
	}
	const bw, bh = 372, 46
	x, y := 0, 0
	if d := mygo.Screen.PrimaryDisplay(); d.WorkArea.Width > 0 {
		x = d.WorkArea.X + (d.WorkArea.Width-bw)/2
		y = d.WorkArea.Y + 6
	}
	h.castBar = mygo.NewWindow(mygo.WindowOptions{
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
	h.castBar.OnClose(func(e *mygo.CloseEvent) { e.PreventDefault() })
}

func (h *mygoHost) CloseCastBar() {
	bar := h.castBar
	h.castBar = nil
	if bar != nil {
		bar.Destroy()
	}
}

func (h *mygoHost) PickFiles() []string {
	paths, _ := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent: h.win, Title: "选择要分发的文件", Multiple: true,
	})
	return paths
}

func (h *mygoHost) PickExportDir() string {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:            h.win,
		Title:             "选择客户端安装包输出目录",
		ButtonLabel:       "导出到这里",
		Directory:         true,
		CreateDirectories: true,
	})
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func (h *mygoHost) OpenPath(dir string) { mygo.Shell.OpenPath(dir) }

// DevicesChanged is pushed to every open window when device state changes.
var DevicesChanged = mygo.NewEvent[[]labsvc.Device]("devices")
var ClassroomChanged = mygo.NewEvent[labsvc.ClassroomState]("classroom")

func (h *mygoHost) EmitDevices(devs []labsvc.Device) {
	for _, w := range mygo.Windows() {
		DevicesChanged.Emit(w, devs)
	}
}

func (h *mygoHost) EmitClassroom(state labsvc.ClassroomState) {
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

func main() {
	// 数据目录：~/Library/Application Support/lab-assistant（macOS）/
	// %AppData%\lab-assistant（Windows）。身份密钥与设备注册表持久化于此。
	dataDir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		dataDir = "."
	}
	srv, err := labsvc.NewServer(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	demoAgents := labsvc.DemoAgentCount()
	srv.AutoApprove = demoAgents > 0 // 演示模式跳过 enrollment
	if len(srv.Devices()) == 0 && demoAgents > 0 {
		labsvc.SeedRegistry(srv) // 首启种子；之后以持久化注册表为准
	}
	host := &mygoHost{}
	lab := labsvc.New(srv, host, dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
	labsvc.SpawnDemoAgents(ctx, srv, demoAgents)

	mygo.Bind(lab)
	mygo.App.WhenReady(func() {
		host.win = mygo.NewWindow(mygo.WindowOptions{
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
		host.win.OnClose(func(e *mygo.CloseEvent) {
			e.PreventDefault()
			host.win.Hide()
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
