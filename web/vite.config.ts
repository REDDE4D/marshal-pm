import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build output lands in the Go package's dist dir, which is go:embed-ed.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/dashboard/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      // For `npm run dev` against a locally-running server (self-signed TLS).
      "/api": { target: "https://localhost:9001", changeOrigin: true, secure: false },
    },
  },
});
