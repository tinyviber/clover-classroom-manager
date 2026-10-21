//go:build windows

package lan

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeJunction(link, target string) error {
	// mklink 是 cmd 内建命令。/J 建目录联接，普通用户对自己的目录就能建。
	// 参数必须分开传：把整段带引号的命令交给 cmd /c 时，cmd 会剥掉首尾引号，
	// 路径被改坏，报 “filename syntax is incorrect”。
	out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mklink /J: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func TestCollectWindowsJunctionAndSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "hw")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "a.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	old := AllowedCollectRoots
	AllowedCollectRoots = []string{resolvedRoot, root}
	t.Cleanup(func() { AllowedCollectRoots = old })

	// \\?\ 前缀是 GetFinalPathName 的常见形状，必须仍算在白名单内。
	if !collectPathInRoots(`\\?\` + resolvedRoot) {
		t.Fatalf("extended path rejected: %s", resolvedRoot)
	}
	sibling := filepath.Join(filepath.Dir(resolvedRoot), filepath.Base(resolvedRoot)+"X")
	if collectPathInRoots(sibling) {
		t.Fatalf("sibling path accepted: %s", sibling)
	}

	// 白名单内的联接指向白名单内：允许。
	innerLink := filepath.Join(root, "inner")
	if err := makeJunction(innerLink, inside); err != nil {
		t.Fatalf("建内部 junction 失败: %v", err)
	}
	if !collectDirAllowed(innerLink) {
		got, _ := filepath.EvalSymlinks(innerLink)
		t.Fatalf("白名单内 junction 被拒: %s -> %s", innerLink, got)
	}

	// 白名单内的联接指向外面：拒绝。这是学生把作业目录指到任意盘的路径。
	escape := filepath.Join(root, "escape")
	if err := makeJunction(escape, outside); err != nil {
		t.Fatalf("建逃逸 junction 失败: %v", err)
	}
	if collectDirAllowed(escape) {
		got, err := filepath.EvalSymlinks(escape)
		t.Fatalf("junction 逃逸未被拒: %s -> %s (%v)", escape, got, err)
	}

	a := NewAgent("pc-win", "PC-WIN")
	a.collectDir(nil, CollectOpenArgs{Dir: escape, Glob: "*"})
	a.pendMu.Lock()
	var stat, detail string
	if len(a.pendAlerts) > 0 {
		last := a.pendAlerts[len(a.pendAlerts)-1]
		stat, detail = last.Stat, last.Detail
	}
	a.pendAlerts = nil
	a.pendMu.Unlock()
	if stat != "denied" {
		t.Fatalf("收取 junction 逃逸目录: stat=%q %q", stat, detail)
	}

	// 文件符号链接需要管理员或开发人员模式。权限不够只跳过这一段，
	// 上面的 junction 断言已经跑完，不能把整个测试标成 Skip。
	t.Run("file symlink", func(t *testing.T) {
		linkFile := filepath.Join(inside, "linked.txt")
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), linkFile); err != nil {
			t.Skipf("无法创建文件符号链接（需要管理员或开发人员模式）: %v", err)
		}
		if collectFileAllowed(linkFile) {
			got, _ := filepath.EvalSymlinks(linkFile)
			t.Fatalf("文件符号链接逃逸未被拒: %s -> %s", linkFile, got)
		}
		if !collectFileAllowed(filepath.Join(inside, "a.txt")) {
			t.Fatal("白名单内的普通文件被拒")
		}
		a.collectDir(nil, CollectOpenArgs{Dir: inside, Glob: "*"})
		a.pendMu.Lock()
		defer a.pendMu.Unlock()
		if len(a.pendAlerts) == 0 {
			t.Fatal("合法目录收取没有回执")
		}
		last := a.pendAlerts[len(a.pendAlerts)-1]
		if last.Stat != "fail" || !strings.Contains(last.Detail, "1 个文件") {
			t.Fatalf("符号链接文件被计入尝试或结果不对: stat=%q %q", last.Stat, last.Detail)
		}
	})
}
