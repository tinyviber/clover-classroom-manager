package trayicon

import _ "embed"

// IconICO 是三叶草托盘图标（16/24/32/48/64 PNG-in-ICO），由 tools/genicon 生成。
//
//go:embed clover.ico
var IconICO []byte
