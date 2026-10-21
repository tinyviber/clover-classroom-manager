// 浏览器预览 mock：页面不在 MyGo 窗口里运行时（如用 vite 单独调试样式），
// 用与 main.go 相同的假数据替代 Lab 绑定，让界面可交互。
import type { ClassroomState, Device, FeatureGroup } from "./mygo";

// 与 main.go catalog() 同步：只有这两项在客户端有真实执行实现。
const readyKeys = new Set(["guard_offline", "lock_taskmgr"]);
type RawFeature = { key: string; name: string; desc: string };
const rawCatalog: { title: string; features: RawFeature[] }[] = [
  {
    title: "脱控防护",
    features: [
      { key: "guard_offline", name: "断网/拔网线检测", desc: "检测到断网时自动锁定并告警" },
      { key: "guard_process", name: "防终止/挂起进程", desc: "保护电子教室与小助手进程不被结束或挂起" },
      { key: "guard_ip", name: "IP 地址篡改恢复", desc: "学生修改 IP 后自动恢复并上报" },
      { key: "guard_firewall", name: "防火墙阻止检测", desc: "防止勾选「阻止所有传入连接」脱控" },
      { key: "guard_vdesk", name: "虚拟桌面防护", desc: "阻止用 Win+Tab 虚拟桌面脱离控制" },
      { key: "guard_safemode", name: "安全模式防护", desc: "开机进入安全模式时蓝屏锁定" },
    ],
  },
  {
    title: "系统限制",
    features: [
      { key: "lock_taskmgr", name: "禁用任务管理器", desc: "含性能页的资源监视器" },
      { key: "lock_regedit", name: "禁用注册表", desc: "regedit 与策略编辑器" },
      { key: "lock_cmd", name: "禁用命令行", desc: "cmd / PowerShell / 脚本宿主" },
      { key: "lock_settings", name: "禁用控制面板与设置", desc: "防止修改网络与系统配置" },
    ],
  },
  {
    title: "外设与存储",
    features: [
      { key: "usb_storage", name: "禁用 U 盘/移动存储", desc: "保留 USB 键鼠，仅拦截存储设备" },
      { key: "usb_bootguard", name: "PE 启动防护提醒", desc: "检测到 PE 工具痕迹时上报告警" },
    ],
  },
  {
    title: "网络与浏览",
    features: [
      { key: "net_download", name: "禁止浏览器下载", desc: "阻止下载破解工具与安装包" },
      { key: "net_netdisk", name: "禁止网盘网站", desc: "屏蔽常见网盘登录页" },
      { key: "net_gamesite", name: "禁止游戏网站", desc: "如 poki.com、digdig.io" },
    ],
  },
  {
    title: "游戏与小程序",
    features: [
      { key: "game_builtin", name: "禁系统小游戏", desc: "扫雷、纸牌、拼图板、放大镜" },
      { key: "game_browser", name: "禁浏览器小游戏", desc: "Chrome 小恐龙、Edge 冲浪" },
    ],
  },
  {
    title: "桌面",
    features: [
      { key: "wallpaper", name: "机房行为规范壁纸", desc: "统一桌面壁纸，附行为规范" },
      { key: "show_id", name: "右上角显示机器编号", desc: "取计算机名后 6 位" },
    ],
  },
];

export const catalog: FeatureGroup[] = rawCatalog.map((group) => ({
  ...group,
  features: group.features.map((feature) => ({ ...feature, ready: readyKeys.has(feature.key) })),
}));

const allKeys = () => catalog.flatMap((g) => g.features.map((f) => f.key));

