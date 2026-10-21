import { useCallback, useEffect, useState } from "react";
import { AnimatePresence, motion } from "motion/react";
import { toast } from "sonner";
import { ArrowDownToLine, CheckCircle2, ChevronRight, Computer, LoaderCircle, Monitor, Moon, Radio, RefreshCw, Sun } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { api, nativeRuntime, onClassroomChanged, onDevicesChanged } from "@/api";
import { useLabStore } from "@/store";
import type { AdminInfo } from "@/mygo";
import { DevicesView } from "./DevicesView";
import { ClassroomView } from "./ClassroomView";

export function AdminApp() {
  const devices = useLabStore((state) => state.devices);
  const loading = useLabStore((state) => state.loading);
  const loadError = useLabStore((state) => state.loadError);
  const lastScan = useLabStore((state) => state.lastScan);
  const view = useLabStore((state) => state.view);
  const setDevices = useLabStore((state) => state.setDevices);
  const setCatalog = useLabStore((state) => state.setCatalog);
  const setClassroom = useLabStore((state) => state.setClassroom);
  const setLoading = useLabStore((state) => state.setLoading);
  const setLoadError = useLabStore((state) => state.setLoadError);
  const setLastScan = useLabStore((state) => state.setLastScan);
  const setView = useLabStore((state) => state.setView);
  const theme = useLabStore((state) => state.resolvedTheme);
  const setTheme = useLabStore((state) => state.setTheme);

  useEffect(() => {
    let active = true;
    const unsubscribeDevices = onDevicesChanged((next) => setDevices(next));
    const unsubscribeClassroom = onClassroomChanged((next) => setClassroom(next));
    Promise.all([api.devices(), api.catalog(), api.classroomState()])
      .then(([nextDevices, catalog, classroom]) => {
        if (!active) return;
        setDevices(nextDevices);
        setCatalog(catalog);
        setClassroom(classroom);
        setLastScan(new Date().toLocaleTimeString("zh-CN", { hour12: false }));
        setLoadError("");
      })
      .catch((error: unknown) => {
        if (!active) return;
        const message = error instanceof Error ? error.message : "无法连接管理服务";
        setLoadError(message);
        toast.error(message);
      })
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
      unsubscribeDevices();
      unsubscribeClassroom();
    };
  }, [setClassroom, setCatalog, setDevices, setLastScan, setLoadError, setLoading]);

  const scan = useCallback(async () => {
    try {
      const latest = await api.scan();
      setDevices(latest);
      setLastScan(new Date().toLocaleTimeString("zh-CN", { hour12: false }));
      toast.success("已刷新局域网设备状态");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "扫描失败");
    }
  }, [setDevices, setLastScan]);

  const selectView = useCallback(async (next: string) => {
    if (next !== "devices" && next !== "classroom") return;
    if (next === view) return;
    if (view === "classroom") {
      const watchIds = useLabStore.getState().classroom?.screenWatchIds ?? [];
      if (watchIds.length) {
        try { await api.watchScreens(watchIds, false); }
        catch (error) { toast.error(error instanceof Error ? error.message : "停止屏幕监看失败"); }
      }
    }
    setView(next);
    if (next === "classroom") {
      try { setClassroom(await api.classroomState()); }
      catch (error) { toast.error(error instanceof Error ? error.message : "读取课堂状态失败"); }
    }
  }, [setClassroom, setView, view]);

  return (
    <div className="min-h-screen bg-background">
      <header className="sticky top-0 z-30 border-b bg-background/90 backdrop-blur-md">
        <div className="mx-auto flex min-h-16 max-w-[1600px] flex-wrap items-center gap-3 px-4 py-2.5 lg:px-8">
          <div className="flex min-w-48 flex-1 items-center gap-3">
            <div className="grid size-9 place-items-center rounded-xl bg-white p-0.5 shadow-sm ring-1 ring-border/50"><img src="/clover-mark.svg" alt="" className="size-full object-contain" /></div>
            <div className="leading-tight">
              <p className="font-semibold tracking-tight">三叶草</p>
              <p className="text-[11px] text-muted-foreground">局域网课堂管理</p>
            </div>
            <Separator orientation="vertical" className="mx-2 hidden h-8 sm:block" />
          </div>
          <nav aria-label="主要视图" className="order-3 flex w-full items-center gap-1 sm:order-none sm:w-auto">
            <Button variant={view === "devices" ? "secondary" : "ghost"} size="sm" aria-current={view === "devices" ? "page" : undefined} onClick={() => void selectView("devices")}><Monitor />设备</Button>
            <Button variant={view === "classroom" ? "secondary" : "ghost"} size="sm" aria-current={view === "classroom" ? "page" : undefined} onClick={() => void selectView("classroom")}><Radio />课堂</Button>
          </nav>
          <div className="order-2 ml-auto flex items-center gap-2 sm:order-none sm:ml-0">
            <Badge variant="outline" className="hidden gap-1.5 sm:inline-flex"><span className={`size-1.5 rounded-full ${loadError ? "bg-destructive" : "bg-success"}`} />{loadError ? "服务异常" : nativeRuntime ? "管理端在线" : "预览模式"}</Badge>
            <Button variant="outline" size="sm" onClick={() => void scan()} disabled={loading}><RefreshCw className={`size-4 ${loading ? "animate-spin" : ""}`} /><span className="hidden md:inline">扫描</span></Button>
            <DistributionDialog />
            <Button variant="ghost" size="icon" aria-label="切换浅色或深色主题" title="切换主题" onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
              {theme === "dark" ? <Sun /> : <Moon />}
            </Button>
          </div>
        </div>
      </header>

      <main className="mx-auto max-w-[1600px] space-y-5 px-4 py-6 lg:px-8 lg:py-8">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div>
            <div className="mb-1 flex items-center gap-2 text-xs text-muted-foreground"><span>管理</span><ChevronRight className="size-3" /><span className="text-foreground">{view === "devices" ? "设备总览" : "课堂控制"}</span></div>
            <h1 className="text-2xl font-semibold tracking-tight">{view === "devices" ? "设备总览" : "课堂控制"}</h1>
            <p className="mt-1 text-sm text-muted-foreground">{view === "devices" ? "查看设备状态、管理防护策略与批量操作。" : "屏幕广播、签到、文件收发与课堂互动。"}</p>
          </div>
          {view === "devices" ? <Badge variant="secondary" className="gap-1.5"><Computer className="size-3.5" />{devices.length} 台设备</Badge> : null}
        </div>

        {loading ? <LoadingState /> : loadError && devices.length === 0 ? (
          <div className="rounded-xl border border-destructive/20 bg-destructive/5 p-5 text-sm">
            <p className="font-medium text-destructive">无法连接管理服务</p>
            <p className="mt-1 text-muted-foreground">{loadError}</p>
            <Button className="mt-4" variant="outline" onClick={() => window.location.reload()}>重新连接</Button>
          </div>
        ) : (
          <AnimatePresence mode="wait" initial={false}>
            <motion.div key={view} initial={{ opacity: 0, y: 5 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0, y: -4 }} transition={{ duration: 0.16 }}>
              {view === "devices" ? <DevicesView /> : <ClassroomView />}
            </motion.div>
          </AnimatePresence>
        )}
      </main>

      <footer className="mx-auto flex max-w-[1600px] flex-wrap items-center justify-between gap-2 px-4 pb-6 text-xs text-muted-foreground lg:px-8">
        <span className="flex items-center gap-1.5"><CheckCircle2 className="size-3.5 text-success" />状态实时同步</span>
        <span>上次刷新 {lastScan || "—"}</span>
      </footer>
    </div>
  );
}

