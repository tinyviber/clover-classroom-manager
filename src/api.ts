import { isMyGo } from "mygo-runtime";
import { events, Lab, type ClassroomState, type Device } from "./mygo";
import { mockLab } from "./mock";

export const nativeRuntime = isMyGo();
export const api: typeof Lab = nativeRuntime ? Lab : (mockLab as typeof Lab);

export function onDevicesChanged(listener: (devices: Device[]) => void) {
  return nativeRuntime ? events.devices.on(listener) : mockLab.onDevices(listener);
}

export function onClassroomChanged(listener: (state: ClassroomState) => void) {
  return nativeRuntime ? events.classroom.on(listener) : mockLab.onClassroom(listener);
}
