import { fileURLToPath } from "url";
import path from "path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

const __dirname = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react({ jsxRuntime: "automatic" })],
  test: {
    environment: "happy-dom",
    globals: true,
    exclude: ["node_modules/**", ".next/**", "out/**"],
    passWithNoTests: true,
    setupFiles: ["./src/test/setup.ts"],
  },
  resolve: {
    alias: [
      // Mirror tsconfig's `@/*` -> `./src/*`. Kept as an explicit regex so it
      // cannot accidentally swallow scoped packages.
      { find: /^@\//, replacement: path.resolve(__dirname, "./src") + "/" },
    ],
  },
});
