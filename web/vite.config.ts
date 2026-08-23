/// <reference types="vitest/config" />
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    // shadcn's components import each other as "@/components/ui/...", which is
    // what makes a copied component paste in unedited.
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: {
    // The bundle is embedded into the Go binary, so it goes somewhere the
    // embed directive can reach.
    outDir: "dist",
    emptyOutDir: true,
    // No source maps in the shipped artifact: they would roughly triple the
    // binary size for something only useful during development.
    sourcemap: false,
  },
  server: {
    // During development the dashboard runs on Vite and proxies the API to a
    // locally running mcpd.
    proxy: {
      "/api": "http://127.0.0.1:80",
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    // The dashboard is the only thing under test; the built bundle is an
    // artifact and node_modules is somebody else's problem.
    exclude: ["node_modules", "dist"],
  },
});
