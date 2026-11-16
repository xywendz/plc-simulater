// 两种编译模板共用的运行时契约。
// Vite 在编译期把 @/runtime 物理替换为 runtime/gui.ts 或 runtime/web.ts。

export type RuntimeMode = "gui" | "web";

// 与 Wails 绑定一致的门面形状：方法名 → 返回 Promise 的函数。
export type RawBridge = Record<string, (...args: any[]) => Promise<any>>;
