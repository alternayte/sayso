import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "openapi.json",
  // The run stream is server-sent events; the run page reads it with EventSource.
  parser: { filters: { operations: { exclude: ["GET /api/v1/runs/{id}/stream"] } } },
  output: { path: "src/client", postProcess: ["prettier"] },
  plugins: [
    "@hey-api/client-fetch",
    "@hey-api/typescript",
    "@hey-api/sdk",
    { name: "@tanstack/react-query", queryOptions: true, mutationOptions: true, queryKeys: true },
  ],
});
