import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
  ArrowDownToLine, Check, CheckCheck, ClipboardCheck, Download, ExternalLink,
  HelpCircle, LayoutGrid, ListChecks, Lock, LockOpen, MessageSquareText,
  Monitor, MonitorOff, Play, Power, Rocket, ScreenShare, Send, Square,
  StopCircle, Users, Video,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Textarea } from "@/components/ui/textarea";
import { api, nativeRuntime } from "@/api";
import type { Device } from "@/mygo";
import { targetDeviceIds, useLabStore } from "@/store";
import { bytesLabel, formatClock } from "./shared";

const qualityLabels: Record<string, string> = {
  perfect: "极好",
  better: "较好",
  normal: "普通",
  low: "低质",
};

type DialogKind = "signin" | "poll" | "demo" | "transfers" | "helps" | "notify" | "url" | "launch" | "collect" | null;

export function ClassroomView() {
  const devices = useLabStore((store) => store.devices);
  const selectedIds = useLabStore((store) => store.selectedIds);
  const toggleSelected = useLabStore((store) => store.toggleSelected);
  const clearSelection = useLabStore((store) => store.clearSelection);
  const classroom = useLabStore((store) => store.classroom);

  // 待审批设备可见但不算可操作学生机（不能选也不能被广播）。
  const online = useMemo(() => devices.filter((device) => device.online && !device.pending), [devices]);
  const onlineKey = online.map((device) => device.id).join(",");
  const onlineIds = useMemo(() => online.map((device) => device.id), [online]);
  const targets = useMemo(() => targetDeviceIds(devices, selectedIds), [devices, selectedIds]);
  const frames = useMemo(
    () => new Map((classroom?.screens ?? []).map((frame) => [frame.deviceId, frame])),
    [classroom?.screens],
  );
  const watchIds = classroom?.screenWatchIds ?? [];
  const watching = watchIds.length > 0;
  const casting = Boolean(classroom?.castMode);
  const castTargets = classroom?.castTargetIds ?? [];

  const anchorRef = useRef("");
  const dialogOpenRef = useRef(false);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [clock, setClock] = useState(Date.now());
  const [watchEnabled, setWatchEnabled] = useState(true);
  const [dialog, setDialogState] = useState<DialogKind>(null);
  const setDialog = (kind: DialogKind) => {
    dialogOpenRef.current = kind !== null;
    setDialogState(kind);
  };
  const [detailsOpen, setDetailsOpen] = useState(false);

  useEffect(() => {
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  // 屏幕墙默认常开：在线设备变化时幂等地保持监看（除非手动关）。
  useEffect(() => {
    if (!watchEnabled || !onlineIds.length) return;
    void api.watchScreens(onlineIds, true).catch(() => undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [onlineKey, watchEnabled]);
  // 离开课堂页时停看。
  useEffect(() => () => {
    void api.watchScreens(useLabStore.getState().devices.filter((d) => d.online).map((d) => d.id), false).catch(() => undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const toggleWatch = async () => {
    try {
      if (watching) {
        setWatchEnabled(false);
        await api.watchScreens(watchIds, false);
      } else {
        setWatchEnabled(true);
        await api.watchScreens(onlineIds, true);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "屏幕监看操作失败");
    }
  };

  // ---- 选择语义：点选 / Ctrl·Cmd 多选 / Shift 范围 / Ctrl·Cmd+A 全选 / Esc 清空 ----
  const clickDevice = (event: React.MouseEvent, id: string) => {
    if (event.metaKey || event.ctrlKey) {
      toggleSelected(id);
      anchorRef.current = id;
      return;
    }
    if (event.shiftKey && anchorRef.current) {
      const order = onlineIds;
      const a = order.indexOf(anchorRef.current);
      const b = order.indexOf(id);
      if (a >= 0 && b >= 0) {
        const [from, to] = a < b ? [a, b] : [b, a];
        useLabStore.setState({ selectedIds: order.slice(from, to + 1) });
        return;
      }
    }
    useLabStore.setState({ selectedIds: [id] });
    anchorRef.current = id;
  };

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const el = event.target as HTMLElement | null;
      const editing = el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.isContentEditable);
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "a" && !editing) {
        event.preventDefault();
        useLabStore.setState({ selectedIds: [...onlineIds] });
      } else if (event.key === "Escape" && !editing && !event.defaultPrevented && !dialogOpenRef.current) {
        clearSelection();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onlineIds, clearSelection]);

  // ---- 快捷操作 ----
  const [mode, setMode] = useState("fullscreen");
  const castAction = async () => {
    try {
      if (casting) {
        await api.castStop();
        toast.success("已停止屏幕广播");
        return;
      }
      if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
      const ok = await api.castStart(mode, targets);
      if (!ok) toast.error("广播未启动：检查设备/演示状态");
      else toast.success(`已向 ${targets.length} 台设备发起广播`);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "屏幕广播操作失败");
    }
  };
  const switchCastMode = async (next: string) => {
    setMode(next);
    if (casting && next !== classroom?.castMode) {
      try { await api.castStart(next, castTargets); }
      catch (error) { toast.error(error instanceof Error ? error.message : "切换广播模式失败"); }
    }
  };

  const pickAndSend = async () => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    let paths: string[] = [];
    if (nativeRuntime) {
      try { paths = await api.pickFiles(); }
      catch (error) { toast.error(error instanceof Error ? error.message : "打开文件选择器失败"); return; }
    } else {
      fileInputRef.current?.click();
      return;
    }
    await sendPaths(paths);
  };
  const sendPaths = async (paths: string[]) => {
    if (!paths.length) return;
    try {
      const queued = await api.sendFiles(targets, paths);
      if (!queued) toast.error("未能启动文件传输");
      else toast.success(`已向 ${targets.length} 台设备分发 ${paths.length} 个文件`);
      setDetailsOpen(true);
    } catch (error) { toast.error(error instanceof Error ? error.message : "文件分发失败"); }
  };

  const collect = async (dir: string, glob: string) => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const sent = await api.collectFiles(targets, dir, glob);
      if (sent < 0) {
        toast.error("目录不在允许范围（仅允许 C:\\Users\\Public 与 C:\\LabHelper 之下）");
        return;
      }
      toast.success(sent
        ? dir ? `已向 ${sent} 台设备发起目录收取` : `已向 ${sent} 台设备发出收取请求`
        : "没有设备接受收取请求");
    } catch (error) { toast.error(error instanceof Error ? error.message : "收取文件失败"); }
  };

  const screenLock = async (on: boolean) => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const n = await api.screenLock(targets, on);
      toast.success(on ? `已锁定 ${n} 台学生机` : `已解锁 ${n} 台学生机`);
    } catch (error) { toast.error(error instanceof Error ? error.message : "锁屏操作失败"); }
  };

  // action: restart|shutdown|restart-force|shutdown-force|cancel
  const power = async (action: string) => {
    if (action === "cancel") {
      try {
        const n = await api.powerCancel(targets);
        toast.success(`已向 ${n} 台设备取消关机/重启倒计时`);
      } catch (error) { toast.error(error instanceof Error ? error.message : "取消失败"); }
      return;
    }
    const force = action.endsWith("-force");
    const base = force ? action.replace("-force", "") : action;
    if (base !== "restart" && base !== "shutdown") return;
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    const label = `${force ? "强制" : ""}${base === "restart" ? "重启" : "关机"}`;
    const hint = force
      ? `确认【强制】${label} ${targets.length} 台学生机？未保存的学生工作将丢失。`
      : `确认${label} ${targets.length} 台学生机？（60 秒倒计时，可取消）`;
    if (nativeRuntime && !window.confirm(hint)) return;
    try {
      const n = await api.powerAction(targets, base, force);
      toast.success(`已向 ${n} 台设备发出${label}指令`);
    } catch (error) { toast.error(error instanceof Error ? error.message : `远程${label}失败`); }
  };

  // 双击学生机卡片 → 高清单机查看；再点右上角/关闭结束。
  const openView = (id: string) => {
    void api.viewDevice(id, true).catch(() => toast.error("无法打开单机查看"));
  };
  const focusId = classroom?.focusId ?? "";
  const focusScreen = classroom?.focusScreen;

  // ---- 座位布局：拖拽设备卡到真实机房座位，行列可批量选择 ----
  const [seatMode, setSeatMode] = useState(false);
  const [seats, setSeats] = useState<Record<string, number[]>>({});
  const [seatCols, setSeatCols] = useState(8);
  useEffect(() => {
    void api.seats().then((s) => setSeats(s)).catch(() => undefined);
    void api.seatCols().then((c) => setSeatCols(c || 8)).catch(() => undefined);
  }, []);
  const saveSeat = (next: Record<string, number[]>) => {
    setSeats(next);
    void api.saveSeats(next).catch(() => toast.error("座位保存失败"));
  };
  const seatAt = (r: number, c: number) => devices.find((d) => seats[d.id]?.[0] === r && seats[d.id]?.[1] === c);
  const selectSeatRange = (kind: "row" | "col", n: number) => {
    const ids = Object.entries(seats)
      .filter(([id, rc]) => (kind === "row" ? rc[0] === n : rc[1] === n) && devices.some((d) => d.id === id && d.online && !d.pending))
      .map(([id]) => id);
    useLabStore.setState({ selectedIds: [...new Set([...useLabStore.getState().selectedIds, ...ids])] });
  };
  const seatRows = useMemo(() => {
    let max = 5;
    for (const rc of Object.values(seats)) if ((rc[0] ?? 0) + 1 > max) max = rc[0]! + 1;
    return max + 1; // 永远多给一行空位可拖
  }, [seats]);

  const selectedOnline = devices.filter((d) => selectedIds.includes(d.id) && d.online).length;
  const activeTransfers = (classroom?.files ?? []).filter((t) => !t.done && !t.err).length;
  const helps = classroom?.helps ?? [];

  // 学生机卡片：普通宫格与座位格共用。双击开高清单机查看。
  const renderCard = (device: (typeof devices)[number], compact = false) => {
    const frame = frames.get(device.id);
    const selected = selectedIds.includes(device.id);
    const age = frame ? Math.max(0, clock - frame.at) : Number.POSITIVE_INFINITY;
    const usable = Boolean(frame && age <= 30000);
    return (
      <article
        key={device.id}
        draggable={seatMode}
        onDragStart={seatMode ? (e) => { e.dataTransfer.setData("text/plain", device.id); e.dataTransfer.effectAllowed = "move"; } : undefined}
        onClick={(event) => clickDevice(event, device.id)}
        onDoubleClick={() => { if (device.online && !device.pending) openView(device.id); }}
        className={`cursor-pointer overflow-hidden rounded-lg border bg-card select-none transition-shadow ${
          selected ? "border-primary ring-2 ring-primary/40" : "hover:border-foreground/20"
        } ${device.online ? "" : "opacity-60"}`}
      >
        <div className={`relative bg-muted/60 ${compact ? "aspect-[16/10]" : "aspect-video"}`}>
          {usable ? (
            <img className="size-full object-cover" src={`data:image/jpeg;base64,${frame!.jpeg}`} alt={`${device.label} 屏幕`} />
          ) : (
            <div className="grid size-full place-items-center text-xs text-muted-foreground">
              {device.online ? (frame ? "画面过期" : "等待画面…") : "离线"}
            </div>
          )}
          <span className={`absolute top-1.5 left-1.5 grid size-5 place-items-center rounded border text-[10px] ${
            selected ? "border-primary bg-primary text-primary-foreground" : "border-foreground/20 bg-background/80 text-transparent"
          }`}><Check className="size-3" /></span>
          <span className="absolute top-1.5 right-1.5 flex gap-1">
            {!device.online && <Badge variant="secondary">离线</Badge>}
            {device.pending && <Badge variant="outline" className="border-warning/50 bg-warning/10 text-warning">待审批</Badge>}
            {device.locked && <Badge variant="destructive"><Lock className="size-3" /></Badge>}
            {device.castTcp && casting && <Badge variant="outline" className="border-info/50 text-info">TCP</Badge>}
            {(device.featErrs?.length ?? 0) > 0 && <Badge variant="outline" className="border-warning/50 text-warning">策略异常</Badge>}
          </span>
        </div>
        <div className="flex items-center gap-2 px-2.5 py-1.5">
          <span className={`size-1.5 shrink-0 rounded-full ${device.online ? "bg-success" : "bg-muted-foreground/50"}`} />
          <div className="min-w-0 flex-1">
            <p className="truncate text-xs font-medium">{device.label} · {device.name}</p>
            <p className="truncate text-[11px] text-muted-foreground">{device.ip}</p>
          </div>
          {device.alerts > 0 && <Badge variant="destructive" className="px-1.5">{device.alerts}</Badge>}
        </div>
      </article>
    );
  };

  return (
    <div className="space-y-3">
      <input ref={fileInputRef} type="file" multiple className="hidden"
        onChange={(e) => { const paths = Array.from(e.currentTarget.files ?? []).map((f) => `mock://${f.name}`); e.currentTarget.value = ""; void sendPaths(paths); }} />

      {/* 极域式快捷工具条 */}
      <div className="sticky top-0 z-20 -mx-1 flex flex-wrap items-center gap-1.5 rounded-lg border bg-card/95 px-2 py-1.5 backdrop-blur">
        <Button size="sm" variant={casting ? "destructive" : "default"} onClick={() => void castAction()}>
          {casting ? <Square /> : <ScreenShare />}{casting ? "停止广播" : "广播"}
        </Button>
        <Select value={casting ? classroom!.castMode : mode} onValueChange={(v) => typeof v === "string" && void switchCastMode(v)}>
          <SelectTrigger className="h-8 w-[104px]"><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="fullscreen">全屏广播</SelectItem>
            <SelectItem value="window">窗口广播</SelectItem>
          </SelectContent>
        </Select>
        {casting && (
          <Select value={classroom?.castQuality ?? "better"} onValueChange={(v) => typeof v === "string" && void api.castSetQuality(v).catch(() => undefined)}>
            <SelectTrigger className="h-8 w-[86px]"><SelectValue /></SelectTrigger>
            <SelectContent>
              {Object.entries(qualityLabels).map(([k, label]) => <SelectItem key={k} value={k}>{label}</SelectItem>)}
            </SelectContent>
          </Select>
        )}
        <Button size="sm" variant="outline" onClick={() => setDialog("demo")}><Video />学生演示</Button>
        <Separator orientation="vertical" className="mx-1 h-5" />
        <Button size="sm" variant="outline" onClick={() => void pickAndSend()}><Send />分发文件</Button>
        <Button size="sm" variant="outline" onClick={() => setDialog("collect")}><Download />收取文件</Button>
        <Separator orientation="vertical" className="mx-1 h-5" />
        <Button size="sm" variant="outline" onClick={() => setDialog("notify")}><Send />发消息</Button>
        <Button size="sm" variant="outline" onClick={() => setDialog("url")}><ExternalLink />推网址</Button>
        <Button size="sm" variant="outline" onClick={() => setDialog("launch")}><Rocket />运行程序</Button>
        <Separator orientation="vertical" className="mx-1 h-5" />
        <Button size="sm" variant="outline" onClick={() => setDialog("signin")}><ClipboardCheck />签到</Button>
        <Button size="sm" variant="outline" onClick={() => setDialog("poll")}><MessageSquareText />投票</Button>
        <Button size="sm" variant="outline" onClick={() => void toggleWatch()}>
          {watching ? <MonitorOff /> : <Monitor />}{watching ? "停止监看" : "开始监看"}
        </Button>
        <Separator orientation="vertical" className="mx-1 h-5" />
        <Button size="sm" variant="outline" onClick={() => void screenLock(true)}><Lock />锁屏</Button>
        <Button size="sm" variant="ghost" onClick={() => void screenLock(false)}><LockOpen />解锁</Button>
        <Select value="" onValueChange={(v) => typeof v === "string" && void power(v)}>
          <SelectTrigger className="h-8 w-[88px]"><Power className="size-3.5" /><SelectValue placeholder="电源" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="restart">远程重启（60s）</SelectItem>
            <SelectItem value="shutdown">远程关机（60s）</SelectItem>
            <SelectItem value="cancel">取消关机/重启</SelectItem>
            <SelectItem value="restart-force" className="text-destructive">强制重启（危险）</SelectItem>
            <SelectItem value="shutdown-force" className="text-destructive">强制关机（危险）</SelectItem>
          </SelectContent>
        </Select>
        <Button size="sm" variant={seatMode ? "secondary" : "ghost"} onClick={() => setSeatMode((v) => !v)} title="按真实机房座位排列">
          <LayoutGrid />{seatMode ? "退出座位" : "座位图"}
        </Button>
        {seatMode && (
          <Select value={String(seatCols)} onValueChange={(v) => {
            const n = Number(v);
            setSeatCols(n);
            void api.setSeatCols(n).catch(() => undefined);
          }}>
            <SelectTrigger className="h-8 w-[72px]"><SelectValue /></SelectTrigger>
            <SelectContent>
              {[5, 6, 7, 8, 9, 10, 12].map((n) => <SelectItem key={n} value={String(n)}>{n} 列</SelectItem>)}
            </SelectContent>
          </Select>
        )}
        <div className="ml-auto flex items-center gap-1.5">
          {helps.length > 0 && (
            <Button size="sm" variant="outline" className="border-warning/40 text-warning" onClick={() => setDialog("helps")}>
              <HelpCircle />{helps.length} 个求助
            </Button>
          )}
          <Badge variant="secondary" className="tabular-nums"><Users className="size-3" />已选 {selectedIds.length ? `${selectedOnline}/${selectedIds.length}` : `0/${online.length}`}</Badge>
          <Button size="sm" variant="ghost" onClick={() => useLabStore.setState({ selectedIds: [...onlineIds] })}>全选</Button>
          <Button size="sm" variant="ghost" onClick={clearSelection} disabled={!selectedIds.length}>清空</Button>
        </div>
      </div>

      {/* 学生机宫格（含预览画面）/ 座位布局 */}
      {devices.length ? (
        seatMode ? (
          <SeatGrid
            devices={devices} seats={seats} seatCols={seatCols} seatRows={seatRows}
            frames={frames} selectedIds={selectedIds} clock={clock}
            onCardClick={clickDevice} onCardDoubleClick={openView}
            onDropSeat={(id, r, c) => {
              // 占位交换：拖到已占用格子时两台设备互换座位；
              // 拖到空位只是移动。否则两台同坐标只有一台可见。
              const occupant = Object.entries(seats).find(
                ([oid, rc]) => oid !== id && rc[0] === r && rc[1] === c,
              );
              const next = { ...seats, [id]: [r, c] };
              if (occupant) {
                const from = seats[id];
                if (from) next[occupant[0]] = from;
                else delete next[occupant[0]];
              }
              saveSeat(next);
            }}
            onUnseat={(id) => {
              const next = { ...seats };
              delete next[id];
              saveSeat(next);
            }}
            onPickRange={selectSeatRange}
            renderCard={renderCard}
          />
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-5">
            {devices.map((device) => renderCard(device))}
          </div>
        )
      ) : (
        <Empty className="min-h-40 border bg-muted/20"><EmptyHeader><EmptyTitle>还没有学生机</EmptyTitle><EmptyDescription>客户端上线后会显示在这里。</EmptyDescription></EmptyHeader></Empty>
      )}

      {/* 底部活动条：广播/演示/签到/投票/传输/求助 摘要 */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border bg-card px-3 py-2 text-xs text-muted-foreground">
        <span className={casting ? "font-medium text-success" : ""}>
          {casting ? `● 广播中 · ${castTargets.length} 台 · ${classroom?.castMode === "fullscreen" ? "全屏" : `窗口${classroom?.castTitle ? `：${classroom.castTitle}` : ""}`} · ${qualityLabels[classroom?.castQuality ?? "better"] ?? ""}` : "未广播"}
        </span>
        {classroom?.demo?.state ? <span>学生演示：{classroom.demo.state === "pending" ? "等待确认" : `${classroom.demo.sourceLabel} 演示中`}</span> : null}
        {classroom?.signIn?.id ? <button className="hover:text-foreground" onClick={() => setDialog("signin")}>签到 {classroom.signIn.replies.length}/{classroom.signIn.sent}{classroom.signIn.open ? " 进行中" : ""}</button> : null}
        {classroom?.poll?.id ? <button className="hover:text-foreground" onClick={() => setDialog("poll")}>投票 {classroom.poll.replies.length}/{classroom.poll.sent}{classroom.poll.open ? " 进行中" : ""}</button> : null}
        {(classroom?.files ?? []).length > 0 ? <button className="hover:text-foreground" onClick={() => setDialog("transfers")}>文件传输 {activeTransfers ? `${activeTransfers} 进行中` : `${classroom!.files.length} 项`}</button> : null}
        <span className="flex-1" />
        <span>目标：{selectedIds.length ? `已选 ${targets.length} 台` : `全部 ${targets.length} 台`}</span>
        <button className="text-foreground/70 underline-offset-2 hover:underline" onClick={() => setDetailsOpen((v) => !v)}>{detailsOpen ? "收起详情" : "展开详情"}</button>
      </div>

      {detailsOpen && <ActivityPanel clock={clock} onOpenDialog={setDialog} />}

      <SignInDialog open={dialog === "signin"} onClose={() => setDialog(null)} targets={targets} clock={clock} />
      <PollDialog open={dialog === "poll"} onClose={() => setDialog(null)} targets={targets} clock={clock} />
      <DemoDialog open={dialog === "demo"} onClose={() => setDialog(null)} targets={targets} />
      <TransfersDialog open={dialog === "transfers"} onClose={() => setDialog(null)} />
      <HelpsDialog open={dialog === "helps"} onClose={() => setDialog(null)} />
      <NotifyDialog open={dialog === "notify"} onClose={() => setDialog(null)} targets={targets} />
      <URLDialog open={dialog === "url"} onClose={() => setDialog(null)} targets={targets} />
      <LaunchDialog open={dialog === "launch"} onClose={() => setDialog(null)} targets={targets} />
      <CollectDialog open={dialog === "collect"} onClose={() => setDialog(null)} onSubmit={collect} />
      <FocusDialog
        deviceId={focusId}
        screen={focusScreen ?? null}
        onClose={() => { if (focusId) void api.viewDevice(focusId, false).catch(() => undefined); }}
      />
    </div>
  );
}

