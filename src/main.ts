// 管理端页面逻辑：设备列表、批量启停、单机功能勾选。
// Lab 服务与 Device/FeatureGroup 类型由 `mygo generate` 从 main.go 生成。
// 页面在普通浏览器中打开（无 MyGo runtime）时改用本地 mock，便于预览样式。
import { isMyGo } from "mygo-runtime";
import { Lab, events, type Device, type FeatureGroup } from "./mygo";
import { mockLab } from "./mock";

const api = isMyGo() ? Lab : mockLab;
const subscribeDevices = isMyGo()
  ? (cb: (d: Device[]) => void) => { events.devices.on(cb); }
  : (cb: (d: Device[]) => void) => mockLab.onDevices(cb);

const $ = <T extends HTMLElement>(s: string) => document.querySelector<T>(s)!;
const $$ = <T extends HTMLElement>(s: string) => [...document.querySelectorAll<T>(s)];

// ---- 状态 ------------------------------------------------------------------

let devices: Device[] = [];
let catalog: FeatureGroup[] = [];
let filter: "all" | "online" | "offline" | "alert" = "all";
let query = "";
const selected = new Set<string>();
const expanded = new Set<string>();

type ModalState =
  | { mode: "single"; device: Device }
  | { mode: "template"; ids: string[] }
  | null;
let modal: ModalState = null;

const FEATURE_TOTAL = () => catalog.reduce((n, g) => n + g.features.length, 0);

const visible = () =>
  devices.filter((d) => {
    if (filter === "online" && !d.online) return false;
    if (filter === "offline" && d.online) return false;
    if (filter === "alert" && d.alerts === 0) return false;
    if (query) {
      const q = query.toLowerCase();
      return (
        d.name.toLowerCase().includes(q) ||
        d.label.toLowerCase().includes(q) ||
        d.ip.includes(q)
      );
    }
    return true;
  });

// ---- KPI -------------------------------------------------------------------

function renderKpis() {
  const total = devices.length;
  const online = devices.filter((d) => d.online).length;
  const guard = devices.filter((d) => d.helperOn).length;
  const alerts = devices.filter((d) => d.alerts > 0).length;
  $("#kTotal").textContent = String(total);
  $("#kOnline").textContent = String(online);
  $("#kOnlineSub").textContent = `离线 ${total - online} 台`;
  $("#kGuard").textContent = String(guard);
  $("#kGuardSub").textContent = `已停用 ${total - guard} 台`;
  const kAlert = $("#kAlert");
  kAlert.textContent = String(alerts);
  kAlert.style.color = alerts > 0 ? "var(--amber)" : "";
  $("#status").textContent =
    `共 ${total} 台 · 在线 ${online} · 上次扫描 ${new Date().toLocaleTimeString("zh-CN", { hour12: false })}`;
}

// ---- 设备表 -----------------------------------------------------------------

