import { useEffect } from "react";
import { Maximize2, Minimize2, Monitor, Radio, X } from "lucide-react";
import { api, onClassroomChanged } from "../api";
import { useLabStore } from "../store";

const qualityLabels: Record<string, string> = {
  perfect: "极好",
  better: "较好",
  normal: "普通",
  low: "低质",
};

// 管理端广播悬浮条（仿极域顶栏）：拖动区域 + 模式切换/画质/管理端/退出。
// 由 Go 侧 Panel 无边框小窗承载，背景透明，只画一条深色胶囊。
export function CastBar() {
  const classroom = useLabStore((s) => s.classroom);
  const setClassroom = useLabStore((s) => s.setClassroom);

  useEffect(() => {
    document.documentElement.classList.add("dark");
    const off = onClassroomChanged(setClassroom);
    void api.classroomState().then(setClassroom).catch(() => undefined);
    return off;
  }, [setClassroom]);

  const mode = classroom?.castMode ?? "fullscreen";
  const targetIds = classroom?.castTargetIds ?? [];
  const quality = classroom?.castQuality ?? "better";
  const other = mode === "fullscreen" ? "window" : "fullscreen";

  return (
    <div
      className="flex h-screen w-screen items-center gap-1.5 rounded-full bg-neutral-900/92 px-2.5 text-neutral-100 shadow-2xl ring-1 ring-white/15"
      style={{ "--app-region": "drag", background: "rgba(24,26,28,0.94)" } as React.CSSProperties}
    >
      <span className="mr-0.5 flex items-center gap-1.5 pl-1 text-xs font-medium">
        <Radio className="size-3.5 animate-pulse text-emerald-400" />
        广播中 · {targetIds.length} 台
      </span>
      <button
        type="button"
        className="flex h-7 items-center gap-1 rounded-md px-2 text-xs hover:bg-white/10 disabled:opacity-40"
        style={{ "--app-region": "no-drag" } as React.CSSProperties}
        disabled={targetIds.length === 0}
        onClick={() => void api.castStart(other, targetIds).catch(() => undefined)}
        title="切换全屏/窗口"
      >
        {other === "fullscreen" ? <Maximize2 className="size-3.5" /> : <Minimize2 className="size-3.5" />}
        {other === "fullscreen" ? "全屏" : "窗口"}
      </button>
      <select
        className="h-7 rounded-md border-0 bg-white/10 px-1.5 text-xs outline-none"
        style={{ "--app-region": "no-drag" } as React.CSSProperties}
        value={quality}
        onChange={(e) => void api.castSetQuality(e.target.value).catch(() => undefined)}
        title="广播画质"
      >
        {Object.entries(qualityLabels).map(([k, label]) => <option key={k} value={k} className="bg-neutral-900">{label}</option>)}
      </select>
      <button
        type="button"
        className="flex h-7 items-center gap-1 rounded-md px-2 text-xs hover:bg-white/10"
        style={{ "--app-region": "no-drag" } as React.CSSProperties}
        onClick={() => void api.focusAdmin().catch(() => undefined)}
        title="显示管理端"
      >
        <Monitor className="size-3.5" />管理端
      </button>
      <button
        type="button"
        className="ml-auto flex size-7 items-center justify-center rounded-md text-red-300 hover:bg-red-500/20"
        style={{ "--app-region": "no-drag" } as React.CSSProperties}
        onClick={() => void api.castStop().catch(() => undefined)}
        title="退出广播"
      >
        <X className="size-4" />
      </button>
    </div>
  );
}
