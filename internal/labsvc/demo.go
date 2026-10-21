package labsvc

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/tinyviber/clover-classroom-manager/internal/castview"
	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

// SeedRegistry 首启种子：之后以持久化注册表为准。
func SeedRegistry(srv *lan.Server) {
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

// DemoAgentCount 读 LAB_DEMO_AGENTS；>0 时管理端跑演示模式（跳过 enrollment）。
func DemoAgentCount() int {
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

// SpawnDemoAgents 在进程内跑一批 loopback agent，让管理端吃到真实协议数据。
func SpawnDemoAgents(ctx context.Context, srv *lan.Server, n int) {
	if n <= 0 {
		return
	}
	// 演示 agent 与管理端同进程：广播窗子进程化会拿管理端 exe 去起
	// -castview，必须禁掉（真实 agent.exe 不受影响）。
	castview.Disabled.Store(true)
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
