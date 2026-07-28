import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The SPA builds into ../web/dist, which the Go binary embeds via go:embed.
// In dev, /api is proxied to the running Autotaggerr service on :8080.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../web/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
