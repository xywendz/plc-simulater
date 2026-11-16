// B/S 模式运行时：HTTP JSON-RPC 门面。
// 仅在 `npm run build:web` / `npm run dev:web` (--mode web) 时进入产物，
// 因此 Web 产物完全不包含 @wailsio/runtime。
import type { RawBridge, RuntimeMode } from "./types";

export const runtimeMode: RuntimeMode = "web";

async function invoke(method: string, args: any[]): Promise<any> {
  let response: Response;
  try {
    response = await fetch(`/api/rpc/${encodeURIComponent(method)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      credentials: "same-origin",
      body: JSON.stringify({ args }),
    });
  } catch (e: any) {
    throw new Error(`无法连接 S7 模拟器服务：${e?.message ?? String(e)}`);
  }
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error: any = new Error(body?.message || `请求失败 (HTTP ${response.status})`);
    error.code = body?.code;
    throw error;
  }
  return body.result;
}

// Proxy 让 api/client.ts 的调用保持与 Wails 绑定完全相同的形状。
export const raw: RawBridge = new Proxy({} as RawBridge, {
  get(_target, prop) {
    if (typeof prop !== "string") return undefined;
    return (...args: any[]) => invoke(prop, args);
  },
});
