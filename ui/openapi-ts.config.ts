import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "openapi.json",
  output: { path: "src/client", postProcess: ["prettier"] },
  plugins: [
    "@hey-api/client-fetch",
    "@hey-api/typescript",
    "@hey-api/sdk",
    { name: "@tanstack/react-query", queryOptions: true, mutationOptions: true, queryKeys: true },
  ],
});
