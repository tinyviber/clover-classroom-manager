//go:build windows

package lan

import (
	"fmt"
	"os/exec"
)

// powerAction 真正执行关机/重启。
// 注意 Windows 的 shutdown /t >0 会【隐含 /f】强关应用——所以「非强制」
// 语义必须是我们自己的 60s 可取消倒计时（schedulePower）到期后执行
// /t 0 且不带 /f：应用收到关机通知、可以拒绝并显示「阻止关机」屏，
// 学生未保存的工作不会静默丢失。force=true 才是 5s 强关。
func powerAction(op string, force bool) error {
	flag := "/r"
	if op == OpShutdown {
		flag = "/s"
	}
	args := []string{flag, "/t", "0", "/c", "三叶草课堂管理：教师发起远程关机/重启"}
	if force {
		args = []string{flag, "/f", "/t", "5", "/c", "三叶草课堂管理：强制远程关机/重启"}
	}
	if err := exec.Command("shutdown.exe", args...).Start(); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// powerCancel 取消系统级关机倒计时（shutdown /a）——兜底清理可能残留的
// force 关机；agent 自己的普通倒计时由 cancelScheduledPower 单独管理。
// Run 等待完成以拿到退出码：没有可取消的关机时 shutdown /a 返回非零。
func powerCancel() error {
	if err := exec.Command("shutdown.exe", "/a").Run(); err != nil {
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