// ---- 底部详情面板（复用原各面板的数据展示） ----

function ActivityPanel({ clock, onOpenDialog }: { clock: number; onOpenDialog: (d: DialogKind) => void }) {
  const classroom = useLabStore((store) => store.classroom);
  const signIn = classroom?.signIn;
  const poll = classroom?.poll;
  const files = classroom?.files ?? [];
  const helps = classroom?.helps ?? [];
  const pollRemaining = poll?.deadline ? Math.max(0, poll.deadline - Math.floor(clock / 1000)) : 0;
  return (
    <div className="grid gap-3 rounded-lg border bg-card p-3 lg:grid-cols-2 2xl:grid-cols-4">
      <section className="space-y-2">
        <div className="flex items-center justify-between"><h3 className="text-xs font-semibold">签到记录</h3><button className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onOpenDialog("signin")}>管理</button></div>
        {signIn?.replies.length ? <div className="max-h-44 divide-y overflow-y-auto rounded-md border text-xs">{signIn.replies.map((r) => <div key={`${r.deviceId}-${r.at}`} className="flex items-center gap-2 px-2 py-1.5"><Badge variant="secondary">{r.label}</Badge><span className="flex-1 truncate">{r.name || "已签到"}</span><span className="text-muted-foreground">{formatClock(r.at)}</span></div>)}</div> : <p className="rounded-md bg-muted/30 px-2 py-3 text-center text-xs text-muted-foreground">暂无签到记录</p>}
      </section>
      <section className="space-y-2">
        <div className="flex items-center justify-between"><h3 className="text-xs font-semibold">投票统计</h3><button className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onOpenDialog("poll")}>管理</button></div>
        {poll?.id ? <PollResults poll={poll} remaining={pollRemaining} compact /> : <p className="rounded-md bg-muted/30 px-2 py-3 text-center text-xs text-muted-foreground">尚未发起投票</p>}
      </section>
      <section className="space-y-2">
        <div className="flex items-center justify-between"><h3 className="text-xs font-semibold">学生求助</h3><button className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onOpenDialog("helps")}>处理</button></div>
        <HelpList helps={helps} />
      </section>
      <section className="space-y-2">
        <div className="flex items-center justify-between"><h3 className="text-xs font-semibold">文件传输</h3><button className="text-xs text-muted-foreground hover:text-foreground" onClick={() => onOpenDialog("transfers")}>详情</button></div>
        <TransferList files={files.slice(0, 12)} />
      </section>
    </div>
  );
}

