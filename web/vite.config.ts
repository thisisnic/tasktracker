import { svelte, vitePreprocess } from "@sveltejs/vite-plugin-svelte";
import { closeSync, openSync } from "node:fs";
import { defineConfig, type Plugin } from "vitest/config";

// go:embed needs the dist directory to exist even before the first web
// build, so a .gitkeep is tracked there; emptying the directory removes it
// and this puts it back.
function keepGitkeep(): Plugin {
  return {
    name: "keep-gitkeep",
    closeBundle() {
      closeSync(openSync("../internal/server/dist/.gitkeep", "a"));
    },
  };
}

// The build lands in the Go package that embeds it. `bun run dev` serves
// the app on its own port and proxies /api to a running tasktracker.
const backend = process.env.TASKTRACKER_DEV_BACKEND ?? "http://127.0.0.1:7344";

export default defineConfig({
  // Asset paths are absolute, so the page loads from any path the
  // server hands it to, such as a bookmark deeper than /.
  base: "/",
  plugins: [svelte({ preprocess: vitePreprocess({ script: true }) }), keepGitkeep()],
  build: {
    outDir: "../internal/server/dist",
    emptyOutDir: true,
  },
  optimizeDeps: {
    exclude: ["@kenn-io/kit-ui"],
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    proxy: { "/api": { target: backend, changeOrigin: false } },
  },
  // The row and landing rules are plain modules, tested without a DOM.
  test: {
    include: ["src/**/*.test.ts"],
    environment: "node",
  },
});
