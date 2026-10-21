// manager-headless 是无窗口的管理端：同一个 labsvc.Lab + 同一套前端 UI，
// 但用 net/http 挂到浏览器而不是 mygo WebView。
//
// 存在的理由：管理端 GUI 依赖 mygo（要求 Go 1.27）与 WebView2，二者都只能在
// Windows 10+ 工作。本二进制用 go1.20 工具链 + agent.go.mod 构建，PE 最低
// 版本 6.1——可在 Windows 7 教师机上运行，教师用浏览器打开管理界面。
//
// 数据目录与桌面管理端一致（%AppData%\lab-assistant）：admin.key、registry、
// seats、received 两者共用，换用形态不丢设备与身份。
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/tinyviber/clover-classroom-manager/internal/labsvc"
	"github.com/tinyviber/clover-classroom-manager/internal/lan"
	"github.com/tinyviber/clover-classroom-manager/internal/lanhttp"
)

// 前端产物由构建脚本拷入本目录（bun run build:web → cp -r dist/. cmd/manager-headless/dist/）。
//
//go:embed all:dist
var distFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:47825", "HTTP 监听地址（默认仅回环；绑 0.0.0.0 时 LAN 访问需带 ?k 凭据，URL 见启动日志）")
	noBrowser := flag.Bool("no-browser", false, "启动后不自动打开浏览器")
	flag.Parse()

	dataDir := "."
	if cfg, err := os.UserConfigDir(); err == nil {
		dataDir = filepath.Join(cfg, "lab-assistant")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		log.Fatalf("数据目录不可用: %v", err)
	}
	srv, err := labsvc.NewServer(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	demoAgents := labsvc.DemoAgentCount()
	srv.AutoApprove = demoAgents > 0
	if len(srv.Devices()) == 0 && demoAgents > 0 {
		labsvc.SeedRegistry(srv)
	}

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("监听 %s 失败（已有管理端在运行？）: %v", *addr, err)
	}
	origin := fmt.Sprintf("http://%s", listener.Addr())

	static, err := fs.Sub(distFS, "dist")
	if err != nil {
		log.Fatal(err)
	}
	hs, err := lanhttp.New(static, filepath.Join(dataDir, "uploads"), origin)
	if err != nil {
		log.Fatal(err)
	}
	lab := labsvc.New(srv, hs, dataDir)
	// 发行构建（-tags=embedagent）把 agent.exe 编进二进制——单独下载
	// headless exe 时导出安装包也不缺学生端
	labsvc.EmbeddedAgent = embeddedAgent
	hs.Bind(lab)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
	labsvc.SpawnDemoAgents(ctx, srv, demoAgents)

	httpServer := &http.Server{Handler: hs.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("三叶草课堂管理（headless）→ %s", origin)
	// 绑定到局域网时页面不再自动发 token：LAN 访问须走带凭据的 URL，
	// 只打印在管理端控制台（本机浏览器仍自动注入）。
	if host, port, err := net.SplitHostPort(listener.Addr().String()); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
			for _, lip := range lan.LocalIPv4s() {
				log.Printf("局域网管理地址（含凭据，勿外传）: http://%s/?k=%s", net.JoinHostPort(lip, port), hs.Token())
			}
		} else if ip != nil && !ip.IsLoopback() {
			log.Printf("局域网管理地址（含凭据，勿外传）: %s/?k=%s", origin, hs.Token())
		}
	}
	if !*noBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := lanhttp.OpenBrowser(origin); err != nil {
				log.Printf("无法自动打开浏览器，请手动访问 %s", origin)
			}
		}()
	}
	if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
