import { defineConfig } from "vite";

// Build into web/dist (embedded by the Go server). In dev, proxy /api to the
// running `gospr sandbox run` server so the SPA and API share an origin.
export default defineConfig({
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:9060",
    },
  },
});
