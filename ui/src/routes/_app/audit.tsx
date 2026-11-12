import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { listAuditActionsOptions } from "@/client/@tanstack/react-query.gen";
import { listAudit } from "@/client/sdk.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Card } from "@/components/ui/card";

export const Route = createFileRoute("/_app/audit")({ beforeLoad: requireRole("admin"), component: Audit });

const pageSize = 50;

function Audit() {
  const [action, setAction] = useState("");
  const actions = useQuery(listAuditActionsOptions());
  const log = useInfiniteQuery({
    queryKey: ["audit", action],
    initialPageParam: 0,
    queryFn: async ({ pageParam }) =>
      (await listAudit({ query: { action: action || undefined, before: pageParam || undefined, limit: pageSize } })).data ?? [],
    getNextPageParam: (last) => (last.length === pageSize ? last[last.length - 1]!.id : undefined),
    placeholderData: (prev) => prev,
  });
  const rows = log.data?.pages.flat() ?? [];
  return (
    <>
      <PageHead
        title={t("audit.title")}
        hint={t("audit.hint")}
        actions={
          <label className="flex items-center gap-2">
            <span className="t-label">{t("audit.filter")}</span>
            <select className="control w-auto" value={action} onChange={(e) => setAction(e.target.value)}>
              <option value="">{t("audit.all")}</option>
              {(actions.data ?? []).map((a) => (
                <option key={a}>{a}</option>
              ))}
            </select>
          </label>
        }
      />
      <ErrorAlert error={log.error} onRetry={() => log.refetch()} />
      <Card className="relative overflow-x-auto">
        {log.isFetching && !log.isPending ? <div className="absolute inset-x-0 top-0 h-0.5 bg-accent fade-in" aria-hidden /> : null}
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("audit.when")}</TableHead>
              <TableHead>{t("audit.actor")}</TableHead>
              <TableHead>{t("audit.action")}</TableHead>
              <TableHead>{t("audit.target")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {log.isPending ? (
              <TableRow>
                <TableCell colSpan={4}>
                  <Skeleton className="h-4 w-full" />
                </TableCell>
              </TableRow>
            ) : rows.length === 0 ? (
              <TableRow>
                <TableCell colSpan={4} className="t-hint">
                  {t("audit.empty")}
                </TableCell>
              </TableRow>
            ) : (
              rows.map((r) => (
                <TableRow key={r.id} data-action={r.action}>
                  <TableCell className="t-hint whitespace-nowrap">{new Date(r.at).toLocaleString()}</TableCell>
                  <TableCell className="t-value whitespace-nowrap">{r.actor_login || r.actor_kind}</TableCell>
                  <TableCell className="t-value text-ink">{r.action}</TableCell>
                  <TableCell className="t-value">{r.target}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </Card>
      {log.hasNextPage ? (
        <Button className="mt-4" onClick={() => log.fetchNextPage()} disabled={log.isFetchingNextPage}>
          {t("audit.more")}
        </Button>
      ) : null}
    </>
  );
}
