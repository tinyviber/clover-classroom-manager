//go:build windows

package lan

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// setDisableTaskMgr 写 HKCU 用户策略 DisableTaskMgr。
// HKCU Policies 键对当前登录用户可写，agent 以学生身份运行即可生效。
// 注意：学生自己也能改回来——真正防篡改要把 agent 做成 SYSTEM 服务
// 并加守护进程互守，见 AGENTS.md 部署清单。
func setDisableTaskMgr(on bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Policies\System`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetDWordValue("DisableTaskMgr", 1)
	}
	if err := k.DeleteValue("DisableTaskMgr"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
