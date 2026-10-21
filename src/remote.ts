// headless 管理端（lanhttp）的传输层：页面由 Go 进程服务时，
// __LAB_HEADLESS__ 被注入，api 改走 HTTP+SSE 而不是 mygo bridge。
import { Lab, type ClassroomState, type Device } from "./mygo";

const cfg = (window as { __LAB_HEADLESS__?: { token: string } }).__LAB_HEADLESS__;
export const headless = !!cfg;
const token = cfg?.token ?? "";

async function httpCall<T>(name: string, args: unknown[]): Promise<T> {
  const res = await fetch("/api/call", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-Lab-Token": token },
    body: JSON.stringify({ m: name, a: args }),
  });
  if (!res.ok) throw new Error(`${name} 调用失败（${res.status}）`);
  return (await res.json()) as T;
}

// 与 src/mygo.ts 的 Lab 形状一致：方法名首字母大写即 Go 侧方法名。
export const httpLab = Object.fromEntries(
  Object.keys(Lab).map((key) => [
    key,
    (...args: unknown[]) => httpCall(`Lab.${key.charAt(0).toUpperCase()}${key.slice(1)}`, args),
  ]),
) as typeof Lab;

const listeners: Record<string, Set<(data: never) => void>> = {
  devices: new Set(),
  classroom: new Set(),
};
let stream: EventSource | undefined;

function ensureStream() {
  if (stream || !headless) return;
  stream = new EventSource(`/api/events?k=${token}`);
  stream.onmessage = (e) => {
    try {
      const msg = JSON.parse(e.data as string) as { type: string; data: never };
      listeners[msg.type]?.forEach((l) => l(msg.data));
    } catch { /* 忽略坏帧 */ }
  };
}

export function onRemoteDevices(listener: (d: Device[]) => void): () => boolean | void {
  ensureStream();
  const set = listeners.devices as Set<(d: Device[]) => void>;
  set.add(listener);
  return () => set.delete(listener);
}

export function onRemoteClassroom(listener: (s: ClassroomState) => void): () => boolean | void {
  ensureStream();
  const set = listeners.classroom as Set<(s: ClassroomState) => void>;
  set.add(listener);
  return () => set.delete(listener);
}

// uploadFiles 浏览器选中的分发文件 → POST 到管理端落盘，返回服务器路径供 SendFiles。
export async function uploadFiles(files: File[]): Promise<string[]> {
  const paths: string[] = [];
  for (const f of files) {
    const res = await fetch(`/api/upload?name=${encodeURIComponent(f.name)}&k=${encodeURIComponent(token)}`, {
      method: "POST",
      body: f,
    });
    if (!res.ok) throw new Error(`上传 ${f.name} 失败（${res.status}）`);
    paths.push(((await res.json()) as { path: string }).path);
  }
  return paths;
}

export function clientPackageURL(addr: string): string {
  return `/api/client-package?addr=${encodeURIComponent(addr)}&k=${encodeURIComponent(token)}`;
}
