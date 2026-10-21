package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tinyviber/clover-classroom-manager/internal/lan"
)

func TestWriteClientPackage(t *testing.T) {
	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	dir := filepath.Join(t.TempDir(), "client-package")
	if err := writeClientPackage(dir, srv, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"client.json", "install.bat", "README.txt"} {
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
	if _, err := os.Stat(filepath.Join(dir, "agent.exe")); err == nil {
		t.Fatal("没有嵌入、旁边也没有 agent.exe 时不应导出客户端程序")
	}
}

func TestWriteClientPackageEmbeddedAgent(t *testing.T) {
	old := embeddedAgent
	embeddedAgent = []byte("MZ-embedded-agent")
	t.Cleanup(func() { embeddedAgent = old })

	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	dir := filepath.Join(t.TempDir(), "client-package")
	if err := writeClientPackage(dir, srv, "192.168.31.8"); err != nil {
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
