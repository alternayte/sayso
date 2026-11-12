import path from "node:path";
import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const apiTarget = process.env.VITE_API_TARGET ?? "http://localhost:8080";

export default defineConfig({
  plugins: [tanstackRouter({ target: "react", autoCodeSplitting: true }), react(), tailwindcss()],
  resolve: { alias: { "@": path.resolve(import.meta.dirname, "./src") } },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    assetsDir: "assets",
    // No data: URIs. The CSP default-src 'self' blocks them.
    assetsInlineLimit: 0,
  },
  server: { proxy: { "/api": apiTarget, "/auth": apiTarget } },
});
