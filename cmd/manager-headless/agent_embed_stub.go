//go:build !embedagent

package main

// 开发与测试默认不嵌入 agent。导出时改为查找管理端旁边的 agent.exe。
var embeddedAgent []byte
