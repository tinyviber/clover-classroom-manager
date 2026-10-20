import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { Check, Computer, LockKeyhole, Monitor, ShieldCheck, ShieldOff, Wifi } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { api, onDevicesChanged } from "@/api";
import { useLabStore } from "@/store";

export function ClientApp() {
  const catalog = useLabStore((store) => store.catalog);
  const status = useLabStore((store) => store.clientStatus);
  const setCatalog = useLabStore((store) => store.setCatalog);
  const setClientStatus = useLabStore((store) => store.setClientStatus);
  const [exitOpen, setExitOpen] = useState(false);
  const [password, setPassword] = useState("");
  const [passwordError, setPasswordError] = useState(false);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    const unsubscribe = onDevicesChanged((devices) => {
      const current = useLabStore.getState().clientStatus;
      const self = current && devices.find((device) => device.name === current.name && device.ip === current.ip);
      if (self) setClientStatus({ name: self.name, label: self.label, ip: self.ip, helperOn: self.helperOn, features: self.features });
    });
    Promise.all([api.catalog(), api.status()]).then(([nextCatalog, nextStatus]) => {
      if (!active) return;
      setCatalog(nextCatalog);
      setClientStatus(nextStatus);
    }).catch((error: unknown) => {
      if (active) toast.error(error instanceof Error ? error.message : "无法读取客户端状态");
    });
    return () => { active = false; unsubscribe(); };
  }, [setCatalog, setClientStatus]);

  const features = useMemo(() => {
    const enabled = new Set(status?.features ?? []);
    return catalog.flatMap((group) => group.features.filter((feature) => enabled.has(feature.key)).map((feature) => ({ ...feature, group: group.title })));
  }, [catalog, status?.features]);

  const tryExit = async () => {
    setBusy(true);
    setPasswordError(false);
    try {
      const ok = await api.exit(password);
      if (ok) {
        setExitOpen(false);
        setPassword("");
        toast.success("已提交退出请求");
      } else setPasswordError(true);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "退出请求失败");
    } finally { setBusy(false); }
  };

  return (
    <main className="min-h-screen bg-muted/30 px-4 py-8 sm:px-6">
      <div className="mx-auto max-w-2xl space-y-4">
        <header className="flex items-center gap-3 pb-2">
          <span className="grid size-10 place-items-center rounded-xl bg-white p-0.5 ring-1 ring-border/50"><img src="/clover-mark.svg" alt="" className="size-full object-contain" /></span>
          <div><p className="font-semibold tracking-tight">三叶草</p><p className="text-xs text-muted-foreground">学生端 · 局域网课堂管理</p></div>
        </header>

        <Card className="overflow-hidden shadow-none">
          <div className={`h-1.5 ${status?.helperOn ? "bg-success" : "bg-warning"}`} />
          <CardHeader className="pb-3">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div><CardTitle className="text-lg">本机状态</CardTitle><CardDescription className="mt-1">查看当前机房防护状态与已启用的功能。</CardDescription></div>
              <Badge variant={status?.helperOn ? "outline" : "secondary"} className={status?.helperOn ? "gap-1.5 border-success/25 bg-success/5 text-success" : "gap-1.5 text-warning"}>
                {status?.helperOn ? <ShieldCheck className="size-3.5" /> : <ShieldOff className="size-3.5" />}{status ? status.helperOn ? "防护中" : "已停用" : "正在连接"}
              </Badge>
            </div>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 sm:grid-cols-3">
              <DeviceInfo icon={Computer} label="设备编号" value={status?.label ?? "—"} />
              <DeviceInfo icon={Monitor} label="计算机名称" value={status?.name ?? "—"} />
              <DeviceInfo icon={Wifi} label="本机地址" value={status?.ip ?? "—"} mono />
            </div>
            <Separator />
            <div className="flex items-center justify-between gap-3"><div><p className="text-sm font-medium">已启用功能</p><p className="mt-0.5 text-xs text-muted-foreground">{status?.helperOn ? `${features.length} 项功能正在生效` : "防护停用时，功能不会生效"}</p></div><Badge variant="secondary">{status?.features.length ?? 0} 项</Badge></div>
            {status?.helperOn && features.length ? (
              <div className="grid gap-2 sm:grid-cols-2">{features.map((feature) => <div key={feature.key} className="flex min-w-0 items-start gap-2.5 rounded-lg border bg-background px-3 py-2.5"><span className="mt-0.5 grid size-4 shrink-0 place-items-center rounded-full bg-success/10 text-success"><Check className="size-3" /></span><div className="min-w-0"><p className="truncate text-sm font-medium">{feature.name}</p><p className="mt-0.5 text-xs text-muted-foreground">{feature.group}</p></div></div>)}</div>
            ) : <div className="rounded-lg border border-dashed bg-muted/20 px-4 py-8 text-center text-sm text-muted-foreground">{status ? status.helperOn ? "未启用任何功能" : "防护已停用，功能不生效" : "正在读取客户端状态…"}</div>}
            <div className="flex justify-end border-t pt-4"><Button variant="outline" size="sm" onClick={() => { setPassword(""); setPasswordError(false); setExitOpen(true); }}><LockKeyhole />退出小助手</Button></div>
          </CardContent>
        </Card>
        <p className="text-center text-xs text-muted-foreground">本机信息由管理端统一配置，设备状态变化会自动同步。</p>
      </div>

      <Dialog open={exitOpen} onOpenChange={setExitOpen}>
        <DialogContent className="max-w-sm">
          <DialogHeader><DialogTitle>退出小助手</DialogTitle><DialogDescription>输入管理密码以提交退出请求。</DialogDescription></DialogHeader>
          <label className="grid gap-1.5 text-sm font-medium">管理密码<Input type="password" value={password} onChange={(event) => { setPassword(event.target.value); setPasswordError(false); }} onKeyDown={(event) => event.key === "Enter" && void tryExit()} autoFocus aria-invalid={passwordError} />{passwordError && <span className="text-xs font-normal text-destructive">密码不正确，请重试。</span>}</label>
          <DialogFooter><Button variant="outline" onClick={() => setExitOpen(false)}>取消</Button><Button onClick={() => void tryExit()} disabled={busy}>{busy ? "正在验证…" : "确认退出"}</Button></DialogFooter>
        </DialogContent>
      </Dialog>
    </main>
  );
}

function DeviceInfo({ icon: Icon, label, value, mono = false }: { icon: typeof Computer; label: string; value: string; mono?: boolean }) {
  return <div className="min-w-0 rounded-lg border bg-muted/20 p-3"><div className="flex items-center gap-1.5 text-xs text-muted-foreground"><Icon className="size-3.5" />{label}</div><p className={`mt-2 truncate text-sm font-medium ${mono ? "font-mono" : ""}`}>{value}</p></div>;
}
