package labsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

func TestWriteClientPackage(t *testing.T) {
	old := EmbeddedAgent
	EmbeddedAgent = []byte("MZ-agent")
	t.Cleanup(func() { EmbeddedAgent = old })

	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	dir := filepath.Join(t.TempDir(), "client-package")
	if err := WriteClientPackage(dir, srv, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client.json", "install.bat", "README.txt", "agent.exe"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	cfg, err := lan.LoadAgentConfig(filepath.Join(dir, "client.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPub != id.PubB64() {
		t.Fatal("admin pub key not written into client.json")
	}
	if cfg.AdminAddr == "" {
		t.Fatal("admin addr empty")
	}
}

// 没有嵌入、旁边也没有 agent.exe 时必须报错——不能生成看似成功
// 实则缺学生端的安装包（评审要求）。
func TestWriteClientPackageNoAgentFails(t *testing.T) {
	old := EmbeddedAgent
	EmbeddedAgent = nil
	t.Cleanup(func() { EmbeddedAgent = old })
	// 工作目录切到临时目录，确保候选路径里也没有 agent.exe
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	dir := filepath.Join(t.TempDir(), "client-package")
	err = WriteClientPackage(dir, srv, "")
	if err == nil || !strings.Contains(err.Error(), "agent.exe") {
		t.Fatalf("缺 agent.exe 时应报错: %v", err)
	}
}

func TestWriteClientPackageEmbeddedAgent(t *testing.T) {
	old := EmbeddedAgent
	EmbeddedAgent = []byte("MZ-embedded-agent")
	t.Cleanup(func() { EmbeddedAgent = old })

	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	dir := filepath.Join(t.TempDir(), "client-package")
	if err := WriteClientPackage(dir, srv, "192.168.31.8"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "agent.exe"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "MZ-embedded-agent" {
		t.Fatalf("导出的 agent 不是嵌入的那一份: %q", got)
	}
	cfg, err := lan.LoadAgentConfig(filepath.Join(dir, "client.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminAddr != "192.168.31.8:47823" || cfg.AdminPub != id.PubB64() {
		t.Fatalf("client.json = %+v", cfg)
	}
}
