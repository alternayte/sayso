import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { getGuidanceOptions, getGuidanceQueryKey, listRepositoriesOptions } from "@/client/@tanstack/react-query.gen";
import { putGuidance } from "@/client/sdk.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { GuidanceEditor, PromptPreview } from "@/components/guidance";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/guidance")({
  beforeLoad: requireRole("admin"),
  loader: ({ context }) => {
    void context.queryClient.prefetchQuery(getGuidanceOptions());
    void context.queryClient.prefetchQuery(listRepositoriesOptions());
  },
  component: Guidance,
});

function Guidance() {
  const q = useQuery(getGuidanceOptions());
  const repos = useQuery(listRepositoriesOptions());
  const [repo, setRepo] = useState("");
  const selected = repo || repos.data?.[0]?.full_name || "";
  return (
    <>
      <PageHead title={t("guidance.title")} hint={t("guidance.hint")} />
      <div className="grid gap-5">
        <Card>
          <CardHeader>
            <CardTitle>{t("guidance.global")}</CardTitle>
          </CardHeader>
          <div className="p-4">
            {q.isPending ? (
              <Skeleton className="h-40 w-full" />
            ) : q.error ? (
              <ErrorAlert error={q.error} onRetry={() => void q.refetch()} />
            ) : (
              <GuidanceEditor
                id="guidance-global"
                limit={8000}
                queryKey={getGuidanceQueryKey()}
                data={q.data}
                canEdit
                save={async (text) => (await putGuidance({ body: { text }, throwOnError: true })).data}
              />
            )}
          </div>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle>{t("guidance.preview")}</CardTitle>
          </CardHeader>
          <div className="grid gap-3 p-4">
            <p className="t-hint max-w-[640px]">{t("guidance.preview.hint")}</p>
            <label className="grid max-w-[360px] gap-1.5">
              <span className="t-label">{t("guidance.preview.repository")}</span>
              <select className="control" value={selected} onChange={(e) => setRepo(e.target.value)}>
                {(repos.data ?? []).map((r) => (
                  <option key={r.full_name} value={r.full_name}>
                    {r.full_name}
                  </option>
                ))}
              </select>
            </label>
            <PromptPreview repository={selected} />
          </div>
        </Card>
      </div>
    </>
  );
}
