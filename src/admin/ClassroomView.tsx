import { useEffect, useMemo, useRef, useState } from "react";
import { AnimatePresence, motion } from "motion/react";
import { toast } from "sonner";
import { ArrowDownToLine, Check, CheckCheck, ClipboardCheck, Files, HelpCircle, MessageSquareText, Monitor, Play, Radio, ScreenShare, Send, Square, StopCircle, Users, Video } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { Input } from "@/components/ui/input";
import { Progress } from "@/components/ui/progress";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Textarea } from "@/components/ui/textarea";
import { api, nativeRuntime } from "@/api";
import type { ClassroomPane } from "@/store";
import { targetDeviceIds, useLabStore } from "@/store";
import { bytesLabel, formatClock, SectionHeading } from "./shared";

const paneOptions: { value: ClassroomPane; label: string; icon: typeof Monitor }[] = [
  { value: "wall", label: "屏幕墙", icon: Monitor },
  { value: "cast", label: "广播", icon: ScreenShare },
  { value: "signin", label: "签到", icon: ClipboardCheck },
  { value: "files", label: "文件", icon: Files },
  { value: "interact", label: "互动", icon: MessageSquareText },
];

export function ClassroomView() {
  const devices = useLabStore((store) => store.devices);
  const selectedIds = useLabStore((store) => store.selectedIds);
  const classroom = useLabStore((store) => store.classroom);
  const pane = useLabStore((store) => store.classroomPane);
  const setPane = useLabStore((store) => store.setClassroomPane);
  const setClassroom = useLabStore((store) => store.setClassroom);
  const targets = useMemo(() => targetDeviceIds(devices, selectedIds), [devices, selectedIds]);
  const [clock, setClock] = useState(Date.now());

  useEffect(() => {
    if (pane === "files" || pane === "cast") return;
    const timer = window.setInterval(() => setClock(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [pane]);

  const selectPane = async (value: string) => {
    if (!paneOptions.some((option) => option.value === value)) return;
    const next = value as ClassroomPane;
    if (pane === next) return;
    if (pane === "wall" && next !== "wall") {
      const watching = classroom?.screenWatchIds ?? [];
      if (watching.length) {
        try { await api.watchScreens(watching, false); }
        catch (error) { toast.error(error instanceof Error ? error.message : "停止屏幕监看失败"); }
      }
    }
    setPane(next);
  };

  const selectedOnline = devices.filter((device) => selectedIds.includes(device.id) && device.online).length;
  const targetDescription = selectedIds.length
    ? `已选在线设备 ${targets.length}/${selectedIds.length} 台`
    : `全部在线设备 ${targets.length} 台`;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border bg-card px-4 py-3">
        <div className="flex items-center gap-2 text-sm"><Users className="size-4 text-muted-foreground" /><span className="font-medium">{targetDescription}</span>{selectedIds.length > 0 && <Badge variant="secondary">{selectedOnline} 在线</Badge>}</div>
        <p className="text-xs text-muted-foreground">课堂操作仅下发给当前在线目标设备</p>
      </div>

      <ToggleGroup
        value={[pane]}
        onValueChange={(values) => void selectPane(values[0] ?? pane)}
        variant="default"
        size="sm"
        spacing={0}
        className="w-full justify-start overflow-x-auto bg-muted/70 p-1 sm:w-fit"
        aria-label="课堂工具"
      >
        {paneOptions.map(({ value, label, icon: Icon }) => <ToggleGroupItem key={value} value={value} className="min-w-fit"><Icon className="size-4" />{label}</ToggleGroupItem>)}
      </ToggleGroup>

      <AnimatePresence mode="wait" initial={false}>
        <motion.div key={pane} initial={{ opacity: 0, y: 4 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -3 }} transition={{ duration: 0.14 }}>
          {pane === "wall" && <ScreenWall clock={clock} />}
          {pane === "cast" && <Broadcast targets={targets} />}
          {pane === "signin" && <SignIn targets={targets} clock={clock} />}
          {pane === "files" && <FileTransfer targets={targets} />}
          {pane === "interact" && <Interaction targets={targets} clock={clock} />}
        </motion.div>
      </AnimatePresence>
    </div>
  );
}

function ScreenWall({ clock }: { clock: number }) {
  const devices = useLabStore((store) => store.devices);
  const selectedIds = useLabStore((store) => store.selectedIds);
  const classroom = useLabStore((store) => store.classroom);
  const watchingIds = classroom?.screenWatchIds ?? [];
  const online = useMemo(() => devices.filter((device) => device.online), [devices]);
  const frames = new Map(classroom?.screens.map((frame) => [frame.deviceId, frame]) ?? []);
  const frameCount = online.filter((device) => frames.has(device.id)).length;
  const [demoSource, setDemoSource] = useState(online[0]?.id ?? "");
  const setDemoSourceId = useLabStore((store) => store.setDemoSource);

  useEffect(() => {
    if (!online.some((device) => device.id === demoSource)) setDemoSource(online[0]?.id ?? "");
  }, [demoSource, online]);
  useEffect(() => { setDemoSourceId(demoSource); }, [demoSource, setDemoSourceId]);

  const toggleWatch = async () => {
    try {
      if (watchingIds.length) {
        await api.watchScreens(watchingIds, false);
        toast.success("已停止屏幕监看");
      } else {
        const ids = targetDeviceIds(devices, selectedIds);
        if (!ids.length) { toast.error("当前没有在线目标设备"); return; }
        await api.watchScreens(ids, true);
        toast.success(`已开始监看 ${ids.length} 台设备`);
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "屏幕监看操作失败");
    }
  };

  return (
    <div className="space-y-4">
      <SectionHeading
        title="学生屏幕"
        description={`${watchingIds.length ? `监看中 · ${frameCount}/${online.length} 台有画面` : `监看关闭 · ${frameCount} 台保留最近画面`} · 轻量缩略图持续更新`}
        action={<Button variant={watchingIds.length ? "outline" : "default"} onClick={() => void toggleWatch()}>{watchingIds.length ? <StopCircle /> : <Play />}{watchingIds.length ? "停止监看" : "开始监看"}</Button>}
      />
      {online.length ? (
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {online.map((device) => {
            const frame = frames.get(device.id);
            const age = frame ? Math.max(0, clock - frame.at) : Number.POSITIVE_INFINITY;
            const label = !frame ? "等待首帧" : age <= 6000 ? "实时" : age <= 15000 ? "延迟" : "画面过期";
            const usable = Boolean(frame && age <= 30000);
            return (
              <motion.article layout key={device.id} className="overflow-hidden rounded-xl border bg-card">
                <div className="flex items-center gap-2 border-b px-3 py-2.5">
                  <span className="grid size-7 shrink-0 place-items-center rounded-md bg-muted text-[11px] font-semibold text-muted-foreground">{device.label.replace(/^./, "")}</span>
                  <div className="min-w-0 flex-1"><p className="truncate text-xs font-medium">{device.label} · {device.name}</p><p className="truncate text-[11px] text-muted-foreground">{device.ip}</p></div>
                  <Badge variant={label === "实时" ? "outline" : "secondary"} className={label === "实时" ? "border-success/25 text-success" : "text-muted-foreground"}>{label}</Badge>
                </div>
                <div className="aspect-video bg-muted/50">
                  {usable ? <img className="size-full object-cover" src={`data:image/jpeg;base64,${frame!.jpeg}`} alt={`${device.label} 学生屏幕缩略图`} /> : <div className="grid size-full place-items-center text-xs text-muted-foreground">{label}</div>}
                </div>
                <div className="flex items-center justify-between gap-2 px-3 py-2">
                  <span className="text-[11px] text-muted-foreground">{frame ? `更新于 ${formatClock(frame.at)}` : "暂无画面"}</span>
                  <Button size="xs" variant={demoSource === device.id ? "secondary" : "ghost"} onClick={() => { setDemoSource(device.id); toast.success("已选择学生演示来源"); }}><Video />{demoSource === device.id ? "演示来源" : "设为来源"}</Button>
                </div>
              </motion.article>
            );
          })}
        </div>
      ) : <Empty className="min-h-36 border bg-muted/20"><EmptyHeader><EmptyTitle>当前没有在线设备</EmptyTitle><EmptyDescription>设备上线后，学生屏幕缩略图会显示在这里。</EmptyDescription></EmptyHeader></Empty>}
      <p className="text-xs text-muted-foreground">选择一台设备作为学生演示来源后，可在「互动」中发起演示请求。</p>
    </div>
  );
}

function Broadcast({ targets }: { targets: string[] }) {
  const devices = useLabStore((store) => store.devices);
  const classroom = useLabStore((store) => store.classroom);
  const [mode, setMode] = useState("window");
  const started = Boolean(classroom?.castMode);
  const castTargets = classroom?.castTargetIds ?? [];
  const onlineTargets = castTargets.filter((id) => devices.some((device) => device.id === id && device.online)).length;

  const start = async () => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try {
      const ok = await api.castStart(mode, targets);
      if (!ok) toast.error("广播未启动，请检查设备状态或课堂演示状态");
      else toast.success(`已向 ${targets.length} 台设备发起广播`);
    } catch (error) { toast.error(error instanceof Error ? error.message : "启动广播失败"); }
  };
  const stop = async () => {
    try { await api.castStop(); toast.success("已停止屏幕广播"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "停止广播失败"); }
  };

  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(280px,0.8fr)]">
      <Card className="shadow-none">
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-base"><ScreenShare className="size-4 text-primary" />教师屏幕广播</CardTitle>
          <CardDescription>将教师屏幕以窗口或全屏模式发送给当前目标设备。</CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <label className="grid max-w-sm gap-2 text-sm font-medium">广播模式
            <Select value={mode} onValueChange={(value) => typeof value === "string" && setMode(value)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="window">窗口广播</SelectItem><SelectItem value="fullscreen">全屏广播</SelectItem></SelectContent>
            </Select>
          </label>
          <div className="rounded-lg border bg-muted/20 p-3">
            <div className="flex items-center justify-between gap-3"><span className="text-xs text-muted-foreground">目标范围</span><Badge variant="secondary">{started ? castTargets.length : targets.length} 台</Badge></div>
            <p className="mt-1 text-sm">{started ? `广播中 · 当前在线 ${onlineTargets} 台` : targets.length ? "按顶部的设备选择范围发送" : "没有在线目标设备"}</p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => void start()} disabled={started || !targets.length}><Play />开始广播</Button>
            <Button variant="outline" onClick={() => void stop()} disabled={!started}><Square />停止广播</Button>
          </div>
        </CardContent>
      </Card>
      <Card className="shadow-none">
        <CardHeader><CardTitle className="text-base">当前状态</CardTitle><CardDescription>管理端将持续显示广播活动状态。</CardDescription></CardHeader>
        <CardContent>
          <div className="flex items-start gap-3 rounded-lg border p-3">
            <span className={`mt-0.5 grid size-8 place-items-center rounded-lg ${started ? "bg-success/10 text-success" : "bg-muted text-muted-foreground"}`}><Radio className="size-4" /></span>
            <div><p className="text-sm font-medium">{started ? "广播进行中" : "未广播"}</p><p className="mt-1 text-xs text-muted-foreground">{started ? `模式：${classroom?.castMode === "fullscreen" ? "全屏" : "窗口"}` : "开始后学生端会显示教师画面。"}</p></div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function SignIn({ targets, clock }: { targets: string[]; clock: number }) {
  const signIn = useLabStore((store) => store.classroom?.signIn);
  const [duration, setDuration] = useState("0");
  const open = signIn?.open ?? false;
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
    <div className="grid gap-4 xl:grid-cols-[minmax(0,0.85fr)_minmax(0,1.15fr)]">
      <Card className="shadow-none">
        <CardHeader><CardTitle className="flex items-center gap-2 text-base"><ClipboardCheck className="size-4 text-primary" />课堂签到</CardTitle><CardDescription>向当前目标学生发送签到请求，并实时汇总回复。</CardDescription></CardHeader>
        <CardContent className="space-y-4">
          <label className="grid max-w-sm gap-2 text-sm font-medium">截止时间
            <Select value={duration} onValueChange={(value) => typeof value === "string" && setDuration(value)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="0">不限时</SelectItem><SelectItem value="60">1 分钟</SelectItem><SelectItem value="180">3 分钟</SelectItem><SelectItem value="300">5 分钟</SelectItem></SelectContent>
            </Select>
          </label>
          <div className="flex flex-wrap gap-2"><Button onClick={() => void start()} disabled={open || !targets.length}><Send />发起签到</Button><Button variant="outline" onClick={() => void stop()} disabled={!open}><StopCircle />结束签到</Button></div>
        </CardContent>
      </Card>
      <Card className="shadow-none">
        <CardHeader className="flex-row items-start justify-between gap-3"><div><CardTitle className="text-base">签到记录</CardTitle><CardDescription>{signIn?.id ? `目标 ${signIn.sent} 台` : "尚未发起签到"}</CardDescription></div><Badge variant={open ? "outline" : "secondary"} className={open ? "border-success/25 text-success" : ""}>{open ? "进行中" : signIn?.id ? "已结束" : "未开始"}</Badge></CardHeader>
        <CardContent className="space-y-3">
          {signIn?.id && <div className="flex items-center justify-between rounded-lg bg-muted/40 px-3 py-2 text-sm"><span>已签到 {signIn.replies.length}/{signIn.sent} 台</span>{open && signIn.deadline > 0 && <span className="text-xs text-muted-foreground">剩余 {remaining} 秒</span>}</div>}
          {signIn?.replies.length ? <div className="max-h-72 divide-y overflow-y-auto rounded-lg border">{signIn.replies.map((reply) => <div key={`${reply.deviceId}-${reply.at}`} className="flex items-center gap-3 px-3 py-2.5 text-sm"><Badge variant="secondary">{reply.label}</Badge><span className="min-w-0 flex-1 truncate">{reply.name || "已签到"}</span><span className="text-xs text-muted-foreground">{formatClock(reply.at)}</span><Check className="size-4 text-success" /></div>)}</div> : <Empty className="min-h-28 border bg-muted/10"><EmptyHeader><EmptyTitle>{open ? "等待学生签到" : "暂无签到记录"}</EmptyTitle></EmptyHeader></Empty>}
        </CardContent>
      </Card>
    </div>
  );
}

function FileTransfer({ targets }: { targets: string[] }) {
  const classroom = useLabStore((store) => store.classroom);
  const pickedPaths = useLabStore((store) => store.pickedPaths);
  const setPickedPaths = useLabStore((store) => store.setPickedPaths);
  const inputRef = useRef<HTMLInputElement>(null);
  const transfers = classroom?.files ?? [];

  const pickFiles = async () => {
    if (nativeRuntime) {
      try { setPickedPaths(await api.pickFiles()); }
      catch (error) { toast.error(error instanceof Error ? error.message : "打开文件选择器失败"); }
    } else inputRef.current?.click();
  };
  const send = async () => {
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    if (!pickedPaths.length) { toast.error("请先选择要分发的文件"); return; }
    const expected = targets.length * pickedPaths.length;
    try {
      const queued = await api.sendFiles(targets, pickedPaths);
      if (!queued) toast.error("未能启动文件传输，请检查文件和设备状态");
      else if (queued < expected) toast.warning(`已启动 ${queued}/${expected} 项传输，未启动部分请检查文件和设备状态`);
      else toast.success(`已启动 ${queued} 项文件传输`);
      if (queued > 0) {
        setPickedPaths([]);
        if (inputRef.current) inputRef.current.value = "";
      }
    } catch (error) { toast.error(error instanceof Error ? error.message : "文件分发失败"); }
  };
  const openReceived = async () => {
    try { const path = await api.openRecvDir(); if (path) toast.info(`接收目录：${path}`); }
    catch (error) { toast.error(error instanceof Error ? error.message : "打开接收目录失败"); }
  };

  return (
    <div className="space-y-4">
      <input ref={inputRef} type="file" multiple className="hidden" onChange={(event) => setPickedPaths(Array.from(event.currentTarget.files ?? []).map((file) => `mock://${file.name}`))} />
      <div className="grid gap-4 xl:grid-cols-[minmax(0,0.8fr)_minmax(0,1.2fr)]">
        <Card className="shadow-none">
          <CardHeader><CardTitle className="flex items-center gap-2 text-base"><Files className="size-4 text-primary" />文件分发</CardTitle><CardDescription>向当前目标设备发送文件，查看每台设备的传输进度。</CardDescription></CardHeader>
          <CardContent className="space-y-4">
            <div className="rounded-lg border border-dashed bg-muted/20 p-4">
              <p className="text-sm font-medium">待发送文件</p>
              <p className="mt-1 break-all text-xs text-muted-foreground">{pickedPaths.length ? pickedPaths.join("、") : "尚未选择文件"}</p>
              <div className="mt-3 flex flex-wrap gap-2"><Button variant="outline" size="sm" onClick={() => void pickFiles()}><Files />选择文件</Button><Button size="sm" onClick={() => void send()} disabled={!pickedPaths.length}><Send />发送到 {targets.length} 台</Button></div>
            </div>
            <div className="flex items-center justify-between gap-3 rounded-lg bg-muted/40 px-3 py-2.5"><span className="text-xs text-muted-foreground">客户端收到的文件可在接收目录查看</span><Button variant="ghost" size="sm" onClick={() => void openReceived()}><ArrowDownToLine />打开接收目录</Button></div>
          </CardContent>
        </Card>
        <Card className="shadow-none">
          <CardHeader className="flex-row items-start justify-between gap-3"><div><CardTitle className="text-base">传输记录</CardTitle><CardDescription>分发与客户端收取的实时进度</CardDescription></div><Badge variant="secondary">{transfers.length} 项</Badge></CardHeader>
          <CardContent>
            {transfers.length ? <div className="max-h-[420px] divide-y overflow-y-auto rounded-lg border">{transfers.map((item) => {
              const percent = item.size > 0 ? Math.min(100, Math.round(item.sent * 100 / item.size)) : item.done ? 100 : 0;
              const status = item.err ? `失败：${item.err}` : item.done ? "完成" : "传输中";
              return <div key={item.id} className="space-y-2 px-3 py-3">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm"><Badge variant="outline">{item.label}</Badge><span className="min-w-0 flex-1 truncate font-medium">{item.name}</span><span className="text-xs text-muted-foreground">{item.in ? "收取" : "分发"}</span></div>
                <div className="flex items-center gap-3"><Progress value={percent} className="flex-1" /><span className="w-12 text-right text-xs tabular-nums text-muted-foreground">{percent}%</span></div>
                <div className="flex justify-between gap-2 text-[11px] text-muted-foreground"><span>{bytesLabel(item.sent)} / {bytesLabel(item.size)}</span><span className={item.err ? "text-destructive" : item.done ? "text-success" : ""}>{status}</span></div>
              </div>;
            })}</div> : <Empty className="min-h-36 border bg-muted/10"><EmptyHeader><EmptyTitle>还没有文件传输</EmptyTitle><EmptyDescription>客户端收到或管理端分发文件后，进度会显示在这里。</EmptyDescription></EmptyHeader></Empty>}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function Interaction({ targets, clock }: { targets: string[]; clock: number }) {
  const devices = useLabStore((store) => store.devices);
  const classroom = useLabStore((store) => store.classroom);
  const sourceFromStore = useLabStore((store) => store.demoSourceId);
  const setDemoSource = useLabStore((store) => store.setDemoSource);
  const poll = classroom?.poll;
  const demo = classroom?.demo;
  const helps = classroom?.helps ?? [];
  const [question, setQuestion] = useState("");
  const [optionText, setOptionText] = useState("选项 A\n选项 B");
  const [pollDuration, setPollDuration] = useState("60");
  const source = devices.some((device) => device.id === sourceFromStore && device.online) ? sourceFromStore : devices.find((device) => device.online)?.id ?? "";
  const options = optionText.split("\n").map((value) => value.trim()).filter(Boolean);
  const pollRemaining = poll?.deadline ? Math.max(0, poll.deadline - Math.floor(clock / 1000)) : 0;
  const demoActive = Boolean(demo?.state);
  const demoTargets = demoActive ? (demo?.targetIds ?? []) : targets.filter((id) => id !== source);

  const startPoll = async () => {
    if (!question.trim()) { toast.error("请填写投票问题"); return; }
    if (options.length < 2 || options.length > 6) { toast.error("投票选项需要为 2–6 个"); return; }
    if (!targets.length) { toast.error("当前没有在线目标设备"); return; }
    try { const count = await api.pollStart(question.trim(), options, Number(pollDuration), targets); toast.success(`已向 ${count} 台设备发布投票`); }
    catch (error) { toast.error(error instanceof Error ? error.message : "发布投票失败"); }
  };
  const stopPoll = async () => {
    try { await api.pollStop(); toast.success("投票已结束"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "结束投票失败"); }
  };
  const startDemo = async () => {
    if (!source) { toast.error("没有可用的在线演示来源"); return; }
    if (!demoTargets.length) { toast.error("请选择至少一台接收设备（来源设备不计入）"); return; }
    try {
      const started = await api.demoStart(source, demoTargets);
      if (started) toast.success("演示请求已发送，等待学生确认");
      else toast.error("演示请求未发送，请检查来源设备或当前课堂活动");
    } catch (error) { toast.error(error instanceof Error ? error.message : "发起学生演示失败"); }
  };
  const stopDemo = async () => {
    try { await api.demoStop(); toast.success("学生演示已停止"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "停止学生演示失败"); }
  };
  const clearHelp = async (deviceId: string) => {
    try { await api.clearHelp(deviceId); toast.success("已处理求助提醒"); }
    catch (error) { toast.error(error instanceof Error ? error.message : "处理求助失败"); }
  };

  return (
    <div className="grid gap-4 2xl:grid-cols-2">
      <Card className="shadow-none">
        <CardHeader><CardTitle className="flex items-center gap-2 text-base"><MessageSquareText className="size-4 text-primary" />随堂投票</CardTitle><CardDescription>发布单题投票并查看实时统计。</CardDescription></CardHeader>
        <CardContent className="space-y-4">
          <label className="grid gap-1.5 text-sm font-medium">问题<Input value={question} onChange={(event) => setQuestion(event.target.value.slice(0, 240))} placeholder="例如：这一步是否已经完成？" maxLength={240} disabled={poll?.open} /></label>
          <label className="grid gap-1.5 text-sm font-medium">选项 <span className="text-xs font-normal text-muted-foreground">每行一个，2–6 项</span><Textarea value={optionText} onChange={(event) => setOptionText(event.target.value)} rows={4} placeholder="每行输入一个选项" disabled={poll?.open} /></label>
          <div className="flex flex-wrap items-end gap-3">
            <label className="grid min-w-36 gap-1.5 text-sm font-medium">投票时长
              <Select value={pollDuration} onValueChange={(value) => typeof value === "string" && setPollDuration(value)} disabled={poll?.open}>
                <SelectTrigger><SelectValue /></SelectTrigger><SelectContent><SelectItem value="30">30 秒</SelectItem><SelectItem value="60">1 分钟</SelectItem><SelectItem value="180">3 分钟</SelectItem></SelectContent>
              </Select>
            </label>
            <Button onClick={() => void startPoll()} disabled={Boolean(poll?.open)}><Send />发布投票</Button>
            <Button variant="outline" onClick={() => void stopPoll()} disabled={!poll?.open}><StopCircle />结束投票</Button>
          </div>
          {poll?.id ? <PollResults poll={poll} remaining={pollRemaining} /> : <p className="rounded-lg bg-muted/30 px-3 py-2 text-xs text-muted-foreground">发布问题后会在这里显示实时统计。</p>}
        </CardContent>
      </Card>

      <div className="space-y-4">
        <Card className="shadow-none">
          <CardHeader><CardTitle className="flex items-center gap-2 text-base"><Video className="size-4 text-primary" />学生演示</CardTitle><CardDescription>学生确认后，将选定学生屏幕转发给其他目标设备。</CardDescription></CardHeader>
          <CardContent className="space-y-4">
            <label className="grid max-w-md gap-2 text-sm font-medium">演示来源
              <Select value={source} onValueChange={(value) => typeof value === "string" && setDemoSource(value)} disabled={demoActive}>
                <SelectTrigger><SelectValue placeholder="选择学生设备" /></SelectTrigger>
                <SelectContent>{devices.filter((device) => device.online).map((device) => <SelectItem key={device.id} value={device.id}>{device.label} · {device.name}</SelectItem>)}</SelectContent>
              </Select>
            </label>
            <div className="flex flex-wrap items-center gap-2"><Button onClick={() => void startDemo()} disabled={demoActive || !source}><Play />请求演示</Button><Button variant="outline" onClick={() => void stopDemo()} disabled={!demoActive}><StopCircle />停止演示</Button><Badge variant={demo?.state === "active" ? "outline" : "secondary"} className={demo?.state === "active" ? "border-success/25 text-success" : ""}>{demo?.state === "pending" ? "等待学生确认" : demo?.state === "active" ? `演示中 · ${demo.sourceLabel}` : demo?.err ? `失败：${demo.err}` : "未进行"}</Badge></div>
            <p className="text-xs text-muted-foreground">接收设备：{demoTargets.length} 台{demoActive ? "" : "（当前目标中排除演示来源）"}</p>
          </CardContent>
        </Card>
        <Card className="shadow-none">
          <CardHeader className="flex-row items-start justify-between gap-3"><div><CardTitle className="flex items-center gap-2 text-base"><HelpCircle className="size-4 text-warning" />学生求助</CardTitle><CardDescription>收到学生端举手请求后在这里处理。</CardDescription></div><Badge variant="secondary">{helps.length} 个待处理</Badge></CardHeader>
          <CardContent>
            {helps.length ? <div className="divide-y rounded-lg border">{helps.map((request) => <div key={`${request.deviceId}-${request.at}`} className="flex flex-wrap items-center gap-2 px-3 py-2.5"><Badge variant="outline">{request.label}</Badge><span className="min-w-0 flex-1 truncate text-sm">{request.name}</span><span className="text-xs text-muted-foreground">{formatClock(request.at)}</span><Button size="xs" variant="ghost" onClick={() => void clearHelp(request.deviceId)}><CheckCheck />已处理</Button></div>)}</div> : <Empty className="min-h-28 border bg-muted/10"><EmptyHeader><EmptyTitle>暂无待处理请求</EmptyTitle><EmptyDescription>学生发起求助后会出现在这里。</EmptyDescription></EmptyHeader></Empty>}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}

function PollResults({ poll, remaining }: { poll: NonNullable<ReturnType<typeof useLabStore.getState>["classroom"]>["poll"]; remaining: number }) {
  const counts = poll.options.map((option) => poll.replies.filter((reply) => reply.option === option).length);
  return <div className="space-y-3 rounded-lg border bg-muted/10 p-3">
    <div className="flex flex-wrap items-start justify-between gap-2"><div><p className="text-sm font-medium">{poll.question}</p><p className="mt-1 text-xs text-muted-foreground">{poll.replies.length} 人作答{poll.open ? ` · 剩余 ${remaining} 秒` : ""}</p></div><Badge variant={poll.open ? "outline" : "secondary"} className={poll.open ? "border-success/25 text-success" : ""}>{poll.open ? "进行中" : "已结束"}</Badge></div>
    {poll.options.map((option, index) => {
      const count = counts[index] ?? 0;
      const percent = poll.replies.length ? Math.round(count * 100 / poll.replies.length) : 0;
      return <div key={`${option}-${index}`} className="space-y-1.5"><div className="flex justify-between gap-2 text-xs"><span>{option}</span><span className="tabular-nums text-muted-foreground">{count} 人 · {percent}%</span></div><Progress value={percent} /></div>;
    })}
    <Separator />
    <p className="text-xs text-muted-foreground">未回复 {Math.max(0, poll.sent - poll.replies.length)} 台</p>
  </div>;
}
