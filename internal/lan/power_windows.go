//go:build windows

package lan

import (
	"fmt"
	"os/exec"
)

// powerAction 远程重启/关机学生机。默认非强制 + 60s 倒计时
// （应用可拒绝、学生可 shutdown /a 取消、管理端可发 power.cancel），
// force=true 才是 5s 强关——强制是有数据丢失风险的显式操作。
func powerAction(op string, force bool) error {
	flag := "/r"
	if op == OpShutdown {
		flag = "/s"
	}
	args := []string{flag, "/t", "60", "/c", "三叶草课堂管理：教师发起远程关机/重启"}
	if force {
		args = []string{flag, "/f", "/t", "5", "/c", "三叶草课堂管理：强制远程关机/重启"}
	}
	if err := exec.Command("shutdown.exe", args...).Start(); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// powerCancel 取消进行中的关机/重启倒计时（shutdown /a）。
func powerCancel() error {
	if err := exec.Command("shutdown.exe", "/a").Start(); err != nil {
		return fmt.Errorf("shutdown /a: %w", err)
	}
	return nil
}

// openURL 用默认浏览器打开教学网址（FileProtocolHandler 不经 shell 解析）。
func openURL(url string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", url).Start()
}

// launchApp 启动教师指定的 .exe（直接 exec，不走 shell，参数不可注入）。
func launchApp(path string) error {
	return exec.Command(path).Start()
}
