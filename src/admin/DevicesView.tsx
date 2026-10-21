import { useEffect, useMemo, useState } from "react";
import { toast } from "sonner";
import { AlertTriangle, ChevronDown, ChevronRight, CircleHelp, Computer, Search, ShieldCheck, ShieldOff, SlidersHorizontal, Trash2, Wifi } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Empty, EmptyDescription, EmptyHeader, EmptyTitle } from "@/components/ui/empty";
import { FieldGroup, FieldSet, FieldTitle } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Separator } from "@/components/ui/separator";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/api";
import { filterDevices, useLabStore, type DeviceFilter } from "@/store";
import type { Device } from "@/mygo";

type FeatureDialogState = { mode: "single"; device: Device } | { mode: "template"; ids: string[] } | null;

const filterOptions: { key: DeviceFilter; label: string }[] = [
  { key: "all", label: "全部设备" },
  { key: "online", label: "在线" },
  { key: "offline", label: "离线" },
  { key: "alert", label: "有告警" },
  { key: "pending", label: "待审批" },
];

export function DevicesView() {
  const devices = useLabStore((state) => state.devices);
  const catalog = useLabStore((state) => state.catalog);
  const filter = useLabStore((state) => state.filter);
  const query = useLabStore((state) => state.query);
  const selectedIds = useLabStore((state) => state.selectedIds);
  const expandedIds = useLabStore((state) => state.expandedIds);
  const setFilter = useLabStore((state) => state.setFilter);
  const setQuery = useLabStore((state) => state.setQuery);
  const toggleSelected = useLabStore((state) => state.toggleSelected);
  const setVisibleSelected = useLabStore((state) => state.setVisibleSelected);
  const clearSelection = useLabStore((state) => state.clearSelection);
  const toggleExpanded = useLabStore((state) => state.toggleExpanded);
  const [featureDialog, setFeatureDialog] = useState<FeatureDialogState>(null);

  const visible = useMemo(() => filterDevices(devices, filter, query), [devices, filter, query]);
  const online = devices.filter((device) => device.online).length;
  const pendingCount = devices.filter((device) => device.pending).length;
  const protectedCount = devices.filter((device) => device.helperOn).length;
  const alertCount = devices.filter((device) => device.alerts > 0).length;
  // 生效功能只数 ready=true 的（客户端真有执行实现）；
  // 规划中项不算数，也不允许勾选，避免「已启用」假象。
  const readyFeatures = useMemo(() => catalog.flatMap((g) => g.features).filter((f) => f.ready), [catalog]);
  const featureTotal = readyFeatures.length;
  const { allFeatureKeys, featureNameByKey, readyKeys } = useMemo(() => {
    const features = catalog.flatMap((group) => group.features);
    return {
      allFeatureKeys: features.filter((f) => f.ready).map((feature) => feature.key),
      featureNameByKey: new Map(features.map((feature) => [feature.key, feature.name])),
      readyKeys: new Set(features.filter((f) => f.ready).map((f) => f.key)),
    };
  }, [catalog]);
  const allVisibleSelected = visible.length > 0 && visible.every((device) => selectedIds.includes(device.id));
  const someVisibleSelected = visible.some((device) => selectedIds.includes(device.id));

  const runAction = async <Result,>(action: () => Promise<Result>, success: (result: Result) => string) => {
    try {
      const result = await action();
      toast.success(success(result));
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "操作未能完成");
    }
  };

  const batchToggle = (on: boolean) => runAction(
    () => api.setHelper(selectedIds, on),
    (count) => `已向 ${count} 台设备下发${on ? "启用" : "停用"}防护`,
  );

  const pendingSelectedIds = useMemo(
    () => devices.filter((d) => d.pending && selectedIds.includes(d.id)).map((d) => d.id),
    [devices, selectedIds],
  );

  return (
    <div className="space-y-5">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <MetricCard icon={Computer} label="设备总数" value={devices.length} hint="已注册设备" tone="text-foreground" />
        <MetricCard icon={Wifi} label="在线设备" value={online} hint={`离线 ${devices.length - online} 台`} tone="text-success" />
        <MetricCard icon={ShieldCheck} label="防护已启用" value={protectedCount} hint={`已停用 ${devices.length - protectedCount} 台`} tone="text-primary" />
        <MetricCard icon={AlertTriangle} label="待处理告警" value={alertCount} hint={alertCount ? "请查看设备详情" : "当前没有告警"} tone={alertCount ? "text-warning" : "text-muted-foreground"} />
      </div>

      <Card className="gap-0 overflow-hidden py-0 shadow-none">
        <div className="flex flex-col gap-3 border-b p-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h2 className="font-semibold tracking-tight">设备列表</h2>
            <p className="mt-1 text-xs text-muted-foreground">按设备状态筛选，选择设备后可批量下发操作</p>
          </div>
          <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
            <ToggleGroup
              value={[filter]}
              onValueChange={(values) => { if (values[0]) setFilter(values[0] as DeviceFilter); }}
              variant="default"
              size="sm"
              spacing={0}
              className="bg-muted/70 p-1"
              aria-label="设备状态筛选"
            >
              {filterOptions.map((option) => (
                <ToggleGroupItem key={option.key} value={option.key}>
                  {option.label}
                  {option.key === "all" ? <span className="text-muted-foreground">{devices.length}</span> : null}
                  {option.key === "pending" && pendingCount ? <Badge variant="destructive" className="ml-1 px-1.5">{pendingCount}</Badge> : null}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
            <div className="relative min-w-56">
              <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索名称、编号或 IP" className="pl-8" aria-label="搜索设备" />
            </div>
          </div>
        </div>

        {selectedIds.length > 0 && (
          <div className="flex flex-wrap items-center gap-2 border-b bg-primary/5 px-4 py-2.5">
            <Badge variant="secondary" className="rounded-md">已选 {selectedIds.length} 台</Badge>
            <Separator orientation="vertical" className="mx-1 h-5" />
            <Button size="sm" onClick={() => void batchToggle(true)}><ShieldCheck />启用防护</Button>
            <Button size="sm" variant="outline" onClick={() => void batchToggle(false)}><ShieldOff />停用防护</Button>
            <Button size="sm" variant="outline" onClick={() => setFeatureDialog({ mode: "template", ids: selectedIds })}><SlidersHorizontal />套用功能模板</Button>
            {pendingSelectedIds.length > 0 && (
              <Button size="sm" variant="outline" onClick={() => void runAction(() => api.approveDevices(pendingSelectedIds), (count) => `已批准 ${count} 台设备加入课堂`)}>批准所选 {pendingSelectedIds.length} 台</Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="text-destructive"
              onClick={() => {
                if (!window.confirm(`确定删除所选 ${selectedIds.length} 台设备？在线设备会先尝试解锁/停广播再删除，重连后需重新审批。离线设备若处于锁定，删除后需现场紧急解锁（Ctrl+Alt+Shift+F12×3）恢复。`)) return;
                void runAction(() => api.removeDevices(selectedIds), (count) => `已删除 ${count} 台设备`);
              }}
            ><Trash2 />删除所选</Button>
            <Button size="sm" variant="ghost" className="ml-auto" onClick={clearSelection}>清除选择</Button>
          </div>
        )}

        {visible.length === 0 ? (
          <div className="p-4"><Empty className="min-h-36 border bg-muted/20"><EmptyHeader><EmptyTitle>没有匹配的设备</EmptyTitle><EmptyDescription>换一个筛选条件或搜索词试试。</EmptyDescription></EmptyHeader></Empty></div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/30 hover:bg-muted/30">
                <TableHead className="w-11 pl-4">
                  <Checkbox
                    aria-label="选择当前列表中的全部设备"
                    checked={allVisibleSelected}
                    indeterminate={someVisibleSelected && !allVisibleSelected}
                    onCheckedChange={(checked) => setVisibleSelected(visible.map((device) => device.id), checked === true)}
                  />
                </TableHead>
                <TableHead>设备</TableHead>
                <TableHead>IP 地址</TableHead>
                <TableHead>连接状态</TableHead>
                <TableHead>防护</TableHead>
                <TableHead>功能</TableHead>
                <TableHead>最近活动</TableHead>
                <TableHead className="pr-4 text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {visible.map((device) => {
                const selected = selectedIds.includes(device.id);
                const expanded = expandedIds.includes(device.id);
                const activeFeatures = device.features.map((key) => featureNameByKey.get(key)).filter((name): name is string => Boolean(name));
                const enabledCount = device.features.filter((key) => readyKeys.has(key)).length;
                const plannedCount = device.features.length - enabledCount;
                return (
                  <DeviceRows
                    key={device.id}
                    device={device}
                    selected={selected}
                    expanded={expanded}
                    featureTotal={featureTotal}
                    enabledCount={enabledCount}
                    plannedCount={plannedCount}
                    activeFeatures={activeFeatures}
                    allFeatureKeys={allFeatureKeys}
                    onSelect={() => toggleSelected(device.id)}
                    onExpand={() => toggleExpanded(device.id)}
                    onConfigure={() => setFeatureDialog({ mode: "single", device })}
                  />
                );
              })}
            </TableBody>
          </Table>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2 border-t bg-muted/20 px-4 py-2.5 text-xs text-muted-foreground">
          <span>显示 {visible.length} / {devices.length} 台设备</span>
          <span className="flex items-center gap-1.5"><CircleHelp className="size-3.5" />点击设备行可展开更多信息</span>
        </div>
      </Card>

      <FeatureDialog state={featureDialog} onOpenChange={(open) => !open && setFeatureDialog(null)} />
    </div>
  );
}

function MetricCard({ icon: Icon, label, value, hint, tone }: { icon: typeof Computer; label: string; value: number; hint: string; tone: string }) {
  return (
    <Card className="gap-0 py-0 shadow-none">
      <CardContent className="flex items-center justify-between gap-4 p-4">
        <div className="min-w-0">
          <p className="text-xs text-muted-foreground">{label}</p>
          <p className={`mt-1 text-2xl font-semibold tabular-nums tracking-tight ${tone}`}>{value}</p>
          <p className="mt-1 truncate text-xs text-muted-foreground">{hint}</p>
        </div>
        <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-muted/70 text-muted-foreground"><Icon className="size-5" /></span>
      </CardContent>
    </Card>
  );
}

function DeviceRows({
  device, selected, expanded, featureTotal, enabledCount, plannedCount, activeFeatures, allFeatureKeys, onSelect, onExpand, onConfigure,
}: {
  device: Device;
  selected: boolean;
  expanded: boolean;
  featureTotal: number;
  enabledCount: number;
  plannedCount: number;
  activeFeatures: string[];
  allFeatureKeys: string[];
  onSelect: () => void;
  onExpand: () => void;
  onConfigure: () => void;
}) {
  const run = async <Result,>(action: () => Promise<Result>, success: (result: Result) => string) => {
    try { toast.success(success(await action())); }
    catch (error) { toast.error(error instanceof Error ? error.message : "操作未能完成"); }
  };
  return <>
    <TableRow data-state={selected ? "selected" : undefined} className="cursor-pointer" onClick={onExpand} aria-expanded={expanded}>
      <TableCell className="pl-4" onClick={(event) => event.stopPropagation()}><Checkbox aria-label={`选择 ${device.label}`} checked={selected} onCheckedChange={onSelect} /></TableCell>
      <TableCell>
        <div className="flex min-w-48 items-center gap-3">
          <Button variant="ghost" size="icon-xs" className="text-muted-foreground" aria-label={expanded ? "收起设备详情" : "展开设备详情"} onClick={(event) => { event.stopPropagation(); onExpand(); }}>
            {expanded ? <ChevronDown /> : <ChevronRight />}
          </Button>
          <span className="grid size-8 shrink-0 place-items-center rounded-lg bg-muted text-xs font-semibold text-muted-foreground">{device.label.replace(/^[A-Za-z]+-?/, "") || device.label}</span>
          <span className="min-w-0">
            <span className="flex flex-wrap items-center gap-1.5 font-medium text-foreground">{device.name}{device.pending && <Badge variant="outline" className="border-warning/40 bg-warning/10 text-warning">待审批</Badge>}{device.locked && <Badge variant="destructive">已锁定</Badge>}{device.castTcp && <Badge variant="outline" className="border-info/40 text-info">广播走TCP</Badge>}{(device.featErrs?.length ?? 0) > 0 && <Badge variant="outline" className="border-warning/40 bg-warning/5 text-warning">策略落地失败 {device.featErrs!.length} 项</Badge>}{device.alerts > 0 && <Badge variant="destructive">{device.alerts} 告警</Badge>}</span>
            <span className="block text-xs text-muted-foreground">{device.label}</span>
          </span>
        </div>
      </TableCell>
      <TableCell className="font-mono text-xs text-muted-foreground">{device.ip}</TableCell>
      <TableCell><Badge variant={device.online ? "outline" : "secondary"} className={device.online ? "gap-1.5 border-success/25 bg-success/5 text-success" : "gap-1.5 text-muted-foreground"}><span className={`size-1.5 rounded-full ${device.online ? "bg-success" : "bg-muted-foreground/50"}`} />{device.online ? "在线" : "离线"}</Badge></TableCell>
      <TableCell>
        <Badge variant={device.helperOn ? "outline" : "secondary"} className={device.helperOn ? "border-success/25 bg-success/5 text-success" : "text-muted-foreground"}>
          {device.helperOn ? "已启用" : "已停用"}
        </Badge>
      </TableCell>
      <TableCell className="tabular-nums text-muted-foreground">{enabledCount}<span className="text-muted-foreground/60">/{featureTotal}</span>{plannedCount > 0 && <span className="ml-1 text-[10px] text-muted-foreground/60">+{plannedCount} 规划中</span>}</TableCell>
      <TableCell className="text-xs text-muted-foreground">{device.lastSeen}</TableCell>
      <TableCell className="pr-4 text-right" onClick={(event) => event.stopPropagation()}>
        {device.pending ? (
          <span className="inline-flex gap-1.5">
            <Button variant="default" size="sm" onClick={() => void run(() => api.approveDevices([device.id]), () => `已批准 ${device.name} 加入课堂`)}>批准</Button>
            <Button variant="ghost" size="sm" className="text-destructive" onClick={() => void run(() => api.rejectDevices([device.id]), () => `已拒绝 ${device.name}`)}>拒绝</Button>
          </span>
        ) : (
          <span className="inline-flex gap-1.5">
            <Button variant="outline" size="sm" onClick={onConfigure}>配置</Button>
            <Button
              variant="ghost"
              size="sm"
              className="text-destructive"
              onClick={() => {
                if (!window.confirm(`确定删除设备 ${device.name}？在线设备会先尝试解锁/停广播再删除，重连后需重新审批。若该机当前锁定且不在线，删除后需现场紧急解锁（Ctrl+Alt+Shift+F12×3）恢复。`)) return;
                void run(() => api.removeDevices([device.id]), () => `已删除 ${device.name}`);
              }}
            >删除</Button>
          </span>
        )}
      </TableCell>
    </TableRow>
    {expanded && (
      <TableRow className="bg-muted/20 hover:bg-muted/20">
        <TableCell colSpan={8} className="px-6 py-4">
          <div className="grid gap-4 md:grid-cols-[minmax(0,1fr)_auto]">
            <div className="grid gap-x-8 gap-y-3 sm:grid-cols-3">
              <Detail label="MAC 地址" value={device.mac} mono />
              <div><p className="text-xs text-muted-foreground">客户端版本</p><p className="mt-1 flex items-center gap-2 text-sm font-medium">v{device.version}{device.version !== "0.0.2" && <Badge variant="outline" className="border-warning/30 bg-warning/5 text-warning">可升级</Badge>}</p></div>
              <div>
                <p className="text-xs text-muted-foreground">已启用功能</p>
                <div className="mt-1.5 flex flex-wrap gap-1.5">
                  {activeFeatures.length ? activeFeatures.map((name) => <Badge key={name} variant="secondary" className="font-normal">{name}</Badge>) : <span className="text-xs text-muted-foreground">未启用功能</span>}
                </div>
              </div>
            </div>
            <div className="flex flex-wrap items-start gap-2 md:justify-end">
              <Button variant={device.helperOn ? "outline" : "default"} size="sm" onClick={() => void run(() => api.setHelper([device.id], !device.helperOn), (count) => `${device.name}：已向 ${count} 台下发${device.helperOn ? "停用" : "启用"}`)}>
                {device.helperOn ? <ShieldOff /> : <ShieldCheck />}{device.helperOn ? "停用防护" : "启用防护"}
              </Button>
              <Button variant="outline" size="sm" onClick={onConfigure}><SlidersHorizontal />配置功能</Button>
              <Button variant="ghost" size="sm" onClick={() => void run(() => api.applyTemplate([device.id], allFeatureKeys), () => `${device.name}：已套用全量模板`)}>套用全量模板</Button>
            </div>
          </div>
        </TableCell>
      </TableRow>
    )}
  </>;
}

function Detail({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div><p className="text-xs text-muted-foreground">{label}</p><p className={`mt-1 text-sm ${mono ? "font-mono" : "font-medium"}`}>{value || "—"}</p></div>;
}

function FeatureDialog({ state, onOpenChange }: { state: FeatureDialogState; onOpenChange: (open: boolean) => void }) {
  const catalog = useLabStore((store) => store.catalog);
  const [checked, setChecked] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const openedState = state;
  const open = Boolean(state);
  const featureKeys = catalog.flatMap((group) => group.features.filter((f) => f.ready).map((feature) => feature.key));

  // Reset the checklist whenever a new device/template target is opened.
  const identity = state?.mode === "single" ? state.device.id : state?.mode === "template" ? state.ids.join("|") : "closed";
  useEffect(() => {
    if (state?.mode === "single") setChecked([...state.device.features]);
    else if (state?.mode === "template") setChecked([]);
  // The identity intentionally tracks the selected target, not the device object reference.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity]);

  const toggle = (key: string, on: boolean) => setChecked((current) => on ? [...new Set([...current, key])] : current.filter((item) => item !== key));
  const apply = async () => {
    if (!openedState) return;
    // 只下发真正可执行的功能键——规划中项即使残留勾选也不发。
    const effective = checked.filter((key) => featureKeys.includes(key));
    setBusy(true);
    try {
      if (openedState.mode === "single") {
        await api.setFeatures(openedState.device.id, effective);
        toast.success(`${openedState.device.name}：已保存 ${effective.length} 项功能`);
      } else {
        const count = await api.applyTemplate(openedState.ids, effective);
        toast.success(`模板已应用到 ${count} 台设备（${effective.length} 项）`);
      }
      onOpenChange(false);
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "保存配置失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl gap-0 p-0">
        <DialogHeader className="border-b px-5 py-4 pr-12">
          <DialogTitle>{state?.mode === "single" ? `配置功能 · ${state.device.name}` : "应用功能模板"}</DialogTitle>
          <DialogDescription>{state?.mode === "single" ? `${state.device.label} · ${state.device.ip}` : `将选中的功能应用到 ${state?.ids.length ?? 0} 台设备`}</DialogDescription>
        </DialogHeader>
        <div className="flex items-center justify-between gap-3 border-b bg-muted/20 px-5 py-2.5">
          <p className="text-xs text-muted-foreground">已选择 {checked.length} / {featureKeys.length} 项</p>
          <div className="flex gap-1">
            <Button variant="ghost" size="xs" onClick={() => setChecked(featureKeys)}>全选</Button>
            <Button variant="ghost" size="xs" onClick={() => setChecked([])}>清空</Button>
          </div>
        </div>
        <ScrollArea className="max-h-[min(65vh,560px)] px-5 py-4">
          <FieldGroup className="gap-5">
            {catalog.map((group) => (
              <FieldSet key={group.title} className="gap-2">
                <div className="flex items-center justify-between">
                  <FieldTitle className="text-sm">{group.title}</FieldTitle>
                  <span className="text-xs text-muted-foreground">{group.features.filter((feature) => checked.includes(feature.key)).length}/{group.features.length}</span>
                </div>
                <div className="grid gap-1 sm:grid-cols-2">
                  {group.features.map((feature) => (
                    <Label key={feature.key} className={`items-start rounded-lg p-2.5 ${feature.ready ? "hover:bg-muted/50" : "opacity-55"}`}>
                      <Checkbox disabled={!feature.ready} checked={feature.ready && checked.includes(feature.key)} onCheckedChange={(value) => toggle(feature.key, value === true)} />
                      <span className="grid min-w-0 gap-1">
                        <span className="flex items-center gap-1.5 text-sm font-medium">{feature.name}{!feature.ready && <Badge variant="outline" className="text-[10px] text-muted-foreground">规划中</Badge>}</span>
                        <span className="text-xs text-muted-foreground">{feature.desc}</span>
                      </span>
                    </Label>
                  ))}
                </div>
              </FieldSet>
            ))}
          </FieldGroup>
        </ScrollArea>
        <DialogFooter className="px-5 py-3">
          <Button variant="outline" onClick={() => onOpenChange(false)}>取消</Button>
          <Button onClick={() => void apply()} disabled={busy}>{busy ? "正在保存…" : state?.mode === "single" ? "保存配置" : "应用到设备"}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
