// Package trayicon 给学生机 agent 提供 Windows 系统托盘图标：
// 隐藏消息窗 + Shell_NotifyIcon + 右键菜单 + 气泡通知，纯 syscall 无 cgo。
// 非 Windows 平台为空实现（demo 客户端在 macOS 上开发时不受影响）。
package trayicon

// Item 托盘右键菜单项。
type Item struct {
	Label     string
	OnClick   func()
	Separator bool // 为 true 时忽略其余字段，渲染分隔线
	Disabled  bool
	Checked   bool
}
