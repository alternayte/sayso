import { defineConfig, devices } from "@playwright/test";
import { stack } from "./stack";

// The e2e stack is one Compose project. Tests share its database, so they run one at a time.
export default defineConfig({
  testDir: "./specs",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"], ["json", { outputFile: process.env.E2E_REPORT ?? "../../tmp/reports/ui.json" }]],
  use: { baseURL: stack.baseURL, trace: "retain-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
