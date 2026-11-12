import { client } from "@/client/client.gen";
import { t } from "@/lib/copy";

/** ApiError carries the status and the detail of a Huma error response. */
export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

client.setConfig({ baseUrl: "", credentials: "same-origin", throwOnError: true });

client.interceptors.error.use((error, response) => {
  const detail = typeof error === "object" && error !== null && "detail" in error ? String((error as { detail: unknown }).detail) : "";
  return new ApiError(response?.status ?? 0, detail || t("error.generic"));
});

/** isStatus reports whether an error is an API error with this status. */
export function isStatus(error: unknown, status: number) {
  return error instanceof ApiError && error.status === status;
}
