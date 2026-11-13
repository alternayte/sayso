import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type React from "react";
import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { dismissFindingMutation, getReviewOptions } from "@/client/@tanstack/react-query.gen";
import type { FindingBody, ReviewBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Input } from "@/components/ui/input";
import { atLeast } from "@/lib/session";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t, type CopyKey } from "@/lib/copy";
import { RoundStatus, sha7, Verdict } from "@/lib/review";

export const Route = createFileRoute("/_app/reviews/$owner/$name/$number")({
  loader: ({ context, params }) =>
    void context.queryClient.prefetchQuery(
      getReviewOptions({ path: { owner: params.owner, name: params.name, number: Number(params.number) } }),
    ),
  component: Review,
});

/**
 * DismissControl dismisses a finding at once (optimistic). On an error the row returns to open,
 * the control shakes and an inline error shows (§16.5).
 */
function DismissControl({ f, queryKey }: { f: FindingBody; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [shake, setShake] = useState(0);
  const dismiss = useMutation({
    ...dismissFindingMutation(),
    onMutate: async () => {
      await qc.cancelQueries({ queryKey });
      const prev = qc.getQueryData<ReviewBody>(queryKey);
      qc.setQueryData<ReviewBody>(queryKey, (r) =>
        r ? { ...r, findings: r.findings.map((x) => (x.id === f.id ? { ...x, status: "dismissed" } : x)) } : r,
      );
      setOpen(false);
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(queryKey, ctx.prev);
      setShake((n) => n + 1);
    },
    onSettled: () => qc.invalidateQueries({ queryKey }),
  });
  if (f.status !== "open" && !dismiss.isError) return null;
  return (
    <div key={shake} className={`grid gap-1 ${shake ? "shake" : ""}`} data-shake={shake || undefined}>
      {open ? (
        <form
          className="flex items-center gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            dismiss.mutate({ path: { id: f.id }, body: { reason } });
          }}
        >
          <Input aria-label={t("finding.dismiss.reason")} value={reason} onChange={(e) => setReason(e.target.value)} />
          <Button size="sm" type="submit" disabled={reason.trim() === ""}>
            {t("finding.dismiss.save")}
          </Button>
        </form>
      ) : f.status === "open" ? (
        <Button size="sm" variant="ghost" onClick={() => setOpen(true)}>
          {t("finding.dismiss")}
        </Button>
      ) : null}
      {dismiss.isError ? (
        <span role="alert" className="text-[12px] text-danger">
          {t("finding.dismiss.failed")}
        </span>
      ) : null}
    </div>
  );
}

export function FindingRow({ f, action }: { f: FindingBody; action?: React.ReactNode }) {
  return (
    <TableRow>
      <TableCell className="t-value">{f.label}</TableCell>
      <TableCell>
        <span className="flex items-center gap-2">
          {f.severity}
          {f.blocking ? <Badge tone="danger">{t("finding.blocking")}</Badge> : null}
        </span>
      </TableCell>
      <TableCell className="min-w-[16rem]">{f.title}</TableCell>
      <TableCell className="t-value">
        {f.path}:{f.start_line}
      </TableCell>
      <TableCell>{t(`finding.status.${f.status}` as CopyKey)}</TableCell>
      {action !== undefined ? <TableCell>{action}</TableCell> : null}
    </TableRow>
  );
}

function Review() {
  const { owner, name, number } = Route.useParams();
  const { me } = Route.useRouteContext();
  const opts = getReviewOptions({ path: { owner, name, number: Number(number) } });
  const q = useQuery(opts);
  const canEdit = atLeast(me.role, "maintainer");
  return (
    <>
      <p className="t-crumb mb-2">
        <Link to="/" className="link">
          {t("reviews.back")}
        </Link>
      </p>
      <PageHead
        title={`${owner}/${name}#${number}`}
        actions={
          q.data?.temporal_url ? (
            <a className="link" href={q.data.temporal_url}>
              {t("temporal.link")}
            </a>
          ) : null
        }
      />
      {q.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : q.error ? (
        <ErrorAlert error={q.error} onRetry={() => q.refetch()} />
      ) : (
        <div className="grid gap-5">
          <Card className="overflow-x-auto">
            <CardHeader>
              <CardTitle>{t("review.rounds")}</CardTitle>
            </CardHeader>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("review.head")}</TableHead>
                  <TableHead>{t("review.trigger")}</TableHead>
                  <TableHead>{t("reviews.round")}</TableHead>
                  <TableHead>{t("reviews.verdict")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {q.data.rounds.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell>
                      <Link to="/rounds/$id" params={{ id: r.id }} className="link t-value">
                        {sha7(r.head_sha)}
                      </Link>
                    </TableCell>
                    <TableCell>{r.trigger}</TableCell>
                    <TableCell>
                      <RoundStatus status={r.status} />
                    </TableCell>
                    <TableCell>
                      <Verdict verdict={r.verdict} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </Card>
          <Card className="overflow-x-auto">
            <CardHeader>
              <CardTitle>{t("review.ledger")}</CardTitle>
            </CardHeader>
            {q.data.findings.length === 0 ? (
              <p className="t-hint p-4">{t("review.ledger.empty")}</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("finding.id")}</TableHead>
                    <TableHead>{t("finding.severity")}</TableHead>
                    <TableHead>{t("finding.title")}</TableHead>
                    <TableHead>{t("finding.location")}</TableHead>
                    <TableHead>{t("finding.status")}</TableHead>
                    {canEdit ? <TableHead /> : null}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {q.data.findings.map((f) => (
                    <FindingRow key={f.id} f={f} action={canEdit ? <DismissControl f={f} queryKey={opts.queryKey} /> : undefined} />
                  ))}
                </TableBody>
              </Table>
            )}
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t("review.signals")}</CardTitle>
            </CardHeader>
            {q.data.signals.length === 0 ? (
              <p className="t-hint p-4">{t("review.signals.empty")}</p>
            ) : (
              <ul className="divide-y divide-hairline">
                {q.data.signals.map((s) => (
                  <li key={`${s.type}-${s.at}`} className="flex flex-wrap gap-3 px-4 py-2 text-[13px]">
                    <span className="t-value text-ink">{s.type}</span>
                    {s.finding ? <span className="t-value">{s.finding}</span> : null}
                    <span className="t-hint">{s.actor}</span>
                    <span className="t-hint">{new Date(s.at).toLocaleString()}</span>
                  </li>
                ))}
              </ul>
            )}
          </Card>
        </div>
      )}
    </>
  );
}