function PollResults({ poll, remaining, compact = false }: { poll: NonNullable<ReturnType<typeof useLabStore.getState>["classroom"]>["poll"]; remaining: number; compact?: boolean }) {
  const counts = poll.options.map((option) => poll.replies.filter((reply) => reply.option === option).length);
  return (
    <div className={`space-y-2 rounded-md border bg-muted/10 ${compact ? "p-2" : "p-3"}`}>
      <div className="flex items-start justify-between gap-2">
        <div><p className="text-xs font-medium">{poll.question}</p><p className="mt-0.5 text-[11px] text-muted-foreground">{poll.replies.length} 人作答{poll.open ? ` · 剩余 ${remaining} 秒` : ""}</p></div>
        <Badge variant={poll.open ? "outline" : "secondary"} className={poll.open ? "border-success/25 text-success" : ""}>{poll.open ? "进行中" : "已结束"}</Badge>
      </div>
      {poll.options.map((option, index) => {
        const count = counts[index] ?? 0;
        const percent = poll.replies.length ? Math.round((count * 100) / poll.replies.length) : 0;
        return (
          <div key={`${option}-${index}`} className="space-y-1">
            <div className="flex justify-between gap-2 text-[11px]"><span>{option}</span><span className="tabular-nums text-muted-foreground">{count} 人 · {percent}%</span></div>
            <Progress value={percent} className="h-1.5" />
          </div>
        );
      })}
      <p className="text-[11px] text-muted-foreground">未回复 {Math.max(0, poll.sent - poll.replies.length)} 台</p>
    </div>
  );
}

