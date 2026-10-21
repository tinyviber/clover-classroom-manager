//go:build !windows

package lan

// setDisableTaskMgr 非 Windows 平台无此策略（agent 目标是学生 Windows 机）。
func setDisableTaskMgr(on bool) error { return nil }
