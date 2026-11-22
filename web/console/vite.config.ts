import react from "@vitejs/plugin-react";
import type { ProxyOptions } from "vite";
import { defineConfig } from "vitest/config";

declare const process: { env: Record<string, string | undefined> };

const target = process.env["OHS_DEV_TARGET"] ?? "http://localhost:8080";
const targetOrigin = new URL(target).origin;

// The session middleware refuses a foreign Origin, so the dev proxy presents
// the origin of the server it forwards to.
interface ProxyHooks {
  on: (event: "proxyReq", handler: (req: { setHeader: (name: string, value: string) => void }) => void) => void;
}

const proxy = {
  target,
  changeOrigin: true,
  configure: (p: ProxyHooks) => {
    p.on("proxyReq", (req) => {
      req.setHeader("Origin", targetOrigin);
    });
  },
};

// vite types the proxy server by a package it does not ship declarations for.
const proxyOptions = proxy as unknown as ProxyOptions;

export default defineConfig({
  plugins: [react()],
  base: "/console/",
  build: {
    outDir: "../../internal/console/dist",
    emptyOutDir: true,
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    sourcemap: false,
    rollupOptions: {
      output: {
        entryFileNames: "assets/[name]-[hash].js",
        chunkFileNames: "assets/[name]-[hash].js",
        assetFileNames: "assets/[name]-[hash][extname]",
      },
    },
  },
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/v1": proxyOptions,
      "/console/session": proxyOptions,
      "/healthz": proxyOptions,
      "/readyz": proxyOptions,
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
  },
});