function LoadingState() {
  return <div className="space-y-5" aria-label="加载中"><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{Array.from({ length: 4 }, (_, index) => <Skeleton key={index} className="h-28 rounded-xl" />)}</div><Skeleton className="h-[430px] rounded-xl" /></div>;
}

function DistributionDialog() {
  const [open, setOpen] = useState(false);
  const [info, setInfo] = useState<AdminInfo | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [exporting, setExporting] = useState(false);
  const [addr, setAddr] = useState(""); // 教师选定的管理端 LAN 地址

  const loadInfo = async () => {
    setLoading(true);
    setLoadError("");
    try { setInfo(await api.adminInfo()); }
    catch (error) {
      const message = error instanceof Error ? error.message : "读取管理端信息失败";
      setLoadError(message);
      toast.error(message);
    }
    finally { setLoading(false); }
  };

  const show = async (nextOpen: boolean) => {
    setOpen(nextOpen);
    if (!nextOpen || info || loading) return;
    await loadInfo();
  };

  const exportPackage = async () => {
    setExporting(true);
    try {
      const output = await api.exportClientPackage(addr);
      if (output) { toast.success(`安装包已导出到 ${output}`); setOpen(false); }
      else toast.info("已取消导出");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "导出安装包失败");
    } finally { setExporting(false); }
  };

  return <>
    <Button size="sm" onClick={() => void show(true)}><ArrowDownToLine className="size-4" /><span className="hidden sm:inline">分发客户端</span><span className="sm:hidden">分发</span></Button>
    <Dialog open={open} onOpenChange={(next) => void show(next)}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>分发客户端</DialogTitle>
          <DialogDescription>在学生机运行安装包后，设备会自动连接到本管理端。</DialogDescription>
        </DialogHeader>
        {loading ? <div className="space-y-3 py-4"><Skeleton className="h-14" /><Skeleton className="h-14" /></div> : loadError ? (
          <div className="flex items-center justify-between gap-3 rounded-lg border border-destructive/20 bg-destructive/5 p-3"><p className="text-sm text-destructive">{loadError}</p><Button variant="outline" size="sm" onClick={() => void loadInfo()}>重试</Button></div>
        ) : info ? (
          <div className="space-y-4 py-2">
            <div className="rounded-lg border bg-muted/20 p-3">
              <p className="mb-2 text-xs font-medium text-muted-foreground">安装包写入的管理端地址 · TCP {info.port}</p>
              {info.ips.length ? (
                <Select value={addr || info.ips[0]} onValueChange={(v) => typeof v === "string" && setAddr(v)}>
                  <SelectTrigger className="h-8 font-mono text-sm"><SelectValue /></SelectTrigger>
                  <SelectContent>
                    {info.ips.map((ip) => <SelectItem key={ip} value={ip}>{ip}:{info.port}</SelectItem>)}
                  </SelectContent>
                </Select>
              ) : <p className="text-sm text-muted-foreground">未检测到本机 IPv4</p>}
              <p className="mt-2 text-xs text-muted-foreground">多网卡/VPN 环境请选学生机可达的地址；UDP 发现端口 47822 兜底</p>
            </div>
            <InfoLine label="公钥指纹" value={info.fingerprint} mono />
            <InfoLine label="数据目录" value={info.dataDir} mono />
            <p className="text-xs leading-relaxed text-muted-foreground">建议在机房还原软件解冻期间部署客户端并确认设备上线，再重新冻结系统。</p>
          </div>
        ) : null}
        <DialogFooter>
          <Button variant="outline" onClick={() => setOpen(false)}>关闭</Button>
          <Button onClick={() => void exportPackage()} disabled={!info || exporting}>{exporting ? <LoaderCircle className="animate-spin" /> : <ArrowDownToLine />}{exporting ? "正在导出…" : "导出安装包"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </>;
}

function InfoLine({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="grid gap-1 sm:grid-cols-[110px_1fr] sm:items-start"><span className="text-xs text-muted-foreground">{label}</span><span className={mono ? "break-all font-mono text-xs" : "break-all text-sm"}>{value || "—"}</span></div>;
}
