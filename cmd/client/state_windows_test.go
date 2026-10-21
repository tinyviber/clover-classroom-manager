//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// 安装目录被 ACL 收成当前用户不可写时，state.json 必须保存失败。
// 显式 DENY 优先于管理员令牌上的允许项；备份特权默认是禁用的，
// CreateFile 不会绕过这条拒绝。
func TestAgentStateACLDeniesWrite(t *testing.T) {
	dir := t.TempDir()
	user := os.Getenv("USERNAME")
	if user == "" {
		t.Fatal("USERNAME 为空")
	}
	out, err := exec.Command("icacls", dir, "/deny", user+":(W)", "/Q").CombinedOutput()
	if err != nil {
		t.Fatalf("icacls 拒绝写失败: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		out, err := exec.Command("icacls", dir, "/remove:d", user, "/Q").CombinedOutput()
		if err != nil {
			t.Errorf("恢复 ACL 失败: %v\n%s", err, out)
		}
	})
	if err := saveAgentState(filepath.Join(dir, "state.json"), true, nil); err == nil {
		t.Fatal("拒绝写的目录上保存竟成功")
	}
}
