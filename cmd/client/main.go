// 机房小助手·客户端（学生机 daemon）。
//
// 用法（install.bat 做的事）：
//   1. 拷贝本 exe + client.json 到受还原保护的安装目录
//   2. 非还原态运行一次 → 自动生成 id/token 回写 client.json
//   3. 冻结 → 之后每次开机按配置连管理端；连不上走 UDP 发现兜底
//
// client.json 形如：
//   {"adminAddr":"192.168.31.1:47823","adminPub":"<b64>","room":"301",
//    "id":"pc-…","token":"…"}
// adminAddr/adminPub 均可空（空则纯发现 + TOFU），但建议管理端导出时写上。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"lab-assistant/internal/lan"
)

func main() {
	cfgPath := flag.String("config", "", "client.json 路径（默认 exe 同目录）")
	printID := flag.Bool("id", false, "只打印本机 ID 后退出")
	flag.Parse()

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
			log.Printf("警告: 回写配置失败（id/token 未固化）: %v", err)
		}
	}
	if *printID {
		fmt.Println(cfg.ID)
		return
	}

	a := cfg.ToAgent()
	if a.PinnedPub == nil {
		log.Printf("agent[%s]: 未 pin 管理端公钥，使用 TOFU（信任第一个应答的信标）", cfg.ID)
	}
	log.Printf("agent[%s]: 启动 name=%s label=%s mac=%s ip=%s admin=%s",
		cfg.ID, a.Name, a.Label, a.MAC, a.IP, a.DirectAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.Run(ctx)
}
