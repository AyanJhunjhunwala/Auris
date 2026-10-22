import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

// The Go API runs on :8080 (AURIS_ADDR). Proxying /api keeps the browser on
// one origin in dev, so no CORS setup is needed.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": { target: process.env.AURIS_API ?? "http://localhost:8080", changeOrigin: true } },
  },
});
