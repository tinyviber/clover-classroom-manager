//go:build !windows

package trayicon

import "fmt"

// Tray 非 Windows 平台的空实现（demo agent 在 macOS/Linux 上跑时静默无托盘）。
type Tray struct{}

func Supported() bool { return false }

func New(ico []byte, tip string) (*Tray, error) { return &Tray{}, nil }

func (t *Tray) SetTooltip(tip string)     {}
func (t *Tray) SetMenu(items []Item)      {}
func (t *Tray) Notify(title, text string) {}
func (t *Tray) Close()                    {}

var _ = fmt.Sprintf // 保持 fmt 引用（占位）
