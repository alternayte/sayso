import { useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { getDiagnosticsOptions } from "@/client/@tanstack/react-query.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { Badge } from "@/components/ui/badge";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";
import { Card } from "@/components/ui/card";

export const Route = createFileRoute("/_app/diagnostics")({ beforeLoad: requireRole("admin"), component: Diagnostics });

function Diagnostics() {
  const diag = useQuery({ ...getDiagnosticsOptions(), staleTime: 0 });
  return (
    <>
      <PageHead
        title={t("diag.title")}
        hint={t("diag.hint")}
        actions={
          <Button onClick={() => diag.refetch()} disabled={diag.isFetching}>
            {t("diag.run")}
          </Button>
        }
      />
      <ErrorAlert error={diag.error} onRetry={() => diag.refetch()} />
      <Card>
        <ul className="divide-y divide-hairline">
          {diag.isPending
            ? [0, 1, 2, 3].map((i) => (
                <li key={i} className="px-4 py-3">
                  <Skeleton className="h-4 w-2/3" />
                </li>
              ))
            : (diag.data?.checks ?? []).map((c) => (
                <li key={c.name} className="flex flex-wrap items-start gap-3 px-4 py-3">
                  <Badge dot tone={c.ok ? "success" : "danger"}>
                    {c.ok ? t("diag.pass") : t("diag.fail")}
                  </Badge>
                  <span className="t-value w-36 text-ink">{c.name}</span>
                  <span className="min-w-0 flex-1 break-words">{c.detail}</span>
                  <span className="t-hint t-value">{c.ms} ms</span>
                </li>
              ))}
        </ul>
      </Card>
    </>
  );
}