function renderTable() {
  const tbody = $("#rows");
  tbody.innerHTML = "";
  const list = visible();
  if (list.length === 0) {
    tbody.innerHTML = `<tr><td colspan="9" style="padding:32px;text-align:center">
      <div style="font-weight:500">没有匹配的设备</div>
      <div style="color:var(--muted);font-size:12px;margin-top:3px">换个筛选条件或搜索词试试</div></td></tr>`;
    return;
  }
  for (const d of list) {
    const tr = document.createElement("tr");
    tr.className =
      "data" +
      (selected.has(d.id) ? " sel" : "") +
      (expanded.has(d.id) ? " open" : "") +
      (!d.online ? " off" : "") +
      (d.alerts > 0 ? " alert" : "");
    tr.innerHTML = `
      <td><input type="checkbox" class="cbx row-cbx" ${selected.has(d.id) ? "checked" : ""}></td>
      <td class="num">${d.label}</td>
      <td><span class="who"><span class="avatar">${d.label.replace(/^./, "")}</span><span class="dname">${d.name}</span>${d.alerts > 0 ? `<span class="badge warn">${d.alerts} 告警</span>` : ""}</span></td>
      <td class="muted ip">${d.ip}</td>
      <td><span class="st"><span class="d"></span>${d.online ? "在线" : "离线"}</span></td>
      <td>${d.helperOn ? `<span class="badge ok">防护中</span>` : `<span class="badge">已停用</span>`}</td>
      <td class="n">${d.features.length}/${FEATURE_TOTAL()}</td>
      <td class="muted">${d.lastSeen}</td>
      <td><button class="btn pill cfg-btn">配置</button></td>`;

    tr.querySelector<HTMLInputElement>(".row-cbx")!.onclick = (e) => {
      e.stopPropagation();
      const el = e.currentTarget as HTMLInputElement;
      el.checked ? selected.add(d.id) : selected.delete(d.id);
      tr.classList.toggle("sel", el.checked);
      renderBatchBar();
      syncCbxAll();
    };
    tr.querySelector<HTMLElement>(".cfg-btn")!.onclick = (e) => {
      e.stopPropagation();
      openModal({ mode: "single", device: d });
    };
    tr.onclick = () => {
      expanded.has(d.id) ? expanded.delete(d.id) : expanded.add(d.id);
      tr.classList.toggle("open");
      dt.classList.toggle("open");
    };

    const dt = document.createElement("tr");
    dt.className = "detail" + (expanded.has(d.id) ? " open" : "");
    const on = new Set(d.features);
    const featChips =
      d.features.length === 0
        ? `<span class="muted" style="font-size:11px">未启用任何功能</span>`
        : catalog
            .flatMap((g) => g.features)
            .filter((f) => on.has(f.key))
            .map((f) => `<span class="chip">${f.name}</span>`)
            .join("");
    dt.innerHTML = `<td colspan="9"><dl class="d-box">
      <dt>MAC 地址</dt><dd class="mono">${d.mac}</dd>
      <dt>客户端版本</dt><dd>v${d.version}${d.version !== "13.1" ? ` <span class="badge warn">可升级</span>` : ""}</dd>
      <dt>已启用功能</dt><dd><div class="feat-chips">${featChips}</div></dd>
      <dt>快捷操作</dt><dd><div class="acts">
        <button class="btn pill ${d.helperOn ? "" : "accent"}" data-act="toggle">${d.helperOn ? "停用防护" : "启用防护"}</button>
        <button class="btn pill" data-act="cfg">配置功能…</button>
        <button class="btn pill" data-act="tpl">套用全量模板</button>
      </div></dd>
    </dl></td>`;
    dt.querySelector<HTMLElement>('[data-act="toggle"]')!.onclick = async (e) => {
      e.stopPropagation();
      const n = await api.setHelper([d.id], !d.helperOn);
      toast(`${d.name}：${d.helperOn ? "已下发停用" : "已下发启用"}（${n} 台）`);
    };
    dt.querySelector<HTMLElement>('[data-act="cfg"]')!.onclick = (e) => {
      e.stopPropagation();
      openModal({ mode: "single", device: d });
    };
    dt.querySelector<HTMLElement>('[data-act="tpl"]')!.onclick = async (e) => {
      e.stopPropagation();
      const all = catalog.flatMap((g) => g.features.map((f) => f.key));
      await api.applyTemplate([d.id], all);
      toast(`${d.name}：已套用全量模板`);
    };
    tbody.append(tr, dt);
  }
}

function syncCbxAll() {
  const list = visible();
  const all = list.length > 0 && list.every((d) => selected.has(d.id));
  const cbx = $<HTMLInputElement>("#cbxAll");
  cbx.checked = all;
  cbx.indeterminate = !all && list.some((d) => selected.has(d.id));
}

function renderBatchBar() {
  const bar = $("#batchBar");
  bar.hidden = selected.size === 0;
  $("#selCount").textContent = `已选 ${selected.size} 台`;
}

