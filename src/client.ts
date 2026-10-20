// 客户端（学生机）页面：展示本机防护状态与已启用功能。
import { isMyGo } from "mygo-runtime";
import { Lab, events, type ClientStatus, type Device, type FeatureGroup } from "./mygo";
import { mockLab } from "./mock";

const $ = <T extends HTMLElement>(s: string) => document.querySelector<T>(s)!;

const api = isMyGo() ? Lab : mockLab;
const subscribeDevices = isMyGo()
  ? (cb: (d: Device[]) => void) => { events.devices.on(cb); }
  : (cb: (d: Device[]) => void) => mockLab.onDevices(cb);

let catalog: FeatureGroup[] = [];
let status: ClientStatus | null = null;

const SELF_ID = "pc-02"; // 与 main.go Status() 的 mock 机器一致

const TICK_SVG = `<svg viewBox="0 0 10 10" width="9" height="9"><path d="M2 5.2l2 2L8 3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>`;

function render() {
  if (!status) return;
  $("#mnum").textContent = status.label;
  $("#mname").textContent = status.name;
  $("#mip").textContent = status.ip;

  const st = $("#st");
  st.className = "st " + (status.helperOn ? "on" : "off");
  $("#stDot").className = "dot " + (status.helperOn ? "ok" : "warn");
  $("#stText").textContent = status.helperOn ? "防护中" : "已停用";

  const on = new Set(status.features);
  const rows = catalog.flatMap((g) =>
    g.features
      .filter((f) => on.has(f.key))
      .map((f) => `<div class="row"><span class="tick">${TICK_SVG}</span><span class="name">${f.name}</span><span class="grp">${g.title}</span></div>`),
  );
  $("#featCount").textContent = `${status.features.length} 项`;
  $("#featList").innerHTML = status.helperOn && rows.length
    ? rows.join("")
    : `<div class="empty">${status.helperOn ? "未启用任何功能" : "防护已停用，功能不生效"}</div>`;
}

async function init() {
  [catalog, status] = await Promise.all([api.catalog(), api.status()]);
  render();

  // 管理端下发变更时刷新本机状态
  subscribeDevices((ds) => {
    const me = ds.find((d) => d.id === SELF_ID);
    if (me) {
      status = {
        name: me.name, label: me.label, ip: me.ip,
        helperOn: me.helperOn, features: me.features,
      };
      render();
    }
  });

  const modal = $("#pwModal");
  $("#exitBtn").onclick = () => {
    modal.hidden = false;
    $<HTMLInputElement>("#pwInput").value = "";
    $("#pwErr").hidden = true;
    setTimeout(() => $<HTMLInputElement>("#pwInput").focus(), 50);
  };
  $("#pwCancel").onclick = () => (modal.hidden = true);
  modal.addEventListener("click", (e) => {
    if (e.target === e.currentTarget) modal.hidden = true;
  });
  const tryExit = async () => {
    const ok = await api.exit($<HTMLInputElement>("#pwInput").value);
    if (ok) {
      modal.hidden = true;
    } else {
      $("#pwErr").hidden = false;
    }
  };
  $("#pwOk").onclick = tryExit;
  $("#pwInput").addEventListener("keydown", (e) => {
    if (e.key === "Enter") tryExit();
  });
}

init();
