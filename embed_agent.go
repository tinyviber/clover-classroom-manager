//go:build embedagent

package main

import _ "embed"

// 发行构建把学生端 agent 编进管理端。教师只拿到管理端 exe 时，
// 「分发客户端」仍能导出带当前地址和公钥的完整安装包。
//
//go:embed packaging/windows/agent.exe
var embeddedAgent []byte
