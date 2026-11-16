// GUI 本地模式运行时：通过 Wails 生成的绑定调用 Go 后端。
// 仅在 `npm run build` / `npm run dev` (--mode gui) 时进入产物。
import { raw as wailsRaw } from "../api/bridge";
import type { RawBridge, RuntimeMode } from "./types";

export const runtimeMode: RuntimeMode = "gui";
export const raw: RawBridge = wailsRaw;
