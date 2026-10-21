package labsvc

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

// Client package scripts are shared by in-app export and GitHub release bundles.
// （原 packaging/windows/client-package/，迁入本包以脱离 package main 也能 embed。）
//
//go:embed clientpkg/install.bat clientpkg/uninstall.bat clientpkg/verify-config.ps1 clientpkg/README.txt
var clientPackageAssets embed.FS

// EmbeddedAgent 发行构建嵌入的 agent.exe（桌面端 -tags=embedagent 注入）；
// nil/空时回退文件系统候选路径——headless 版 agent.exe 就放在管理端旁边。
var EmbeddedAgent []byte

// WriteClientPackage 生成安装包；addr 是教师选定的管理端 LAN 地址
// （空串退回第一张网卡——多网卡/VPN 机器上默认项未必是学生可达的）。
func WriteClientPackage(dir string, srv *lan.Server, addr string) error {
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
	// agent.exe：发行版嵌在管理端里；否则按候选路径找（管理端旁边、
	// 编译产物、packaging 目录）。
	agentNote := "agent.exe 未随包导出（未发现已构建的客户端，请先：mkdir -p packaging/windows && GOTOOLCHAIN=go1.20.14 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -modfile=agent.go.mod -o packaging/windows/agent.exe ./cmd/client）"
	if len(EmbeddedAgent) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "agent.exe"), EmbeddedAgent, 0o755); err != nil {
			return fmt.Errorf("写入客户端程序: %w", err)
		}
		agentNote = "agent.exe 已随包导出"
	} else {
		agentSrc := ""
		candidates := []string{"./packaging/windows/agent.exe", "./build/windows-amd64/agent.exe", "./cmd/client/agent.exe", "./agent.exe"}
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
	}
	for _, name := range []string{"install.bat", "uninstall.bat", "verify-config.ps1"} {
		if err := writeClientPackageAsset(dir, name); err != nil {
			return err
		}
	}
	readme, err := clientPackageAssets.ReadFile("clientpkg/README.txt")
	if err != nil {
		return err
	}
	readmeText := strings.ReplaceAll(string(readme), "@VERSION@", "随当前管理端版本")
	readmeText = strings.ReplaceAll(readmeText, "@AGENT_STATUS@", agentNote)
	return os.WriteFile(filepath.Join(dir, "README.txt"), []byte(readmeText), 0o644)
}

func writeClientPackageAsset(dir, name string) error {
	data, err := clientPackageAssets.ReadFile("clientpkg/" + name)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), data, 0o644)
}
