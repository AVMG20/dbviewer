import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";
import wails from "@wailsio/runtime/plugins/vite";
import { fileURLToPath, URL } from "node:url";

// Browser dev mode (`wails3 task dev:browser`): the Go app runs headless in Wails
// server mode on WAILS_SERVER_PORT (9246) and Vite proxies the Wails runtime
// endpoints to it, so http://localhost:9245 works in any browser with live bindings.
const backend = `http://127.0.0.1:${process.env.WAILS_SERVER_PORT || 9246}`;

export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
    proxy: {
      "/wails": {
        target: backend,
        changeOrigin: true,
        ws: true,
        // The Wails server enforces same-origin; present the proxied request as its own.
        configure: (proxy) => {
          proxy.on("proxyReq", (req) => req.setHeader("origin", backend));
          proxy.on("proxyReqWs", (req) => req.setHeader("origin", backend));
        },
      },
    },
  },
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  plugins: [vue(), tailwindcss(), wails("./bindings")],
});
