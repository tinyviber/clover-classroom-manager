// 浏览器预览 mock：页面不在 MyGo 窗口里运行时（如用 vite 单独调试样式），
// 用与 main.go 相同的假数据替代 Lab 绑定，让界面可交互。
import type { ClientStatus, Device, FeatureGroup } from "./mygo";

export const catalog: FeatureGroup[] = [
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
      version: "13.1",
      alerts: 0,
      features: [...core],
      lastSeen: "刚刚",
    });
  }
  devs[3]!.helperOn = false; devs[3]!.features = [];
  devs[6]!.alerts = 2; devs[6]!.lastSeen = "1 分钟前";
  devs[10]!.online = false; devs[10]!.helperOn = false; devs[10]!.features = []; devs[10]!.lastSeen = "18 分钟前";
  devs[14]!.online = false; devs[14]!.helperOn = false; devs[14]!.features = []; devs[14]!.lastSeen = "2 小时前";
  devs[17]!.alerts = 1;
  devs[18]!.features = allKeys(); devs[18]!.version = "13.0";
  return devs;
}

const state = { devices: seed() };
const listeners = new Set<(d: Device[]) => void>();
const emit = () => listeners.forEach((cb) => cb([...state.devices]));

// 与 mygo.ts 中 Lab 服务同形的 mock 实现
export const mockLab = {
  async devices() { return [...state.devices]; },
  async catalog() { return catalog; },
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
  async status(): Promise<ClientStatus> {
    const d = state.devices[1]!;
    return { name: d.name, label: d.label, ip: d.ip, helperOn: d.helperOn, features: [...d.features] };
  },
  async exit(password: string) {
    if (password !== "123456") return false;
    await this.setHelper(["pc-02"], false);
    return true;
  },
  onDevices(cb: (d: Device[]) => void) { listeners.add(cb); },
  async adminInfo() {
    return {
      ips: ["192.168.31.1"],
      fingerprint: "SHA256:000000000000000000",
      port: 47823,
      pubB64: "",
      dataDir: "(浏览器预览)",
    };
  },
  async exportClientPackage() { return ""; },
  async clearAlerts(_id: string) {},
};