function seed(): Device[] {
  const core = [
    "guard_offline", "guard_process", "guard_ip",
    "lock_taskmgr", "lock_regedit", "lock_cmd",
    "usb_storage", "net_netdisk", "wallpaper", "show_id",
  ];
  const devs: Device[] = [];
  for (let i = 1; i <= 24; i++) {
    devs.push({
      id: `pc-${String(i).padStart(2, "0")}`,
      name: `LAB301-PC${String(i).padStart(2, "0")}`,
      label: `A-${String(i).padStart(2, "0")}`,
      ip: `192.168.31.${100 + i}`,
      mac: `54:EE:75:0A:1B:${i.toString(16).padStart(2, "0").toUpperCase()}`,
      online: true,
      helperOn: true,
      version: "0.0.2",
      alerts: 0,
      features: [...core],
      pending: false,
      locked: false,
      castTcp: false,
      lastSeen: "刚刚",
    });
  }
  devs[3]!.helperOn = false; devs[3]!.features = [];
  devs[6]!.alerts = 2; devs[6]!.lastSeen = "1 分钟前";
  devs[10]!.online = false; devs[10]!.helperOn = false; devs[10]!.features = []; devs[10]!.lastSeen = "18 分钟前";
  devs[14]!.online = false; devs[14]!.helperOn = false; devs[14]!.features = []; devs[14]!.lastSeen = "2 小时前";
  devs[17]!.alerts = 1;
  devs[18]!.features = allKeys(); devs[18]!.version = "0.0.1";
  return devs;
}

const state = {
  devices: seed(),
  classroom: {
    screens: [],
    screenWatchIds: [],
    signIn: { open: false, id: "", deadline: 0, targetIds: [], sent: 0, replies: [], online: 22 },
    files: [],
    collects: [],
    helps: [],
    poll: { open: false, id: "", question: "", options: [], deadline: 0, targetIds: [], sent: 0, replies: [] },
    demo: { state: "", sessionId: "", sourceId: "", sourceLabel: "", targetIds: [], requestedAt: "", err: "" },
    castMode: "",
    castTargetIds: [],
    castQuality: "better",
    castTitle: "",
    focusId: "",
  } as ClassroomState,
  watching: new Set<string>(),
  screenTimer: 0,
};
const seatStore = { cols: 8, seats: {} as Record<string, number[]> };

// 与 main.go validCollectDir / agent 白名单一致：浏览器 mock 也要拒掉越界目录，
// 否则「指定目录收取」的失败提示只在真机上才出现。
function allowedCollectDir(dir: string): boolean {
  if (!dir || dir.includes("..") || dir.startsWith("\\\\")) return false;
  const low = dir.replaceAll("\\", "/").replace(/\/+$/, "").toLowerCase();
  const roots = ["c:/users/public", "c:/labhelper/homework"];
  return roots.some((root) => low === root || low.startsWith(`${root}/`));
}
const listeners = new Set<(d: Device[]) => void>();
const emit = () => listeners.forEach((cb) => cb([...state.devices]));
const classroomListeners = new Set<(s: ClassroomState) => void>();
const cloneClassroom = (): ClassroomState => ({
  ...state.classroom,
  screens: state.classroom.screens.map((item) => ({ ...item })),
  screenWatchIds: [...state.classroom.screenWatchIds],
    signIn: { ...state.classroom.signIn, targetIds: [...state.classroom.signIn.targetIds], replies: state.classroom.signIn.replies.map((item) => ({ ...item })) },
  files: state.classroom.files.map((item) => ({ ...item })),
  helps: state.classroom.helps.map((item) => ({ ...item })),
  poll: { ...state.classroom.poll, options: [...state.classroom.poll.options], targetIds: [...state.classroom.poll.targetIds], replies: state.classroom.poll.replies.map((item) => ({ ...item })) },
  demo: { ...state.classroom.demo, targetIds: [...state.classroom.demo.targetIds] },
});
const emitClassroom = () => {
  const snapshot = cloneClassroom();
  classroomListeners.forEach((cb) => cb(snapshot));
};

function mockScreen(id: string) {
  const device = state.devices.find((item) => item.id === id);
  if (!device) return;
  const canvas = document.createElement("canvas");
  canvas.width = 480;
  canvas.height = 270;
  const ctx = canvas.getContext("2d");
  if (!ctx) return;
  const gradient = ctx.createLinearGradient(0, 0, 480, 270);
  gradient.addColorStop(0, "#dce9e4");
  gradient.addColorStop(1, "#b7c9c4");
  ctx.fillStyle = gradient;
  ctx.fillRect(0, 0, 480, 270);
  ctx.fillStyle = "#f8faf8";
  ctx.fillRect(0, 0, 480, 28);
  ctx.fillStyle = "#587067";
  ctx.font = "13px sans-serif";
  ctx.fillText("三叶草局域网课堂管理 · 学生屏幕预览", 16, 19);
  ctx.fillStyle = "#324841";
  ctx.font = "bold 24px sans-serif";
  ctx.fillText(device.label, 24, 88);
  ctx.font = "17px sans-serif";
  ctx.fillText(device.name, 24, 119);
  ctx.fillStyle = "#71867e";
  ctx.font = "14px sans-serif";
  ctx.fillText("浏览器预览画面", 24, 161);
  const jpeg = canvas.toDataURL("image/jpeg", 0.58).split(",")[1] ?? "";
  const frame = {
    deviceId: id, label: device.label, name: device.name, jpeg, at: Date.now(),
  };
  state.classroom.screens = [
    ...state.classroom.screens.filter((item) => item.deviceId !== id), frame,
  ];
}

