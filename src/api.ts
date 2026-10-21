import { isMyGo } from "mygo-runtime";
import { events, Lab, type ClassroomState, type Device } from "./mygo";
import { mockLab } from "./mock";
import { headless, httpLab, onRemoteClassroom, onRemoteDevices } from "./remote";

export { uploadFiles, clientPackageURL } from "./remote";

export const nativeRuntime = isMyGo();
// remoteRuntime：页面由 headless 管理端（lanhttp）服务，走 HTTP+SSE。
export const remoteRuntime = !nativeRuntime && headless;
export const api: typeof Lab = nativeRuntime ? Lab : remoteRuntime ? httpLab : (mockLab as typeof Lab);

export function onDevicesChanged(listener: (devices: Device[]) => void) {
  if (nativeRuntime) return events.devices.on(listener);
  if (remoteRuntime) return onRemoteDevices(listener);
  return mockLab.onDevices(listener);
}

export function onClassroomChanged(listener: (state: ClassroomState) => void) {
  if (nativeRuntime) return events.classroom.on(listener);
  if (remoteRuntime) return onRemoteClassroom(listener);
  return mockLab.onClassroom(listener);
}
