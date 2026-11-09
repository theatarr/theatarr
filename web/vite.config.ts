import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";
import { PRODUCT_NAME } from "./src/brand";

// The bundle is embedded in the Go binary (internal/webui). Hashed names under assets/ are cached for a year.
// Nothing is inlined: the CSP is default-src 'self' (no data: fonts, no inline scripts or styles).
export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    // The product name lives in src/brand.ts only; index.html gets it here.
    { name: "brand", transformIndexHtml: (html: string) => html.replaceAll("%PRODUCT_NAME%", PRODUCT_NAME) },
  ],
  resolve: { alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) } },
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    assetsInlineLimit: 0,
    target: "es2022",
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks: {
          react: ["react", "react-dom", "react-router"],
          query: ["@tanstack/react-query"],
          radix: ["radix-ui"],
        },
      },
    },
  },
  server: {
    // npm run dev: proxy the API and the classic pages to a local theatarr serve.
    proxy: Object.fromEntries(
      ["/api", "/classic", "/static", "/radarr", "/sonarr", "/healthz", "/readyz"].map((p) => [
        p,
        { target: process.env.THEATARR_DEV_TARGET ?? "http://127.0.0.1:7700", changeOrigin: false },
      ]),
    ),
  },
  test: { environment: "node", include: ["src/**/*.test.{ts,tsx}"] },
});