function HelpList({ helps }: { helps: { deviceId: string; label: string; name: string; at: string }[] }) {
  const clearHelp = async (deviceId: string) => {
    try { await api.clearHelp(deviceId); toast.success("已处理求助提醒"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "处理求助失败"); }
  };
  if (!helps.length) return <p className="rounded-md bg-muted/30 px-2 py-3 text-center text-xs text-muted-foreground">暂无待处理请求</p>;
  return (
    <div className="max-h-44 divide-y overflow-y-auto rounded-md border text-xs">
      {helps.map((request) => (
        <div key={`${request.deviceId}-${request.at}`} className="flex items-center gap-2 px-2 py-1.5">
          <Badge variant="outline">{request.label}</Badge>
          <span className="min-w-0 flex-1 truncate">{request.name}</span>
          <span className="text-muted-foreground">{formatClock(request.at)}</span>
          <Button size="xs" variant="ghost" onClick={() => void clearHelp(request.deviceId)}><CheckCheck />已处理</Button>
        </div>
      ))}
    </div>
  );
}

function TransferList({ files }: { files: { id: string; label: string; name: string; size: number; sent: number; done: boolean; in: boolean; err?: string }[] }) {
  if (!files.length) return <p className="rounded-md bg-muted/30 px-2 py-3 text-center text-xs text-muted-foreground">暂无文件传输</p>;
  return (
    <div className="max-h-44 divide-y overflow-y-auto rounded-md border text-xs">
      {files.map((item) => {
        const percent = item.size > 0 ? Math.min(100, Math.round((item.sent * 100) / item.size)) : item.done ? 100 : 0;
        return (
          <div key={item.id} className="space-y-1 px-2 py-1.5">
            <div className="flex items-center gap-1.5"><Badge variant="outline">{item.label}</Badge><span className="min-w-0 flex-1 truncate">{item.name}</span><span className="text-muted-foreground">{item.in ? "收取" : "分发"}</span></div>
            <div className="flex items-center gap-2"><Progress value={percent} className="h-1.5 flex-1" /><span className="w-9 text-right tabular-nums text-muted-foreground">{percent}%</span></div>
            {item.err ? <p className="text-[11px] text-destructive">失败：{item.err}</p> : null}
          </div>
        );
      })}
    </div>
  );
}

