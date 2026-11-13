import { defineConfig } from "vitest/config";

export default defineConfig({
  // Review tests live outside the package; they use the vitest globals,
  // because "vitest" does not resolve from that directory.
  test: { include: ["src/**/*.test.ts", "../tests/review/**/*.test.ts"], globals: true },
});
