import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// In dev, proxy the hub API + WebSocket so the front always talks to same-origin
// /api and /ws (exactly like nginx does in the cluster). Override the hub target
// with K8SHARK_HUB=http://host:port when running `npm run dev`.
const hub = process.env.K8SHARK_HUB || "http://localhost:8898";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: hub, changeOrigin: true },
      "/ws": { target: hub.replace("http", "ws"), ws: true },
    },
  },
  build: { outDir: "dist" },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
