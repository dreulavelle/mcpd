import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
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
});
