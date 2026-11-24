import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  base: "/assets/",
  publicDir: false,
  build: {
    outDir: "dist",
    emptyOutDir: true,
    cssCodeSplit: false,
    rollupOptions: {
      output: {
        entryFileNames: "app.js",
        assetFileNames: "[name][extname]",
        inlineDynamicImports: true,
      },
    },
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:19992",
      "/assets/echarts.min.js": "http://127.0.0.1:19992",
    },
  },
  test: {
    environment: "jsdom",
    testTimeout: 20000,
    setupFiles: ["./src/test/setup.ts"],
    restoreMocks: true,
  },
});
