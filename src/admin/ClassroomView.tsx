import { useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
  ArrowDownToLine, Check, CheckCheck, ClipboardCheck, Download, HelpCircle,
  MessageSquareText, Monitor, MonitorOff, Play, ScreenShare, Send, Square,
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
import { targetDeviceIds, useLabStore } from "@/store";
import { bytesLabel, formatClock } from "./shared";

const qualityLabels: Record<string, string> = {
  perfect: "极好",
  better: "较好",
  normal: "普通",
  low: "低质",
};

type DialogKind = "signin" | "poll" | "demo" | "transfers" | "helps" | null;

export function ClassroomView() {
  const devices = useLabStore((store) => store.devices);
  const selectedIds = useLabStore((store) => store.selectedIds);
  const toggleSelected = useLabStore((store) => store.toggleSelected);
  const clearSelection = useLabStore((store) => store.clearSelection);
  const classroom = useLabStore((store) => store.classroom);

  const online = useMemo(() => devices.filter((device) => device.online), [devices]);
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

  const collect = async () => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const sent = await api.collectFiles(targets);
      toast.success(sent ? `已向 ${sent} 台设备发出收取请求` : "没有设备接受收取请求");
    } catch (error) { toast.error(error instanceof Error ? error.message : "收取文件失败"); }
  };

  const selectedOnline = devices.filter((d) => selectedIds.includes(d.id) && d.online).length;
  const activeTransfers = (classroom?.files ?? []).filter((t) => !t.done && !t.err).length;
  const helps = classroom?.helps ?? [];

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
        <Button size="sm" variant="outline" onClick={() => void collect()}><Download />收取文件</Button>
        <Separator orientation="vertical" className="mx-1 h-5" />
        <Button size="sm" variant="outline" onClick={() => setDialog("signin")}><ClipboardCheck />签到</Button>
        <Button size="sm" variant="outline" onClick={() => setDialog("poll")}><MessageSquareText />投票</Button>
        <Button size="sm" variant="outline" onClick={() => void toggleWatch()}>
          {watching ? <MonitorOff /> : <Monitor />}{watching ? "停止监看" : "开始监看"}
        </Button>
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

      {/* 学生机宫格（含预览画面） */}
      {devices.length ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 2xl:grid-cols-5">
          {devices.map((device) => {
            const frame = frames.get(device.id);
            const selected = selectedIds.includes(device.id);
            const age = frame ? Math.max(0, clock - frame.at) : Number.POSITIVE_INFINITY;
            const usable = Boolean(frame && age <= 30000);
            return (
              <article
                key={device.id}
                onClick={(event) => clickDevice(event, device.id)}
                className={`cursor-pointer overflow-hidden rounded-lg border bg-card select-none transition-shadow ${
                  selected ? "border-primary ring-2 ring-primary/40" : "hover:border-foreground/20"
                } ${device.online ? "" : "opacity-60"}`}
              >
                <div className="relative aspect-video bg-muted/60">
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
                  {!device.online && <Badge variant="secondary" className="absolute top-1.5 right-1.5">离线</Badge>}
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
          })}
        </div>
      ) : (
        <Empty className="min-h-40 border bg-muted/20"><EmptyHeader><EmptyTitle>还没有学生机</EmptyTitle><EmptyDescription>客户端上线后会显示在这里。</EmptyDescription></EmptyHeader></Empty>
      )}

      {/* 底部活动条：广播/演示/签到/投票/传输/求助 摘要 */}
      <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-lg border bg-card px-3 py-2 text-xs text-muted-foreground">
        <span className={casting ? "font-medium text-success" : ""}>
          {casting ? `● 广播中 · ${castTargets.length} 台 · ${classroom?.castMode === "fullscreen" ? "全屏" : "窗口"} · ${qualityLabels[classroom?.castQuality ?? "better"] ?? ""}` : "未广播"}
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
