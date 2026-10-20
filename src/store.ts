import { create } from "zustand";
import type { ClassroomState, ClientStatus, Device, FeatureGroup } from "./mygo";

export type DeviceFilter = "all" | "online" | "offline" | "alert";
export type MainView = "devices" | "classroom";
export type ClassroomPane = "wall" | "cast" | "signin" | "files" | "interact";
export type ThemePreference = "light" | "dark" | "system";

const themeStorageKey = "lab-assistant-theme";

function readThemePreference(): ThemePreference {
  try {
    const value = localStorage.getItem(themeStorageKey);
    if (value === "light" || value === "dark" || value === "system") return value;
  } catch {
    // Storage can be unavailable in restricted browser contexts.
  }
  return "system";
}

function readResolvedTheme(theme: ThemePreference): "light" | "dark" {
  if (theme !== "system") return theme;
  return typeof window !== "undefined" && window.matchMedia("(prefers-color-scheme: dark)").matches
    ? "dark"
    : "light";
}

type LabStore = {
  devices: Device[];
  catalog: FeatureGroup[];
  classroom: ClassroomState | null;
  clientStatus: ClientStatus | null;
  loading: boolean;
  loadError: string;
  lastScan: string;
  filter: DeviceFilter;
  query: string;
  selectedIds: string[];
  expandedIds: string[];
  view: MainView;
  classroomPane: ClassroomPane;
  pickedPaths: string[];
  demoSourceId: string;
  theme: ThemePreference;
  resolvedTheme: "light" | "dark";
  setDevices: (devices: Device[]) => void;
  setCatalog: (catalog: FeatureGroup[]) => void;
  setClassroom: (classroom: ClassroomState) => void;
  setClientStatus: (status: ClientStatus) => void;
  setLoading: (loading: boolean) => void;
  setLoadError: (message: string) => void;
  setLastScan: (value: string) => void;
  setFilter: (filter: DeviceFilter) => void;
  setQuery: (query: string) => void;
  toggleSelected: (id: string) => void;
  setVisibleSelected: (ids: string[], checked: boolean) => void;
  clearSelection: () => void;
  toggleExpanded: (id: string) => void;
  setView: (view: MainView) => void;
  setClassroomPane: (pane: ClassroomPane) => void;
  setPickedPaths: (paths: string[]) => void;
  setDemoSource: (id: string) => void;
  setTheme: (theme: ThemePreference) => void;
  setResolvedTheme: (theme: "light" | "dark") => void;
};

const initialTheme = readThemePreference();

export const useLabStore = create<LabStore>()((set) => ({
  devices: [],
  catalog: [],
  classroom: null,
  clientStatus: null,
  loading: true,
  loadError: "",
  lastScan: "",
  filter: "all",
  query: "",
  selectedIds: [],
  expandedIds: [],
  view: "devices",
  classroomPane: "wall",
  pickedPaths: [],
  demoSourceId: "",
  theme: initialTheme,
  resolvedTheme: readResolvedTheme(initialTheme),
  setDevices: (devices) => set({ devices }),
  setCatalog: (catalog) => set({ catalog }),
  setClassroom: (classroom) => set({ classroom }),
  setClientStatus: (clientStatus) => set({ clientStatus }),
  setLoading: (loading) => set({ loading }),
  setLoadError: (loadError) => set({ loadError }),
  setLastScan: (lastScan) => set({ lastScan }),
  setFilter: (filter) => set({ filter }),
  setQuery: (query) => set({ query }),
  toggleSelected: (id) => set((state) => ({
    selectedIds: state.selectedIds.includes(id)
      ? state.selectedIds.filter((item) => item !== id)
      : [...state.selectedIds, id],
  })),
  setVisibleSelected: (ids, checked) => set((state) => ({
    selectedIds: checked
      ? [...new Set([...state.selectedIds, ...ids])]
      : state.selectedIds.filter((id) => !ids.includes(id)),
  })),
  clearSelection: () => set({ selectedIds: [] }),
  toggleExpanded: (id) => set((state) => ({
    expandedIds: state.expandedIds.includes(id)
      ? state.expandedIds.filter((item) => item !== id)
      : [...state.expandedIds, id],
  })),
  setView: (view) => set({ view }),
  setClassroomPane: (classroomPane) => set({ classroomPane }),
  setPickedPaths: (pickedPaths) => set({ pickedPaths }),
  setDemoSource: (demoSourceId) => set({ demoSourceId }),
  setTheme: (theme) => {
    try {
      localStorage.setItem(themeStorageKey, theme);
    } catch {
      // Keep the in-memory preference if storage is unavailable.
    }
    set({ theme });
  },
  setResolvedTheme: (resolvedTheme) => set({ resolvedTheme }),
}));

export function filterDevices(devices: Device[], filter: DeviceFilter, query: string) {
  const needle = query.trim().toLowerCase();
  return devices.filter((device) => {
    if (filter === "online" && !device.online) return false;
    if (filter === "offline" && device.online) return false;
    if (filter === "alert" && device.alerts === 0) return false;
    return !needle || [device.name, device.label, device.ip].some((value) => value.toLowerCase().includes(needle));
  });
}

export function targetDeviceIds(devices: Device[], selectedIds: string[]) {
  const online = devices.filter((device) => device.online);
  return selectedIds.length === 0
    ? online.map((device) => device.id)
    : online.filter((device) => selectedIds.includes(device.id)).map((device) => device.id);
}
