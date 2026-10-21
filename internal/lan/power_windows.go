//go:build windows

package lan

import (
	"fmt"
	"os/exec"
)

// powerAction 远程重启/关机学生机。延迟 5s 让 ack 先回到管理端；
// /f 强关挡路程序（课堂管控下没有需要保留的学生未保存工作）。
func powerAction(op string) error {
	flag := "/r"
	if op == OpShutdown {
		flag = "/s"
	}
	cmd := exec.Command("shutdown.exe", flag, "/f", "/t", "5",
		"/c", "三叶草课堂管理：远程关机指令")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
