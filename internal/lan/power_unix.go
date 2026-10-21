//go:build !windows

package lan

import "fmt"

// powerAction 非 Windows 平台暂不实现（agent 部署目标是学生 Windows 机）。
func powerAction(op string) error {
	return fmt.Errorf("unsupported platform")
}
