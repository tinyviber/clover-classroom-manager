# 三叶草局域网课堂管理 · Clover LAN Classroom Manager

极域电子教室风格的局域网机房管理工具：**管理端 + 学生机客户端**。
管理端是 MyGo 桌面应用（Go 后端 + WebView 前端），客户端是 Windows 后台
托盘 agent，双方经加密 LAN 协议通信，可直接跨机部署。

## 功能

- **屏幕广播**（仿极域）：教师屏幕经 **UDP 组播**一次发送全班接收
  （AES-256-GCM 加密帧分片 + 每数据报 HMAC 认证）；组播不可用的客户端
  自动回落 TCP 单播，互不感知。全屏/窗口两种模式、四档画质、画面静止跳帧。
  广播时管理端与学生端顶部各有悬浮工具条（切换模式/画质/退出）。
- **课堂主页**：全部学生机实时预览宫格；点选 / Ctrl·Cmd 多选 / Shift 范围选 /
  Ctrl·Cmd+A 全选 / Esc 清空；顶部快捷条一键 广播/演示/分发文件/收取文件/
  签到/投票/屏幕墙/锁屏/远程电源。
- **文件分发/收取**：管理端选文件群发到学生机接收目录；一键向所选学生
  发起「提交文件」，学生在本机互动页上传即回传管理端 received/。
- **课堂互动**：限时签到、快速投票、举手求助（管理端弹出待办）、
  学生演示（学生同意后经教师端中继低帧率共享给其他学生）。
- **学生机静默运行**：agent.exe 编译为 GUI 子系统——无窗口、无终端，
  只有系统托盘三叶草图标；课堂事件走气泡通知，互动页按需唤起；
  广播以原生全屏/窗口画面呈现；全屏广播时低级钩子锁死学生键鼠
  （只放行悬浮工具条上的点击）、隐藏任务栏——和极域一样拦不住
  Ctrl+Alt+Del，需配合 DisableTaskMgr 类策略兜底。
- **纪律管控**：一键黑屏锁屏/解锁（学生端无退出途径）、远程重启/关机；
  已落地的策略——禁用任务管理器（HKCU 策略）、断线自动锁屏（防拔网线）；
  功能目录里未实现的项如实标「规划中」，不可勾选也不计入生效数。
- **设备准入**：新客户端首次注册一律进「待审批」，教师批准后才能
  接收指令、成为广播/分发目标——局域网里的机器没法冒充学生机混进来。
- **分发安装包**：管理端一键导出学生机安装包（client.json 已固定
  管理端公钥 + install.bat 自动部署 + README/卸载脚本）。

## 架构

```
管理端 (mygo 应用)                      学生机 (agent.exe)
┌──────────────┐   UDP 发现 :47822    ┌──────────────┐
│ React 课堂页  │  ────────────────▶  │ headless agent│
│ castbar 悬浮条│   加密 TCP :47823    │  ├ Win32 托盘  │
│ Go Lab 服务  │  ◀───────────────▶  │  ├ 互动页(按需)│
│ lan.Server   │  X25519+Ed25519+GCM │  └ castview 窗 │
└──────┬───────┘   UDP 组播 :47824   └──────▲───────┘
       └──────── 加密帧分片组播 ─────────────┘
```

- `internal/lan/` — 协议层：`crypto.go`（X25519 ECDH + Ed25519 签名握手 +
  HKDF 双向 AES-256-GCM）、`server.go`/`agent.go`（注册表/会话/指令）、
  `mcast.go`（广播组播通道）、`frames.go`（TCP 兜底）、`extras.go`（课堂
  业务状态与断线恢复）。
- `internal/capture/` 屏幕捕获与画质档；`internal/castview/` 学生机原生
  广播窗（Win32，含顶部悬浮条）；`internal/trayicon/` 无 cgo 系统托盘；
  `internal/studentui/` 学生本机互动页（loopback + token）。
- `main.go` 前端绑定服务 `Lab`；`src/admin/` 课堂宫格与设备管理；
  `src/castbar/` 管理端广播悬浮条页；`cmd/client/` 学生机入口。
- 详细约定见 [AGENTS.md](AGENTS.md)。

## 开发

```sh
bun install                        # 依赖（另需 Go 1.27+，GOTOOLCHAIN=auto 会自动拉工具链）
bun run dev                        # vite(5173) + 桌面应用热更新
# LAB_DEMO_AGENTS=21 bun run dev   # 本地跑 21 个协议演示客户端
bun run generate                   # 改过 main.go 绑定方法后重新生成 src/mygo.ts
bun run typecheck && go test ./... # 前端类型检查 + 后端协议端到端测试
bun run build                      # 打包管理端（可 -platform 跨编译）
```

## 部署（Windows 机房）

```sh
# 交叉编译学生机 agent（GUI 子系统，无控制台窗口）
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags="-s -w -H windowsgui" -o build/windows-amd64/agent.exe ./cmd/client
```

1. 教师机运行管理端 → 「分发客户端」导出安装包（自动写入管理端
   地址 + Ed25519 公钥 pin）。
2. 学生机**解冻状态**下管理员运行 `install.bat` → 校验公钥 →
   拷贝到 `C:\LabHelper` → 写登录启动项 + 收紧 ACL → 首启固化身份。
3. 登录后 agent 静默驻留托盘，管理端上线可见 → 冻结还原软件。
4. 教师机换 IP 无需重装：agent 连不上 `adminAddr` 自动回退
   UDP 广播发现，公钥匹配即重连。

GitHub Release 流程见 `.github/workflows/windows-release.yml`
（push `v*` 标签自动构建管理端 EXE/含 agent 包/校验清单）。

## 安全要点

- 控制通道全加密：X25519 临时 ECDH + 管理端 Ed25519 签名（客户端
  pin 公钥，空则 TOFU），HKDF 出双向 AES-256-GCM，seq 当 nonce 防重放。
- 广播组播帧：随机会话密钥经加密通道下发；帧整体 GCM 加密 +
  每数据报 HMAC 认证，伪造分片无法污染重组；TTL=1 不出子网。
- 学生互动页仅绑定 127.0.0.1 + 随机 token，不接受局域网访问。
- 客户端目录 ACL 收紧（Users 只读），身份 id/token 首启随机生成。
