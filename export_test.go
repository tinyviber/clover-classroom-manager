package main

import (
	"os"
	"path/filepath"
	"testing"

	"lab-assistant/internal/lan"
)

func TestWriteClientPackage(t *testing.T) {
	id, err := lan.GenIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := lan.NewServer(id, lan.TCPPort)
	dir := filepath.Join(t.TempDir(), "client-package")
	if err := writeClientPackage(dir, srv); err != nil {
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
}
