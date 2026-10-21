//go:build windows

package lan

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func TestDisableTaskMgrRoundTrip(t *testing.T) {
	const keyPath = `Software\Microsoft\Windows\CurrentVersion\Policies\System`
	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()

	old, _, errOld := k.GetIntegerValue("DisableTaskMgr")
	had := errOld == nil
	if errOld != nil && !errors.Is(errOld, registry.ErrNotExist) {
		t.Fatal(errOld)
	}
	t.Cleanup(func() {
		if had {
			_ = k.SetDWordValue("DisableTaskMgr", uint32(old))
			return
		}
		_ = k.DeleteValue("DisableTaskMgr")
	})

	if err := setDisableTaskMgr(true); err != nil {
		t.Fatal(err)
	}
	got, _, err := k.GetIntegerValue("DisableTaskMgr")
	if err != nil || got != 1 {
		t.Fatalf("DisableTaskMgr=%d err=%v", got, err)
	}
	if err := setDisableTaskMgr(false); err != nil {
		t.Fatal(err)
	}
	_, _, err = k.GetIntegerValue("DisableTaskMgr")
	if !errors.Is(err, registry.ErrNotExist) {
		t.Fatalf("关闭后 DisableTaskMgr 仍在: %v", err)
	}
}

func TestValidLaunchPathWindows(t *testing.T) {
	notepad := `C:\Windows\System32\notepad.exe`
	if _, err := os.Stat(notepad); err != nil {
		t.Skipf("没有 %s: %v", notepad, err)
	}
	if err := validLaunchPath(notepad); err != nil {
		t.Fatal(err)
	}
	for _, blocked := range []string{
		`C:\Windows\System32\cmd.exe`,
		`C:\Windows\System32\CMD.EXE`,
		`C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
	} {
		if _, err := os.Stat(blocked); err != nil {
			continue
		}
		if err := validLaunchPath(blocked); err == nil {
			t.Fatalf("%s 应被拒绝", blocked)
		}
	}
}
