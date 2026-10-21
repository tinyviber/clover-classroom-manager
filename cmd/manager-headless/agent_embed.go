//go:build embedagent

package main

import _ "embed"

// 发行构建把学生端 agent 编进 headless 管理端——单独下载这一个
// exe 也能导出完整安装包（与桌面管理端同样的自包含语义）。
// 构建前需把 agent.exe 拷到本目录（见 release workflow）。
//
//go:embed agent.exe
var embeddedAgent []byte