// ---- 对话框 ----

function SignInDialog({ open, onClose, targets, clock }: { open: boolean; onClose: () => void; targets: string[]; clock: number }) {
  const signIn = useLabStore((store) => store.classroom?.signIn);
  const [duration, setDuration] = useState("0");
  const remaining = signIn?.deadline ? Math.max(0, signIn.deadline - Math.floor(clock / 1000)) : 0;
  const start = async () => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try { const count = await api.signInStart(Number(duration), targets); toast.success(`已向 ${count} 台设备发起签到`); }
    catch (error) { toast.error(error instanceof Error ? error.message : "发起签到失败"); }
  };
  const stop = async () => {
    try { await api.signInStop(); toast.success("签到已结束"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "结束签到失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>课堂签到</DialogTitle><DialogDescription>向当前目标学生发送签到请求。</DialogDescription></DialogHeader>
        <div className="flex items-end gap-2">
          <label className="grid min-w-32 gap-1.5 text-xs font-medium">截止时间
            <Select value={duration} onValueChange={(v) => typeof v === "string" && setDuration(v)}>
              <SelectTrigger className="h-8"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="0">不限时</SelectItem><SelectItem value="60">1 分钟</SelectItem><SelectItem value="180">3 分钟</SelectItem><SelectItem value="300">5 分钟</SelectItem></SelectContent>
            </Select>
          </label>
          <Button size="sm" onClick={() => void start()} disabled={signIn?.open || !targets.length}><Play />发起</Button>
          <Button size="sm" variant="outline" onClick={() => void stop()} disabled={!signIn?.open}><StopCircle />结束</Button>
        </div>
        {signIn?.id ? (
          <div className="space-y-2">
            <div className="flex items-center justify-between rounded-md bg-muted/40 px-2.5 py-1.5 text-xs">
              <span>已签到 {signIn.replies.length}/{signIn.sent} 台</span>
              {signIn.open && signIn.deadline > 0 ? <span className="text-muted-foreground">剩余 {remaining} 秒</span> : null}
            </div>
            {signIn.replies.length ? <div className="max-h-56 divide-y overflow-y-auto rounded-md border text-xs">{signIn.replies.map((r) => <div key={`${r.deviceId}-${r.at}`} className="flex items-center gap-2 px-2 py-1.5"><Badge variant="secondary">{r.label}</Badge><span className="flex-1 truncate">{r.name || "已签到"}</span><span className="text-muted-foreground">{formatClock(r.at)}</span></div>)}</div> : null}
          </div>
        ) : null}
        <DialogFooter><p className="text-xs text-muted-foreground">学生端会收到气泡提醒并在互动页确认。</p></DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function PollDialog({ open, onClose, targets, clock }: { open: boolean; onClose: () => void; targets: string[]; clock: number }) {
  const poll = useLabStore((store) => store.classroom?.poll);
  const [question, setQuestion] = useState("");
  const [optionText, setOptionText] = useState("选项 A\n选项 B");
  const [duration, setDuration] = useState("60");
  const options = optionText.split("\n").map((v) => v.trim()).filter(Boolean);
  const remaining = poll?.deadline ? Math.max(0, poll.deadline - Math.floor(clock / 1000)) : 0;
  const start = async () => {
    if (!question.trim()) { toast.error("请填写投票问题"); return; }
    if (options.length < 2 || options.length > 6) { toast.error("投票选项需要为 2–6 个"); return; }
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try { const count = await api.pollStart(question.trim(), options, Number(duration), targets); toast.success(`已向 ${count} 台设备发布投票`); }
    catch (error) { toast.error(error instanceof Error ? error.message : "发布投票失败"); }
  };
  const stop = async () => {
    try { await api.pollStop(); toast.success("投票已结束"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "结束投票失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>随堂投票</DialogTitle><DialogDescription>发布单题投票并查看实时统计。</DialogDescription></DialogHeader>
        {!poll?.open && (
          <div className="space-y-3">
            <label className="grid gap-1.5 text-xs font-medium">问题<Input value={question} onChange={(e) => setQuestion(e.target.value.slice(0, 240))} placeholder="例如：这一步是否已经完成？" maxLength={240} /></label>
            <label className="grid gap-1.5 text-xs font-medium">选项 <span className="font-normal text-muted-foreground">每行一个，2–6 项</span><Textarea value={optionText} onChange={(e) => setOptionText(e.target.value)} rows={3} /></label>
            <div className="flex items-end gap-2">
              <label className="grid min-w-28 gap-1.5 text-xs font-medium">时长
                <Select value={duration} onValueChange={(v) => typeof v === "string" && setDuration(v)}>
                  <SelectTrigger className="h-8"><SelectValue /></SelectTrigger>
                  <SelectContent><SelectItem value="30">30 秒</SelectItem><SelectItem value="60">1 分钟</SelectItem><SelectItem value="180">3 分钟</SelectItem></SelectContent>
                </Select>
              </label>
              <Button size="sm" onClick={() => void start()}><Send />发布投票</Button>
            </div>
          </div>
        )}
        {poll?.id ? <PollResults poll={poll} remaining={remaining} /> : null}
        {poll?.open ? <Button size="sm" variant="outline" onClick={() => void stop()}><StopCircle />结束投票</Button> : null}
      </DialogContent>
    </Dialog>
  );
}

function DemoDialog({ open, onClose, targets }: { open: boolean; onClose: () => void; targets: string[] }) {
  const devices = useLabStore((store) => store.devices);
  const selectedIds = useLabStore((store) => store.selectedIds);
  const demo = useLabStore((store) => store.classroom?.demo);
  const sourceFromStore = useLabStore((store) => store.demoSourceId);
  const setDemoSource = useLabStore((store) => store.setDemoSource);
  const online = devices.filter((d) => d.online);
  const demoActive = Boolean(demo?.state);
  const fallback = selectedIds.find((id) => online.some((d) => d.id === id)) ?? online[0]?.id ?? "";
  const source = online.some((d) => d.id === sourceFromStore) ? sourceFromStore : fallback;
  const receivers = demoActive ? (demo?.targetIds ?? []) : targets.filter((id) => id !== source);
  const start = async () => {
    if (!source) { toast.error("没有可用的在线演示来源"); return; }
    if (!receivers.length) { toast.error("没有接收设备（来源设备不计入）"); return; }
    try {
      const ok = await api.demoStart(source, receivers);
      if (ok) toast.success("演示请求已发送，等待学生确认");
      else toast.error("演示请求未发送");
    } catch (error) { toast.error(error instanceof Error ? error.message : "发起学生演示失败"); }
  };
  const stop = async () => {
    try { await api.demoStop(); toast.success("学生演示已停止"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "停止学生演示失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>学生演示</DialogTitle><DialogDescription>学生确认后，将其屏幕转发给其他目标设备。</DialogDescription></DialogHeader>
        <label className="grid gap-1.5 text-xs font-medium">演示来源
          <Select value={source} onValueChange={(v) => typeof v === "string" && setDemoSource(v)} disabled={demoActive}>
            <SelectTrigger className="h-8"><SelectValue placeholder="选择学生设备" /></SelectTrigger>
            <SelectContent>{online.map((d) => <SelectItem key={d.id} value={d.id}>{d.label} · {d.name}</SelectItem>)}</SelectContent>
          </Select>
        </label>
        <div className="flex items-center gap-2">
          <Button size="sm" onClick={() => void start()} disabled={demoActive || !source}><Play />请求演示</Button>
          <Button size="sm" variant="outline" onClick={() => void stop()} disabled={!demoActive}><StopCircle />停止</Button>
          <Badge variant={demo?.state === "active" ? "outline" : "secondary"} className={demo?.state === "active" ? "border-success/25 text-success" : ""}>
            {demo?.state === "pending" ? "等待学生确认" : demo?.state === "active" ? `演示中 · ${demo.sourceLabel}` : demo?.err ? `失败：${demo.err}` : "未进行"}
          </Badge>
        </div>
        <p className="text-xs text-muted-foreground">接收设备：{receivers.length} 台（当前目标中排除演示来源）</p>
      </DialogContent>
    </Dialog>
  );
}

function TransfersDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const files = useLabStore((store) => store.classroom?.files) ?? [];
  const openReceived = async () => {
    try { const path = await api.openRecvDir(); if (path) toast.info(`接收目录：${path}`); }
    catch (error) { toast.error(error instanceof Error ? error.message : "打开接收目录失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader><DialogTitle>文件传输</DialogTitle><DialogDescription>分发与收取的实时进度。</DialogDescription></DialogHeader>
        <div className="max-h-80 overflow-y-auto"><TransferList files={files} /></div>
        <DialogFooter>
          <Button size="sm" variant="outline" onClick={() => void openReceived()}><ArrowDownToLine />打开接收目录</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function HelpsDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const helps = useLabStore((store) => store.classroom?.helps) ?? [];
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle className="flex items-center gap-2"><HelpCircle className="size-4 text-warning" />学生求助</DialogTitle><DialogDescription>学生端举手请求。</DialogDescription></DialogHeader>
        <HelpList helps={helps} />
      </DialogContent>
    </Dialog>
  );
}

// ---- 新增：消息 / 网址 / 程序 / 收取 / 单机大图 / 座位布局 ----

function NotifyDialog({ open, onClose, targets }: { open: boolean; onClose: () => void; targets: string[] }) {
  const [text, setText] = useState("");
  const send = async () => {
    if (!text.trim()) { toast.error("请填写消息内容"); return; }
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const n = await api.notifyText(targets, text.trim());
      toast.success(`消息已送达 ${n} 台学生机`);
      setText("");
      onClose();
    } catch (error) { toast.error(error instanceof Error ? error.message : "发送失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>发送消息</DialogTitle><DialogDescription>消息以气泡通知出现在学生机右下角（不弹窗、不打断操作）。</DialogDescription></DialogHeader>
        <Textarea value={text} onChange={(e) => setText(e.target.value)} rows={3} maxLength={200}
          placeholder="例如：请把今天的作业保存到桌面后提交。" />
        <DialogFooter>
          <p className="mr-auto text-xs text-muted-foreground">目标：{targets.length} 台 · {text.length}/200</p>
          <Button size="sm" onClick={() => void send()} disabled={!targets.length || !text.trim()}><Send />发送</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function URLDialog({ open, onClose, targets }: { open: boolean; onClose: () => void; targets: string[] }) {
  const [url, setURL] = useState("");
  const send = async () => {
    const u = url.trim();
    if (!/^https?:\/\//i.test(u)) { toast.error("只允许 http:// 或 https:// 网址"); return; }
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const n = await api.openURLFor(targets, u);
      if (!n) toast.error("没有设备接受该网址（格式无效或全部离线）");
      else { toast.success(`已在 ${n} 台学生机打开网址`); onClose(); }
    } catch (error) { toast.error(error instanceof Error ? error.message : "推送网址失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>推送教学网址</DialogTitle><DialogDescription>所选学生机将用默认浏览器打开该页面。</DialogDescription></DialogHeader>
        <Input value={url} onChange={(e) => setURL(e.target.value)} placeholder="https://example.com/lesson" />
        <DialogFooter>
          <p className="mr-auto text-xs text-muted-foreground">仅支持 http/https；目标 {targets.length} 台</p>
          <Button size="sm" onClick={() => void send()} disabled={!targets.length || !url.trim()}><ExternalLink />推送</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

const launchPresets = [
  { label: "记事本", path: "C:\\Windows\\System32\\notepad.exe" },
  { label: "计算器", path: "C:\\Windows\\System32\\calc.exe" },
  { label: "画图", path: "C:\\Windows\\System32\\mspaint.exe" },
];

function LaunchDialog({ open, onClose, targets }: { open: boolean; onClose: () => void; targets: string[] }) {
  const [path, setPath] = useState(launchPresets[0]!.path);
  const launch = async () => {
    const p = path.trim();
    if (!/^([a-zA-Z]:\\|\\\\).+\.exe$/i.test(p)) { toast.error("需要 .exe 的绝对路径（如 C:\\Windows\\System32\\notepad.exe）"); return; }
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const n = await api.launchAppFor(targets, p);
      if (!n) toast.error("没有设备接受该指令");
      else { toast.success(`已在 ${n} 台学生机启动程序`); onClose(); }
    } catch (error) { toast.error(error instanceof Error ? error.message : "启动程序失败"); }
  };
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>远程启动程序</DialogTitle><DialogDescription>在所选学生机上启动一个 .exe（不经过命令行，参数不可注入）。</DialogDescription></DialogHeader>
        <div className="flex flex-wrap gap-1.5">
          {launchPresets.map((preset) => (
            <Button key={preset.path} size="xs" variant={path === preset.path ? "secondary" : "outline"} onClick={() => setPath(preset.path)}>{preset.label}</Button>
          ))}
        </div>
        <Input value={path} onChange={(e) => setPath(e.target.value)} placeholder="C:\\Program Files\\App\\app.exe" />
        <DialogFooter>
          <p className="mr-auto text-xs text-muted-foreground">cmd/PowerShell/脚本宿主类程序已被禁启；目标 {targets.length} 台</p>
          <Button size="sm" onClick={() => void launch()} disabled={!targets.length || !path.trim()}><Rocket />启动</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function CollectDialog({ open, onClose, onSubmit }: { open: boolean; onClose: () => void; onSubmit: (dir: string, glob: string) => Promise<void> }) {
  const [mode, setMode] = useState<"manual" | "dir">("manual");
  const [dir, setDir] = useState("C:\\Users\\Public\\Desktop\\作业");
  const [glob, setGlob] = useState("*.*");
  return (
    <Dialog open={open} onOpenChange={(v) => !v && onClose()}>
      <DialogContent>
        <DialogHeader><DialogTitle>收取文件</DialogTitle><DialogDescription>收上来的文件会汇总到管理端接收目录，传输进度在「文件传输」里看。</DialogDescription></DialogHeader>
        <div className="flex gap-2">
          <Button size="sm" variant={mode === "manual" ? "secondary" : "outline"} onClick={() => setMode("manual")}>学生手动提交</Button>
          <Button size="sm" variant={mode === "dir" ? "secondary" : "outline"} onClick={() => setMode("dir")}>指定目录收取</Button>
        </div>
        {mode === "manual" ? (
          <p className="rounded-md bg-muted/30 px-3 py-2 text-xs text-muted-foreground">学生机收到气泡提醒，在互动页里选择文件提交。</p>
        ) : (
          <div className="space-y-2">
            <label className="grid gap-1.5 text-xs font-medium">学生机目录<Input value={dir} onChange={(e) => setDir(e.target.value)} placeholder="C:\\Users\\Public\\Desktop\\作业" /></label>
            <label className="grid gap-1.5 text-xs font-medium">匹配规则<Input value={glob} onChange={(e) => setGlob(e.target.value)} placeholder="*.* 或 *.docx" /></label>
            <p className="text-xs text-muted-foreground">学生无需操作，匹配文件将自动上传（仅允许 C:\Users\Public 与 C:\LabHelper 之下的目录；单机 ≤64 个文件、总量 ≤256MB、单文件 ≤128MB）。结果会逐台显示在「文件传输」。</p>
          </div>
        )}
        <DialogFooter>
          <Button size="sm" onClick={() => { void onSubmit(mode === "dir" ? dir.trim() : "", mode === "dir" ? glob.trim() : ""); onClose(); }}>
            <Download />{mode === "dir" ? "开始收取" : "发出请求"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// FocusDialog 单机高清查看：双击设备卡打开，关闭即停 hi 帧流。
function FocusDialog({ deviceId, screen, onClose }: { deviceId: string; screen: { deviceId: string; label: string; name: string; jpeg: string; at: number } | null; onClose: () => void }) {
  return (
    <Dialog open={Boolean(deviceId)} onOpenChange={(v) => !v && onClose()}>
      <DialogContent className="max-w-5xl">
        <DialogHeader>
          <DialogTitle>{screen ? `${screen.label} · ${screen.name}` : "单机查看"}</DialogTitle>
          <DialogDescription>高清画面 ~1.4fps。远程控制（接管键鼠）将在后续版本提供。</DialogDescription>
        </DialogHeader>
        <div className="relative aspect-video w-full overflow-hidden rounded-md bg-muted/40">
          {screen ? (
            <img className="size-full object-contain" src={`data:image/jpeg;base64,${screen.jpeg}`} alt={`${screen.label} 屏幕`} />
          ) : (
            <div className="grid size-full place-items-center text-sm text-muted-foreground">等待画面…</div>
          )}
        </div>
        <DialogFooter>
          <Button size="sm" variant="outline" onClick={onClose}><Square />结束查看</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

// SeatGrid 座位模式：固定行列宫格，卡片可拖到任意格子，行/列头点击批量选择。
function SeatGrid({
  devices, seats, seatCols, seatRows, frames, selectedIds, clock,
  onCardClick, onCardDoubleClick, onDropSeat, onUnseat, onPickRange, renderCard,
}: {
  devices: Device[];
  seats: Record<string, number[]>;
  seatCols: number; seatRows: number;
  frames: Map<string, { jpeg: string; at: number }>;
  selectedIds: string[]; clock: number;
  onCardClick: (e: React.MouseEvent, id: string) => void;
  onCardDoubleClick: (id: string) => void;
  onDropSeat: (id: string, r: number, c: number) => void;
  onUnseat: (id: string) => void;
  onPickRange: (kind: "row" | "col", n: number) => void;
  renderCard: (d: (typeof devices)[number], compact?: boolean) => React.ReactNode;
}) {
  const seated = new Set(Object.keys(seats));
  const unseated = devices.filter((d) => !seated.has(d.id));
  const cell = (r: number, c: number) => {
    const device = devices.find((d) => seats[d.id]?.[0] === r && seats[d.id]?.[1] === c);
    return (
      <div
        key={`${r}-${c}`}
        className="min-h-20 rounded-md border border-dashed border-foreground/10 bg-muted/10 p-1"
        onDragOver={(e) => { e.preventDefault(); e.dataTransfer.dropEffect = "move"; }}
        onDrop={(e) => {
          e.preventDefault();
          const id = e.dataTransfer.getData("text/plain");
          if (id && devices.some((d) => d.id === id)) onDropSeat(id, r, c);
        }}
      >
        {device ? renderCard(device, true) : <div className="grid h-full place-items-center text-[10px] text-muted-foreground/50">{r + 1}-{c + 1}</div>}
      </div>
    );
  };
  const cols = Array.from({ length: seatCols }, (_, i) => i);
  const rows = Array.from({ length: seatRows }, (_, i) => i);
  return (
    <div className="space-y-2">
      <div className="grid gap-1.5" style={{ gridTemplateColumns: `24px repeat(${seatCols}, minmax(0,1fr))` }}>
        <div />
        {cols.map((c) => (
          <button key={c} className="rounded py-0.5 text-center text-[10px] text-muted-foreground hover:bg-muted" title={`选择第 ${c + 1} 列`}
            onClick={() => onPickRange("col", c)}>列{c + 1}</button>
        ))}
        {rows.map((r) => (
          <Fragment key={r}>
            <button className="rounded px-1 text-center text-[10px] text-muted-foreground hover:bg-muted" title={`选择第 ${r + 1} 行`}
              onClick={() => onPickRange("row", r)}>{r + 1}</button>
            {cols.map((c) => cell(r, c))}
          </Fragment>
        ))}
      </div>
      <div
        className="rounded-md border border-dashed p-2"
        onDragOver={(e) => { e.preventDefault(); e.dataTransfer.dropEffect = "move"; }}
        onDrop={(e) => {
          e.preventDefault();
          const id = e.dataTransfer.getData("text/plain");
          if (id && seated.has(id)) onUnseat(id);
        }}
      >
        <p className="mb-1.5 text-[11px] text-muted-foreground">未排座（拖到此处移出布局）：{unseated.length} 台</p>
        <div className="flex flex-wrap gap-1.5">
          {unseated.map((d) => (
            <span key={d.id} draggable
              onDragStart={(e) => { e.dataTransfer.setData("text/plain", d.id); }}
              onClick={(e) => onCardClick(e, d.id)}
              className={`cursor-pointer rounded border px-2 py-1 text-xs ${selectedIds.includes(d.id) ? "border-primary bg-primary/10" : "bg-muted/40"} ${d.online ? "" : "opacity-50"}`}>
              {d.label}
            </span>
          ))}
        </div>
      </div>
    </div>
  );
}