function renderAll() {
  renderKpis();
  renderTable();
  renderBatchBar();
  syncCbxAll();
}

// ---- 功能配置对话框 -----------------------------------------------------------

function featureChecklist(checked: Set<string>): string {
  return catalog
    .map(
      (g) => `<div class="cfg-group">
        <div class="row-head"><span class="label">${g.title}</span><span class="mini">${g.features.length} 项</span></div>
        <div class="cfg-list">${g.features
          .map(
            (f) => `<div class="row">
              <input type="checkbox" class="cbx f-cbx" data-key="${f.key}" ${checked.has(f.key) ? "checked" : ""}>
              <div class="who"><span class="name">${f.name}</span><span class="sub muted">${f.desc}</span></div>
            </div>`,
          )
          .join("")}</div>
      </div>`,
    )
    .join("");
}

function openModal(m: ModalState) {
  modal = m;
  if (!m) return;
  const checked =
    m.mode === "single" ? new Set(m.device.features) : new Set<string>();
  $("#cfgTitle").textContent =
    m.mode === "single" ? `配置功能 — ${m.device.name}` : "应用功能模板";
  $("#cfgSub").textContent =
    m.mode === "single"
      ? `${m.device.label} · ${m.device.ip}`
      : `将勾选项套用到 ${m.ids.length} 台已选设备`;
  $("#cfgBody").innerHTML = featureChecklist(checked);
  $("#cfgModal").hidden = false;
}

function closeModal() {
  modal = null;
  $("#cfgModal").hidden = true;
}

async function applyModal() {
  if (!modal) return;
  const keys = $$<HTMLInputElement>("#cfgBody .f-cbx")
    .filter((c) => c.checked)
    .map((c) => c.dataset.key!);
  if (modal.mode === "single") {
    await api.setFeatures(modal.device.id, keys);
    toast(`${modal.device.name}：已保存 ${keys.length} 项功能`);
  } else {
    const n = await api.applyTemplate(modal.ids, keys);
    toast(`模板已应用到 ${n} 台设备（${keys.length} 项）`);
  }
  closeModal();
}

// ---- 提示条 -----------------------------------------------------------------

let toastTimer = 0;
function toast(msg: string, err = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast" + (err ? " err" : "");
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = window.setTimeout(() => (t.hidden = true), 2600);
}

// ---- 初始化 -----------------------------------------------------------------

