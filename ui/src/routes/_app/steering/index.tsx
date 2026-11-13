import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { steeringQueueOptions } from "@/client/@tanstack/react-query.gen";
import type { SteeringQueueData } from "@/client/types.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";
import { sha7, Verdict } from "@/lib/review";

export const Route = createFileRoute("/_app/steering/")({ beforeLoad: requireRole("maintainer"), component: Steering });

type Filter = NonNullable<SteeringQueueData["query"]>;

const signalTypes = ["", "dismissed", "overridden", "conceded", "resolved_unfixed", "human_comment_unflagged", "rerun_requested"] as const;

function Steering() {
  const [f, setF] = useState<Filter>({});
  const q = useQuery({ ...steeringQueueOptions({ query: f }), placeholderData: keepPreviousData });
  const field = (k: keyof Filter, label: string, type = "text") => (
    <div className="grid gap-1.5">
      <label className="t-label" htmlFor={`steer-${k}`}>
        {label}
      </label>
      <Input
        id={`steer-${k}`}
        type={type}
        mono={type === "text"}
        value={(f[k] as string | undefined) ?? ""}
        onChange={(e) => setF({ ...f, [k]: e.target.value || undefined })}
      />
    </div>
  );
  return (
    <>
      <PageHead title={t("steering.title")} hint={t("steering.hint")} />
      <Card className="mb-5 grid grid-cols-[repeat(auto-fill,minmax(12rem,1fr))] gap-3 p-4">
        {field("repository", t("steering.filter.repository"))}
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="steer-skill-source">
            {t("steering.filter.skill_source")}
          </label>
          <select
            id="steer-skill-source"
            className="control"
            value={f.skill_source ?? ""}
            onChange={(e) => setF({ ...f, skill_source: (e.target.value || undefined) as Filter["skill_source"] })}
          >
            <option value="">{t("steering.filter.any")}</option>
            <option value="repository">repository</option>
            <option value="skael">skael</option>
            <option value="builtin">builtin</option>
          </select>
        </div>
        {field("skill_version", t("steering.filter.skill_version"))}
        {field("prompt_version", t("steering.filter.prompt_version"))}
        {field("profile", t("steering.filter.profile"))}
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="steer-signal">
            {t("steering.filter.signal")}
          </label>
          <select
            id="steer-signal"
            className="control"
            value={f.signal_type ?? ""}
            onChange={(e) => setF({ ...f, signal_type: (e.target.value || undefined) as Filter["signal_type"] })}
          >
            {signalTypes.map((s) => (
              <option key={s} value={s}>
                {s || t("steering.filter.any")}
              </option>
            ))}
          </select>
        </div>
        {field("from", t("steering.filter.from"), "date")}
        {field("to", t("steering.filter.to"), "date")}
      </Card>
      {q.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : q.error ? (
        <ErrorAlert error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <Card className="relative overflow-x-auto">
          {q.isFetching ? <div className="progress-line" role="progressbar" aria-label={t("loading.list")} /> : null}
          {q.data.length === 0 ? (
            <p className="t-hint p-4">{t("steering.empty")}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("reviews.pull")}</TableHead>
                  <TableHead>{t("reviews.verdict")}</TableHead>
                  <TableHead>{t("steering.signals")}</TableHead>
                  <TableHead>{t("steering.skill")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {q.data.map((r) => (
                  <TableRow key={r.round_id}>
                    <TableCell>
                      <Link to="/rounds/$id" params={{ id: r.round_id }} className="link t-value">
                        {r.repository}#{r.number} · {sha7(r.head_sha)}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Verdict verdict={r.verdict} />
                    </TableCell>
                    <TableCell>
                      <span className="flex flex-wrap gap-1">
                        {r.signals.map((s) => (
                          <Badge key={s}>{s}</Badge>
                        ))}
                      </span>
                    </TableCell>
                    <TableCell className="t-hint">
                      {r.skill_source} {r.skill_version}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      )}
    </>
  );
}
