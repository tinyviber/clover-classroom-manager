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
GOTOOLCHAIN=go1.21.13 GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -modfile=agent.go.mod -trimpath -ldflags="-s -w -H windowsgui" -o build/windows-amd64/agent.exe ./cmd/client
                       # 交叉编译学生机 agent：go1.21 是最后支持 Win7 的 Go，配 agent.go.mod
                       # 的降版依赖，产物 PE 最低版本 6.1（Win7+ 通吃；管理端仍需 Win10+）。
                       # -H windowsgui 无控制台窗口；导出安装包会带上它
```

`.github/workflows/windows-release.yml` 在 `main`/`master` push 和 PR 时构建校验；推送 `v*` 标签时，
会发布管理端 EXE、含 agent 的管理端 ZIP、学生端 EXE、客户端安装模板 ZIP 和 SHA256 校验清单。
`build/` 与 `dist/` 均为本地产物目录，不提交到仓库。

## 结构

- `main.go` — `Lab` 服务（`mygo.Bind` 暴露给前端）：设备列表、功能目录、
  批量启停 `SetHelper`、单机勾选 `SetFeatures`、模板套用 `ApplyTemplate`、
  扫描 `Scan`、课堂快照 `ClassroomState`、屏幕墙 `WatchScreens`、广播
  `CastStart`/`CastStop`/`CastSetQuality`、签到 `SignInStart/Stop`、
  分发 `SendFiles`/`PickFiles`/`OpenRecvDir`、收文件 `CollectFiles`、
  投票 `PollStart/Stop`、求助 `ClearHelp`、学生演示 `DemoStart/Stop`、
  分发 `AdminInfo`/`ExportClientPackage`、`FocusAdmin`（悬浮条唤起主窗）。
  广播开始时主窗最小化并弹出 `castbar.html` 悬浮条（Panel 无边框小窗），
  管理端常驻系统托盘（三叶草图标）。全部经 `lan.Server.SendCommand`
  下发到在线 agent；注册表变化经 `srv.OnChange` → `DevicesChanged` /
  `ClassroomChanged` 事件推给所有窗口。
  `spawnDemoAgents` 可在进程内跑 loopback agent；默认关闭，设置
  `LAB_DEMO_AGENTS=21` 显式开启 21 个演示客户端。
- `internal/lan/` — 局域网协议层（真实实现，可直接跨机部署）：
  - `crypto.go` X25519 临时 ECDH + 管理端 Ed25519 签名握手 + HKDF-SHA256
    导出双向 AES-256-GCM 会话密钥；帧 = u32 长度前缀 + AEAD，seq 当 nonce 防重放；
    `Identity` 持久化为 admin.key（Ed25519 私钥文件）
  - `proto.go` 消息信封（hello/status/command/ack/alert）与 UDP 发现信标格式
  - `server.go` 管理端：UDP :47822 应答发现、TCP :47823 接客户端、
    设备注册表持久化（registry.json，1s 防抖原子写）、`SendCommand` 等 Ack；
    新设备一律注册 pending（enrollment），教师 `Approve` 后才有课堂资格
    ——下行指令与上行数据（屏幕帧/文件/求助/告警）双向拦截，只放行
    status 心跳（演示模式 `Server.AutoApprove` 跳过）；握手+hello 阶段
    各有 10s deadline 且未认证连接数上限 64，防局域网资源耗尽
  - `agent.go` 学生机 agent：UDP 广播发现（或直接 `AdminAddr`）→ 握手
    （pin 管理端公钥，空则 TOFU——首连学到的公钥经 `PersistPub` 回写
    client.json）→ hello → 3s 心跳 + 指令执行；ack 之前先推 status，
    保证服务端 SendCommand 返回时注册表已更新；`OnPrompt`/`OnConn`/
    `OnNotify`（教师消息气泡）/`PersistState`（helperOn/features 回写
    state.json，重启后策略继续生效）钩子；`enforce()` 把已启用功能落到
    本机（`enforce_windows.go` 的 DisableTaskMgr、guard_offline
    断线锁屏），落地失败的键经 `StatusMsg.FeatureErr` 上报；
    `castGen` 广播代数拒绝过期 cast.start/stop；`screen.watch` 带
    `hi` 标记进单机高清模式（1600px/~1.4fps，`ScreenMsg.Hi` 路由到
    服务端 hiFrames 而非缩略图墙）；`collect.open` 带 `dir`/`glob`
    时按目录匹配自动上传（collectDir），空则弹互动页
  - `mcast.go` 屏幕广播组播通道（仿极域）：教师端把 AES-256-GCM 加密帧
    切片组播到 239.42.4.23:47824，一次发送全班接收；每数据报另附
    HMAC-SHA256 截断 tag（HKDF 派生密钥），伪造分片无法污染重组器；
    会话参数（组/端口/会话号/密钥）经 TCP 控制通道随 `cast.start` 下发；
    组播加入失败的客户端回 "tcp"、加入后 ~10s 收不到帧的发 `cast_tcp`
    告警，都由 `frames.go` 的单槽队列改走 TCP 单播兜底，缺片整帧丢弃
  - `frames.go` TCP 兜底广播帧分发（每客户端单槽队列，慢端不积压）
  - `extras.go` 屏幕墙/广播/签到/文件/投票/演示的服务端状态与恢复逻辑
  - `config.go` `AgentConfig`（client.json：adminAddr/adminPub/token/id/room）
    与持久化辅助；`netinfo.go` 本机 IPv4 枚举
  - `sockopt_{unix,windows}.go` 平台 socket 选项（Windows 的 SetsockoptInt 签名不同）
- `internal/capture/` — 屏幕捕获（robotgo）：`Grab`/`Encode`（画质档参数）
  与 `Fingerprinter` 画面指纹（静止桌面跳帧，广播心跳 4s 一保）
- `internal/castview/` — 学生机原生广播画面：`viewer_windows.go` Win32
  全屏/窗口渲染 + 顶部悬浮工具条（模式切换/退出），其他平台为 no-op。
  状态机三分：广播(mode/visible)、教师锁(teachLock)、守护锁(guardLock)
  互独立，`syncState` 统一算输入锁/窗口形态/工具条；全屏或任一锁定
  → 低级钩子吞键鼠（工具条矩形内放行）+ 藏任务栏。
  紧急解锁：Ctrl+Alt+Shift+F12 三秒内连按三次（F12 长按自动重复
  不计数）。解锁同步 disarm agent 侧守护标记——本次断线期不再上锁
  （管理员接管语义），重连后的下次断线正常再上锁；离线告警缓存
  进 pendAlerts 上限 8 条，重连即补发
- `internal/trayicon/` — agent 用无 cgo Win32 系统托盘（菜单/气泡/提示），
  `trayicon_stub.go` 为非 Windows 平台 no-op；`clover.ico` embed 进 exe
- `internal/studentui/` — 学生本机回环 HTTP 互动页（签到/投票/举手/
  演示确认/提交文件）：服务常驻但**不自动开浏览器**，`UI.Open()` 按需唤起
  （托盘菜单或新课堂活动气泡）；loopback + token 双重校验
- `cmd/client/main.go` — 学生机静默 agent 入口（GUI 子系统，无控制台）：
  读 client.json → 日志写 `%LOCALAPPDATA%\LabHelper\agent.log` →
  命名互斥体单实例 → 系统托盘（三叶草）+ 气泡通知 → `studentui` 按需唤起。
  `-id` 只打印身份后退出，供 install.bat 首启固化。
- `index.html` + `src/main.tsx` + `src/admin/` — React 管理端 UI：
  课堂宫格（预览+选择+快捷工具条，`ClassroomView.tsx`）、设备列表与客户端分发。
- `castbar.html` + `src/castbar-entry.tsx` + `src/castbar/` — 管理端广播
  悬浮条页（仿极域顶栏：模式切换/画质/管理端/退出，`--app-region` 拖动）。
- `src/components/ui/` — shadcn/ui Base Nova 组件；`src/index.css` — 共用主题与布局令牌。
- `src/api.ts` — MyGo 绑定与浏览器 mock 的单一 API 边界；`src/store.ts` — Zustand 跨视图状态。
- `src/mock.ts` — 无 MyGo runtime 时（浏览器调试）的假数据。
- `tools/genicon/` — 三叶草图标生成器（resources/icon.png、tray*.png、
  internal/trayicon/clover.ico）；`resources/` 为应用与托盘图标。

## 数据目录（持久化）

`mygo.App.Path(PathUserData)`：macOS `~/Library/Application Support/lab-assistant[ Dev]`，
Windows `%AppData%\lab-assistant`。
- `admin.key` — 管理端 Ed25519 身份，首启生成后复用（客户端 pin 的就是它）
- `registry.json` — 设备注册表，1s 防抖写盘；重启后设备保留且标离线
- `state.json`（学生机侧）— `%APPDATA%\LabHelper\state.json`，用户可写；
  helperOn/features 等可变策略（client.json 安装后被 ACL 收成只读，
  可变状态必须分开持久化）
- `client.json`（学生机侧）— agent.exe 同目录，installer 部署；含
  adminAddr/adminPub/token/id/room + 回写的 helperOn/features 策略，
  agent 首启 `-id` 固化 id+token
- `seats.json`（管理端）— 座位布局（设备 ID → [行,列] + 列数）

## 分发流程（机房还原软件）

管理端「分发客户端…」→ 显示本机 LAN IP:47823 + 公钥指纹 →
「导出安装包」生成 `client-package/`：`client.json`（已写管理端地址+公钥）、
`agent.exe`（若已交叉编译）、`install.bat`、`uninstall.bat`、
`verify-config.ps1` 与 `README.txt`。管理端导出和 GitHub 发布包共用 `packaging/windows/client-package/` 模板。

学生机：**解冻状态下** 管理员运行 `install.bat` →
校验 `client.json` 中的管理端 Ed25519 公钥 → 拷到 `C:\LabHelper` →
写入登录启动项并收紧目录 ACL（Users 只读）→ 首启固化身份 →
用户登录后 agent.exe 静默驻留系统托盘（三叶草图标，无窗口无终端）→
管理端看到上线后创建还原点冻结。

教师机换 IP：agent 连不上 `adminAddr` 时回退 UDP 广播发现，公钥匹配即重连，
无需重装；换管理端密钥则需重新导出安装包。

## 正式部署时还要做

- 离线设备的指令队列（现在 SendCommand 对离线机直接报错，应改为上线补发）
- 客户端 Windows 化：SYSTEM 服务（install.bat 现为计划任务兜底）+
  守护进程互守 + IFEO + 禁蓝屏改全屏锁定（DisableTaskMgr 已由 lock_taskmgr
  落地，但 HKCU 用户策略可被学生自己改回，服务级执行才能防篡改）
- client.json 的 token 是明文 bearer 凭证，目录 ACL 只能挡普通读取——
  学生拿到自己机器的 token 可冒充本机。要挡复制得换 DPAPI/TPM 绑定的凭证。
- 其余 catalog 功能的 Windows 执行端（USB 存储/网站黑白名单/进程守护等）
  尚未实现，目录里标 `ready: false` 不展示为已生效
- 可选：管理端加一个 `net/http` 适配器把同一套 UI 挂到局域网浏览器
