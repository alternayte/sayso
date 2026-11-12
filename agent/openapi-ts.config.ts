import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "../schemas/runner.openapi.json",
  output: { path: "src/protocol" },
  plugins: ["@hey-api/typescript"],
});
