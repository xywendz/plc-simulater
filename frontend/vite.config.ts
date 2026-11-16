import { defineConfig } from "vite";
import { fileURLToPath, URL } from "node:url";

// 两套编译入口：
//   --mode gui (默认) -> dist/     + runtime/gui.ts (Wails IPC)，供 cmd/plc-simulater-gui 嵌入
//   --mode web        -> dist-web/ + runtime/web.ts (HTTP RPC)，供 internal/webui 嵌入
export default defineConfig(({ mode }) => {
  const web = mode === "web";
  const srcDir = fileURLToPath(new URL("./src/", import.meta.url)).replace(/\\/g, "/");
  const runtimeFile = srcDir + (web ? "runtime/web.ts" : "runtime/gui.ts");
  const apiTarget = process.env.VITE_API_TARGET || "http://127.0.0.1:8070";

  return {
    resolve: {
      // 精确匹配必须排在 @/ 通配之前：编译期物理替换运行时实现。
      alias: [
        { find: /^@\/runtime$/, replacement: runtimeFile },
        { find: /^@\//, replacement: srcDir },
      ],
    },
    build: {
      outDir: web ? "dist-web" : "dist",
      // 由构建脚本在构建前清理目录：部分 Windows 环境的删除保护会拦截 Vite 的 emptyOutDir。
      emptyOutDir: false,
      target: "esnext",
    },
    server: {
      port: 34115,
      strictPort: true,
      // Web 开发模式由 Vite 提供 HMR，RPC 请求代理到 Go 进程。
      proxy: web
        ? {
            "/api": { target: apiTarget, changeOrigin: true },
          }
        : undefined,
    },
  };
});
