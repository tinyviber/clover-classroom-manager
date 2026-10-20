# 三叶草局域网课堂管理（重写版）

英文名：**Clover LAN Classroom Manager**

Go module：`github.com/tinyviber/clover-classroom-manager`。MyGo 的内部 app name 仍为
`lab-assistant`，用于沿用旧版数据目录并保留管理端密钥与设备注册表。

极域式机房管理工具的重写。架构：**管理端 + 客户端**（局域网），框架
[mygo](https://github.com/egoist/mygo)（Go 后端 + WebView 前端 + 生成的 TS 绑定），
界面使用 shadcn/ui Base Nova 组件、Lucide 图标与统一的 Geist 设计令牌，保留 magpie quiet surface 的克制层级。

## 常用命令

```sh
bun install            # 首次安装依赖（另需 Go 1.27+，本机 Go 1.26 经 GOTOOLCHAIN=auto 自动下载工具链）
bun run dev            # 开发：起 vite(5173) + 编译运行桌面应用，改 Go/前端均热更新
# LAB_DEMO_AGENTS=21 bun run dev  # 可选：本地显示 21 台协议演示客户端
bun run generate       # 改完 main.go 的绑定服务后重新生成 src/mygo.ts
bun run typecheck      # tsc --noEmit
bun run build          # 打包各平台安装包到 build/（mygo 无 cgo，可跨编译任意平台；
                       # 例：bunx mygo build -platform windows/amd64 → build/windows-amd64/lab-assistant.exe）
go test ./...          # 后端测试（lan 协议端到端 + 安装包导出）
mkdir -p build/windows-amd64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o build/windows-amd64/agent.exe ./cmd/client
                       # 交叉编译学生机 agent（导出安装包会带上它）
```

`.github/workflows/windows-release.yml` 在 `main`/`master` push 和 PR 时构建校验；推送 `v*` 标签时，
会发布管理端 EXE、含 agent 的管理端 ZIP、学生端 EXE、客户端安装模板 ZIP 和 SHA256 校验清单。
`build/` 与 `dist/` 均为本地产物目录，不提交到仓库。

## 结构

- `main.go` — `Lab` 服务（`mygo.Bind` 暴露给前端）：设备列表、功能目录、
  批量启停 `SetHelper`、单机勾选 `SetFeatures`、模板套用 `ApplyTemplate`、
  扫描 `Scan`、客户端状态 `Status`、密码退出 `Exit`、分发 `AdminInfo`/`ExportClientPackage`。
  全部经 `lan.Server.SendCommand` 下发到在线 agent；注册表变化经
  `srv.OnChange` → `DevicesChanged` 事件推给所有窗口。
  `spawnDemoAgents` 可在进程内跑 loopback agent；默认关闭，设置
  `LAB_DEMO_AGENTS=21` 显式开启 21 个演示客户端。
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
- `index.html` + `src/main.tsx` + `src/admin/` — React 管理端 UI：设备列表、课堂控制、屏幕墙与客户端分发。
- `client.html` + `src/client-entry.tsx` + `src/client/` — React 学生端状态窗。
- `src/components/ui/` — shadcn/ui Base Nova 组件；`src/index.css` — 共用主题与布局令牌。
- `src/api.ts` — MyGo 绑定与浏览器 mock 的单一 API 边界；`src/store.ts` — Zustand 跨视图状态。
- `src/mock.ts` — 无 MyGo runtime 时（浏览器调试）的假数据。

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
`agent.exe`（若已交叉编译）、`install.bat`、`uninstall.bat`、
`verify-config.ps1` 与 `README.txt`。管理端导出和 GitHub 发布包共用 `packaging/windows/client-package/` 模板。

学生机：**解冻状态下** 管理员运行 `install.bat` →
校验 `client.json` 中的管理端 Ed25519 公钥 → 拷到 `C:\LabHelper` →
写入登录启动项并收紧目录 ACL（Users 只读）→ 首启固化身份 →
用户登录后客户端运行并打开课堂交互页 → 管理端看到上线后创建还原点冻结。

教师机换 IP：agent 连不上 `adminAddr` 时回退 UDP 广播发现，公钥匹配即重连，
无需重装；换管理端密钥则需重新导出安装包。

## 正式部署时还要做

- 客户端 enrollment token 审批（新机器首次上线需管理端确认）
- 离线设备的指令队列（现在 SendCommand 对离线机直接报错，应改为上线补发）
- 客户端 Windows 化：SYSTEM 服务（install.bat 现为计划任务兜底）+
  守护进程互守 + DisableTaskMgr/IFEO + 禁蓝屏改全屏锁定
- 可选：管理端加一个 `net/http` 适配器把同一套 UI 挂到局域网浏览器