// 与 mygo.ts 中 Lab 服务同形的 mock 实现
export const mockLab = {
  async devices() { return [...state.devices]; },
  async catalog() { return catalog; },
  async classroomState() { return cloneClassroom(); },
  async castMode() { return state.classroom.castMode; },
  onClassroom(cb: (s: ClassroomState) => void) {
    classroomListeners.add(cb);
    return () => classroomListeners.delete(cb);
  },
  async watchScreens(ids: string[], on: boolean) {
    const targets = ids.length ? ids : state.devices.filter((device) => device.online).map((device) => device.id);
    if (on) {
      state.classroom.screenWatchIds = [...targets];
      targets.forEach((id) => state.watching.add(id));
      targets.forEach(mockScreen);
      if (!state.screenTimer) {
        state.screenTimer = window.setInterval(() => {
          state.watching.forEach(mockScreen);
          if (state.watching.size) emitClassroom();
        }, 1500);
      }
    } else {
      targets.forEach((id) => state.watching.delete(id));
      state.classroom.screenWatchIds = state.classroom.screenWatchIds.filter((id) => !targets.includes(id));
      if (!state.watching.size && state.screenTimer) {
        clearInterval(state.screenTimer);
        state.screenTimer = 0;
      }
    }
    emitClassroom();
  },
  async castStart(mode: string, _ids: string[]) {
    if (state.classroom.demo.state === "pending" || state.classroom.demo.state === "active") return false;
    state.classroom.castMode = mode;
    state.classroom.castTargetIds = [..._ids];
    emitClassroom();
    return true;
  },
  async castStop() { state.classroom.castMode = ""; state.classroom.castTargetIds = []; emitClassroom(); },
  async castSetQuality(level: string) { state.classroom.castQuality = level; emitClassroom(); },
  async collectFiles(ids: string[], dir: string, _glob: string) {
    if (dir && !allowedCollectDir(dir)) return -1;
    const targets = state.devices.filter((device) => device.online && ids.includes(device.id));
    return targets.length;
  },
  async screenLock(ids: string[], _on: boolean) {
    return state.devices.filter((d) => d.online && ids.includes(d.id)).length;
  },
  async powerAction(ids: string[], _action: string, _force: boolean) {
    return state.devices.filter((d) => d.online && ids.includes(d.id)).length;
  },
  async powerCancel(ids: string[]) {
    return state.devices.filter((d) => d.online && ids.includes(d.id)).length;
  },
  async notifyText(ids: string[], _text: string) {
    return state.devices.filter((d) => d.online && ids.includes(d.id)).length;
  },
  async openURLFor(ids: string[], _url: string) {
    return state.devices.filter((d) => d.online && ids.includes(d.id)).length;
  },
  async launchAppFor(ids: string[], _path: string) {
    return state.devices.filter((d) => d.online && ids.includes(d.id)).length;
  },
  async viewDevice(id: string, on: boolean) {
    state.classroom.focusId = on ? id : "";
    emitClassroom();
    if (on) {
      mockScreen(id);
      const frame = state.classroom.screens.find((item) => item.deviceId === id);
      if (frame) state.classroom.focusScreen = { ...frame };
      emitClassroom();
    } else {
      state.classroom.focusScreen = undefined;
    }
    emitClassroom();
    return true;
  },
  async seats() { return { ...seatStore.seats }; },
  async seatCols() { return seatStore.cols; },
  async setSeatCols(cols: number) { seatStore.cols = cols; },
  async saveSeats(seats: Record<string, number[]>) { seatStore.seats = { ...seats }; },
  async focusAdmin() {},
  async signInStart(seconds: number, ids: string[]) {
    const online = state.devices.filter((device) => device.online && (!ids.length || ids.includes(device.id)));
    state.classroom.signIn = {
      open: true,
      id: `mock-sign-${Date.now()}`,
      deadline: seconds > 0 ? Math.floor(Date.now() / 1000) + seconds : 0,
      targetIds: online.map((device) => device.id),
      sent: online.length,
      replies: [],
      online: online.length,
    };
    emitClassroom();
    return online.length;
  },
  async signInStop() {
    state.classroom.signIn.open = false;
    emitClassroom();
  },
  async pickFiles() { return []; },
  async sendFiles(ids: string[], paths: string[]) {
    const targets = state.devices.filter((device) => device.online && (!ids.length || ids.includes(device.id)));
    for (const device of targets) for (const path of paths) {
      const name = path.replace(/^mock:\/\//, "").split(/[\\/]/).pop() || "文件";
      state.classroom.files.unshift({
        id: `mock-file-${Date.now()}-${Math.random().toString(16).slice(2)}`,
        deviceId: device.id,
        label: device.label,
        name,
        size: 256 * 1024,
        sent: 256 * 1024,
        done: true,
        in: false,
        at: new Date().toISOString(),
      });
    }
    emitClassroom();
    return targets.length * paths.length;
  },
  async openRecvDir() { return "(浏览器预览)"; },
  async pollStart(question: string, options: string[], seconds: number, ids: string[]) {
    const online = state.devices.filter((device) => device.online && (!ids.length || ids.includes(device.id)));
    state.classroom.poll = {
      open: true,
      id: `mock-poll-${Date.now()}`,
      question,
      options: [...options],
      deadline: seconds > 0 ? Math.floor(Date.now() / 1000) + seconds : 0,
      targetIds: online.map((device) => device.id),
      sent: online.length,
      replies: [],
    };
    emitClassroom();
    return online.length;
  },
  async pollStop() { state.classroom.poll.open = false; emitClassroom(); },
  async clearHelp(deviceID: string) {
    state.classroom.helps = state.classroom.helps.filter((item) => item.deviceId !== deviceID);
    emitClassroom();
  },
  async demoStart(sourceID: string, targetIDs: string[]) {
    if (state.classroom.castMode) return false;
    const source = state.devices.find((item) => item.id === sourceID);
    state.classroom.demo = {
      state: "pending",
      sessionId: `mock-demo-${Date.now()}`,
      sourceId: sourceID,
      sourceLabel: source?.label ?? sourceID,
      targetIds: [...targetIDs],
      requestedAt: new Date().toISOString(),
      err: "",
    };
    emitClassroom();
    return true;
  },
  async demoStop() {
    state.classroom.demo = { state: "", sessionId: "", sourceId: "", sourceLabel: "", targetIds: [], requestedAt: "", err: "" };
    emitClassroom();
  },
  async setHelper(ids: string[], on: boolean) {
    let n = 0;
    for (const d of state.devices) if (ids.includes(d.id)) { d.helperOn = on; n++; }
    emit();
    return n;
  },
  async setFeatures(id: string, keys: string[]) {
    const d = state.devices.find((x) => x.id === id);
    if (d) d.features = keys;
    emit();
    return d!;
  },
  async applyTemplate(ids: string[], keys: string[]) {
    let n = 0;
    for (const d of state.devices) if (ids.includes(d.id)) { d.features = [...keys]; n++; }
    emit();
    return n;
  },
  async scan() { return [...state.devices]; },
  onDevices(cb: (d: Device[]) => void) {
    listeners.add(cb);
    return () => listeners.delete(cb);
  },
  async adminInfo() {
    return {
      ips: ["192.168.31.1"],
      fingerprint: "SHA256:000000000000000000",
      port: 47823,
      pubB64: "",
      dataDir: "(浏览器预览)",
    };
  },
  async exportClientPackage(_addr: string) { return ""; },
  async approveDevices(ids: string[]) {
    let n = 0;
    for (const d of state.devices) if (ids.includes(d.id) && d.pending) { d.pending = false; n++; }
    emit();
    return n;
  },
  async rejectDevices(ids: string[]) {
    const before = state.devices.length;
    state.devices = state.devices.filter((d) => !(ids.includes(d.id) && d.pending));
    if (state.devices.length !== before) emit();
    return before - state.devices.length;
  },
  async clearAlerts(_id: string) {},
};