async function init() {
  [devices, catalog] = await Promise.all([api.devices(), api.catalog()]);
  renderAll();

  subscribeDevices((ds) => {
    devices = ds;
    renderAll();
  });

  // 筛选 segs
  const seg = $("#stateSegs");
  const thumb = seg.querySelector<HTMLElement>(".thumb")!;
  const place = () => {
    const on = seg.querySelector<HTMLElement>(".opt.on");
    if (on) {
      thumb.style.left = on.offsetLeft + "px";
      thumb.style.width = on.offsetWidth + "px";
    }
  };
  $$<HTMLElement>("#stateSegs .opt").forEach((o) => {
    o.onclick = () => {
      $$("#stateSegs .opt").forEach((x) => x.classList.remove("on"));
      o.classList.add("on");
      filter = o.dataset.f as typeof filter;
      place();
      renderTable();
      syncCbxAll();
    };
  });
  requestAnimationFrame(place);
  addEventListener("resize", place);

  // 搜索
  $<HTMLInputElement>("#searchInput").oninput = (e) => {
    query = (e.currentTarget as HTMLInputElement).value.trim();
    renderTable();
    syncCbxAll();
  };

  // 全选
  $<HTMLInputElement>("#cbxAll").onchange = (e) => {
    const on = (e.currentTarget as HTMLInputElement).checked;
    visible().forEach((d) => (on ? selected.add(d.id) : selected.delete(d.id)));
    renderTable();
    renderBatchBar();
    syncCbxAll();
  };

  // 批量操作
  $("#batchOn").onclick = async () => {
    const n = await api.setHelper([...selected], true);
    toast(`已下发启用防护到 ${n} 台设备`);
  };
  $("#batchOff").onclick = async () => {
    const n = await api.setHelper([...selected], false);
    toast(`已下发停用防护到 ${n} 台设备`);
  };
  $("#batchTpl").onclick = () =>
    openModal({ mode: "template", ids: [...selected] });
  $("#batchClear").onclick = () => {
    selected.clear();
    renderTable();
    renderBatchBar();
    syncCbxAll();
  };

  // 对话框
  $("#cfgCancel").onclick = closeModal;
  $("#cfgApply").onclick = applyModal;
  $("#cfgModal").addEventListener("click", (e) => {
    if (e.target === e.currentTarget) closeModal();
  });
  $("#cfgAll").onclick = () =>
    $$<HTMLInputElement>("#cfgBody .f-cbx").forEach((c) => (c.checked = true));
  $("#cfgNone").onclick = () =>
    $$<HTMLInputElement>("#cfgBody .f-cbx").forEach((c) => (c.checked = false));
  addEventListener("keydown", (e) => {
    if (e.key === "Escape" && modal) closeModal();
  });

  // 机房切换（演示 pop 菜单）
  const menu = $<HTMLElement>("#roomMenu");
  const field = $("#roomField");
  const placeMenu = () => {
    const r = field.getBoundingClientRect();
    menu.style.top = r.bottom + 5 + "px";
    menu.style.left = r.left + "px";
    menu.style.setProperty("--ox", r.width / 2 + "px");
  };
  field.onclick = (e) => {
    e.stopPropagation();
    menu.hidden = !menu.hidden;
    if (!menu.hidden) placeMenu();
  };
  document.addEventListener("click", () => (menu.hidden = true));
  menu.addEventListener("click", (e) => e.stopPropagation());
  $$<HTMLElement>("#roomMenu .mi").forEach((mi) => {
    mi.onclick = () => {
      if (mi.dataset.room === "add") {
        toast("演示版暂不支持新增机房", true);
        return;
      }
      $$("#roomMenu .mi").forEach((x) => x.classList.remove("sel"));
      mi.classList.add("sel");
      $("#roomVal").textContent = `${mi.dataset.room} 机房`;
      toast(`已切换到 ${mi.dataset.room} 机房（演示数据不变）`);
    };
  });

  // 分发对话框
  const distModal = $("#distModal");
  $("#distBtn").onclick = async () => {
    const info = await api.adminInfo();
    $("#distFp").textContent = info.fingerprint;
    $("#distDir").textContent = info.dataDir;
    $("#distAddrs").innerHTML = info.ips.length
      ? info.ips
          .map(
            (ip) =>
              `<div class="ip-line"><span class="dot"></span><span class="mono">${ip}:${info.port}</span></div>`,
          )
          .join("") +
        `<div class="ip-line" style="color:var(--muted);font-size:11px">UDP 发现端口 ${47822}</div>`
      : `<span class="muted">未检测到本机 IPv4</span>`;
    distModal.hidden = false;
  };
  $("#distCancel").onclick = () => (distModal.hidden = true);
  distModal.addEventListener("click", (e) => {
    if (e.target === e.currentTarget) distModal.hidden = true;
  });
  $("#distExport").onclick = async () => {
    const out = await api.exportClientPackage();
    if (out) {
      toast(`安装包已导出到 ${out}`);
      distModal.hidden = true;
    } else {
      toast("已取消导出");
    }
  };

  // 扫描 & 主题
  $("#scanBtn").onclick = async () => {
    devices = await api.scan();
    renderAll();
    toast("已重新扫描局域网");
  };
  $("#themeBtn").onclick = () => {
    const r = document.documentElement;
    r.dataset.theme = r.dataset.theme === "dark" ? "light" : "dark";
  };
  if (location.search.includes("dark")) document.documentElement.dataset.theme = "dark";
}

init();
