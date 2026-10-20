# 机房小助手（重写版）

极域式机房管理工具的重写。架构：**管理端 + 客户端**（局域网），框架
[mygo](https://github.com/egoist/mygo)（Go 后端 + WebView 前端 + 生成的 TS 绑定），
皮肤为 magpie "quiet surface" 设计系统。

## 常用命令

```sh
bun install            # 首次安装依赖（另需 Go 1.27+，本机 Go 1.26 经 GOTOOLCHAIN=auto 自动下载工具链）
bun run dev            # 开发：起 vite(5173) + 编译运行桌面应用，改 Go/前端均热更新
bun run generate       # 改完 main.go 的绑定服务后重新生成 src/mygo.ts
bun run typecheck      # tsc --noEmit
bun run build          # 打包各平台安装包到 build/（mygo 无 cgo，可跨编译任意平台；
                       # 例：bunx mygo build -platform windows/amd64 → build/windows-amd64/lab-assistant.exe）
go test ./...          # 后端测试（lan 协议端到端 + 安装包导出）
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o agent.exe ./cmd/client
                       # 交叉编译学生机 agent（导出安装包会带上它）
```

`release/windows-amd64/` 是面向机房的分发产物：管理端 exe +
`client-package/`（agent.exe + client.json + install/uninstall.bat + README），
使用说明见 `release/windows-amd64/使用说明.md`。

## 结构

- `main.go` — `Lab` 服务（`mygo.Bind` 暴露给前端）：设备列表、功能目录、
  批量启停 `SetHelper`、单机勾选 `SetFeatures`、模板套用 `ApplyTemplate`、
  扫描 `Scan`、客户端状态 `Status`、密码退出 `Exit`、分发 `AdminInfo`/`ExportClientPackage`。
  全部经 `lan.Server.SendCommand` 下发到在线 agent；注册表变化经
  `srv.OnChange` → `DevicesChanged` 事件推给所有窗口。
  `spawnDemoAgents` 在进程内跑 21 个 loopback agent（`LAB_DEMO_AGENTS=0` 关闭）。
- `internal/lan/` — 局域网协议层（真实实现，可直接跨机部署）：
  - `crypto.go` X25519 临时 ECDH + 管理端 Ed25519 签名握手 + HKDF-SHA256
    导出双向 AES-256-GCM 会话密钥；帧 = u32 长度前缀 + AEAD，seq 当 nonce 防重放；
    `Identity` 持久化为 admin.key（Ed25519 私钥文件）
  - `proto.go` 消息信封（hello/status/command/ack/alert）与 UDP 发现信标格式
  - `server.go` 管理端：UDP :47822 应答发现、TCP :47823 接客户端、
    设备注册表持久化（registry.json，1s 防抖原子写）、`SendCommand` 等 Ack
  - `agent.go` 学生机 agent：UDP 广播发现（或直接 `AdminAddr`）→ 握手
    （pin 管理端公钥，空则 TOFU）→ hello → 3s 心跳 + 指令执行
  - `config.go` `AgentConfig`（client.json：adminAddr/adminPub/token/id/room）
    与持久化辅助；`netinfo.go` 本机 IPv4 枚举
  - `sockopt_{unix,windows}.go` 平台 socket 选项（Windows 的 SetsockoptInt 签名不同）
- `cmd/client/main.go` — 学生机无头 agent 入口：读 client.json（相对 exe，
  找不到则回落 exe 旁→工作目录），`-id` 只生成身份后退出，供 install.bat 首启固化。
- `index.html` + `src/main.ts` — 管理端 UI：KPI 条、筛选/搜索、设备表
  （行展开、勾选批量条、单机配置弹窗、分发客户端弹窗）。
- `client.html` + `src/client.ts` — 客户端状态窗。
- `src/mock.ts` — 无 MyGo runtime 时（浏览器调试）的假数据。
- `src/tokens.css` `src/base.css` — magpie 设计系统，样式只用 token。

## 数据目录（持久化）

`mygo.App.Path(PathUserData)`：macOS `~/Library/Application Support/lab-assistant[ Dev]`，
Windows `%AppData%\lab-assistant`。
- `admin.key` — 管理端 Ed25519 身份，首启生成后复用（客户端 pin 的就是它）
- `registry.json` — 设备注册表，1s 防抖写盘；重启后设备保留且标离线
- `client.json`（学生机侧）— agent.exe 同目录，installer 部署；含
  adminAddr/adminPub/token/id/room，agent 首启 `-id` 固化 id+token

## 分发流程（机房还原软件）

管理端「分发客户端…」→ 显示本机 LAN IP:47823 + 公钥指纹 →
「导出安装包」生成 `client-package/`：`client.json`（已写管理端地址+公钥）、
`agent.exe`（若已交叉编译）、`install.bat`、`README.txt`。

学生机：**解冻状态下** 管理员运行 `install.bat` →
拷到 `C:\LabHelper` → `schtasks` 注册 SYSTEM 开机自启（先于登录）→
icacls 收紧目录 ACL（Users 只读）→ 首启固化身份并启动 →
管理端看到上线后创建还原点冻结。

教师机换 IP：agent 连不上 `adminAddr` 时回退 UDP 广播发现，公钥匹配即重连，
无需重装；换管理端密钥则需重新导出安装包。

## 正式部署时还要做

- 客户端 enrollment token 审批（新机器首次上线需管理端确认）
- 离线设备的指令队列（现在 SendCommand 对离线机直接报错，应改为上线补发）
- 客户端 Windows 化：SYSTEM 服务（install.bat 现为计划任务兜底）+
  守护进程互守 + DisableTaskMgr/IFEO + 禁蓝屏改全屏锁定
- 可选：管理端加一个 `net/http` 适配器把同一套 UI 挂到局域网浏览器
