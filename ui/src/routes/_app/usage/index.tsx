import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { getUsageOptions } from "@/client/@tanstack/react-query.gen";
import { ErrorAlert, PageHead, Segmented } from "@/components/app-ui";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";
import { useTween } from "@/motion";

export const Route = createFileRoute("/_app/usage/")({ beforeLoad: requireRole("maintainer"), component: Usage });

type Group = "month" | "repository" | "profile" | "round";

function Money({ value }: { value: number }) {
  return <span className="t-value tabular">${useTween(value).toFixed(4)}</span>;
}

function Usage() {
  const [group, setGroup] = useState<Group>("month");
  const q = useQuery({ ...getUsageOptions({ query: { group } }), placeholderData: keepPreviousData });
  const total = (q.data ?? []).reduce((s, r) => s + r.cost_usd, 0);
  return (
    <>
      <PageHead title={t("usage.title")} hint={t("usage.hint")} />
      <div className="mb-4 flex flex-wrap items-center gap-4">
        <Segmented
          label={t("usage.group")}
          options={(["month", "repository", "profile", "round"] as const).map((g) => ({ value: g, label: t(`usage.group.${g}`) }))}
          value={group}
          onChange={setGroup}
        />
        <span className="t-hint">
          {t("usage.total")} <Money value={total} />
        </span>
      </div>
      {q.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : q.error ? (
        <ErrorAlert error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <Card className="relative overflow-x-auto">
          {q.isFetching ? <div className="progress-line" role="progressbar" aria-label={t("loading.list")} /> : null}
          <Table aria-label={t("usage.title")}>
            <TableHeader>
              <TableRow>
                <TableHead>{t(`usage.group.${group}`)}</TableHead>
                <TableHead className="text-right">{t("usage.tokens_in")}</TableHead>
                <TableHead className="text-right">{t("usage.tokens_out")}</TableHead>
                <TableHead className="text-right">{t("usage.cost")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {q.data.map((r) => (
                <TableRow key={r.key} data-key={r.key}>
                  <TableCell className="t-value">{r.label || r.key}</TableCell>
                  <TableCell className="tabular text-right">{r.tokens_in.toLocaleString()}</TableCell>
                  <TableCell className="tabular text-right">{r.tokens_out.toLocaleString()}</TableCell>
                  <TableCell className="text-right" data-cost={r.cost_usd.toFixed(6)}>
                    <Money value={r.cost_usd} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
    </>
  );
}
