import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type React from "react";
import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import {
  dismissFindingMutation,
  fixFindingsMutation,
  getReviewOptions,
  retryFailedCommandMutation,
} from "@/client/@tanstack/react-query.gen";
import type { FailedCommandBody, FindingBody, FixBody, ReviewBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SafeMarkdown } from "@/components/markdown";
import { Alert } from "@/components/ui/alert";
import { Input } from "@/components/ui/input";
import { atLeast } from "@/lib/session";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t, type CopyKey } from "@/lib/copy";
import { ModeBadge } from "@/lib/mode";
import { liveStatus, RoundStatus, sha7, Verdict } from "@/lib/review";

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

/**
 * Suggestion shows the replacement code of a finding as added lines. The API has no removed
 * lines, so the block names the replaced range instead.
 */
function Suggestion({ f }: { f: FindingBody }) {
  if (!f.suggestion) return null;
  const range =
    f.end_line > f.start_line
      ? t("finding.suggestion.replaces", { start: f.start_line, end: f.end_line })
      : t("finding.suggestion.replaces_one", { start: f.start_line });
  const lines = f.suggestion.replace(/\n$/, "").split("\n");
  return (
    <div className="grid gap-1.5">
      <p className="flex flex-wrap items-baseline gap-x-3">
        <span className="t-label">{t("finding.suggestion")}</span>
        <span className="t-value t-hint">
          {f.path} · {range}
        </span>
      </p>
      <pre
        tabIndex={0}
        aria-label={`${t("finding.suggestion")} ${f.label}`}
        className="control control-mono max-h-80 overflow-auto px-0 py-1.5 whitespace-pre"
      >
        {lines.map((l, i) => (
          <span key={i} className="block bg-success-soft px-2.5 text-success">
            <span aria-hidden="true" className="mr-2 select-none">
              +
            </span>
            {l}
          </span>
        ))}
      </pre>
    </div>
  );
}

type Select = { checked: boolean; onChange: (on: boolean) => void } | null;

/**
 * FindingRow is one ledger row. With select, the row has a checkbox cell (empty when the finding
 * is not open). A finding with a suggestion gets a second row with the suggested code.
 */
export function FindingRow({ f, action, select, fixUrl }: { f: FindingBody; action?: React.ReactNode; select?: Select; fixUrl?: string }) {
  const columns = 5 + (select !== undefined ? 1 : 0) + (action !== undefined ? 1 : 0);
  return (
    <>
      <TableRow>
        {select !== undefined ? (
          <TableCell className="w-8">
            {select ? (
              <input
                type="checkbox"
                aria-label={t("finding.select", { label: f.label })}
                checked={select.checked}
                onChange={(e) => select.onChange(e.target.checked)}
              />
            ) : null}
          </TableCell>
        ) : null}
        <TableCell className="t-value">{f.label}</TableCell>
        <TableCell>
          <span className="flex flex-wrap items-center gap-2">
            {f.severity}
            {f.blocking ? <Badge tone="danger">{t("finding.blocking")}</Badge> : null}
            {f.pre_existing ? <Badge>{t("finding.pre_existing")}</Badge> : null}
          </span>
        </TableCell>
        <TableCell className="min-w-[16rem]">{f.title}</TableCell>
        <TableCell className="t-value">
          {f.path}:{f.start_line}
        </TableCell>
        <TableCell>
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            {t(`finding.status.${f.status}` as CopyKey)}
            {fixUrl ? (
              <a href={fixUrl} className="link" rel="noopener noreferrer" target="_blank">
                {t("finding.fix_link")}
              </a>
            ) : null}
          </span>
        </TableCell>
        {action !== undefined ? <TableCell>{action}</TableCell> : null}
      </TableRow>
      {f.suggestion ? (
        <TableRow>
          <TableCell colSpan={columns}>
            <Suggestion f={f} />
          </TableCell>
        </TableRow>
      ) : null}
    </>
  );
}

/** fixUrls maps a finding label to the pull request of a done fix that contains it. */
function fixUrls(fixes: FixBody[]) {
  const out = new Map<string, string>();
  for (const x of fixes) {
    if (x.status !== "done" || !x.pull_url) continue;
    for (const label of x.findings) if (!out.has(label)) out.set(label, x.pull_url);
  }
  return out;
}

type ReviewPath = { owner: string; name: string; number: number };

function Ledger({ path, data, canEdit, queryKey }: { path: ReviewPath; data: ReviewBody; canEdit: boolean; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const [chosen, setChosen] = useState<string[]>([]);
  const fix = useMutation({
    ...fixFindingsMutation(),
    onSuccess: () => setChosen([]),
    // A refusal often means the ledger changed, so the page reads it again either way.
    onSettled: () => qc.invalidateQueries({ queryKey }),
  });
  const urls = fixUrls(data.fixes);
  const open = new Set(data.findings.filter((f) => f.status === "open").map((f) => f.label));
  const selected = chosen.filter((l) => open.has(l));
  return (
    <Card className="overflow-x-auto">
      <CardHeader>
        <CardTitle>{t("review.ledger")}</CardTitle>
        {canEdit && open.size > 0 ? (
          <div className="flex flex-wrap items-center gap-3">
            {fix.isSuccess && !fix.isPending ? (
              <span role="status" className="t-hint fade-in">
                {t("review.fix.queued")}
              </span>
            ) : null}
            <Button
              size="sm"
              mutation={fix}
              disabled={selected.length === 0}
              onClick={() => fix.mutate({ path, body: { findings: selected } })}
            >
              {t("review.fix.selected")}
            </Button>
          </div>
        ) : null}
      </CardHeader>
      {fix.error ? <ErrorAlert error={fix.error} className="m-4" /> : null}
      {data.findings.length === 0 ? (
        <p className="t-hint p-4">{t("review.ledger.empty")}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              {canEdit ? <TableHead /> : null}
              <TableHead>{t("finding.id")}</TableHead>
              <TableHead>{t("finding.severity")}</TableHead>
              <TableHead>{t("finding.title")}</TableHead>
              <TableHead>{t("finding.location")}</TableHead>
              <TableHead>{t("finding.status")}</TableHead>
              {canEdit ? <TableHead /> : null}
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.findings.map((f) => (
              <FindingRow
                key={f.id}
                f={f}
                fixUrl={urls.get(f.label)}
                select={
                  canEdit
                    ? f.status === "open"
                      ? {
                          checked: selected.includes(f.label),
                          onChange: (on) => setChosen(on ? [...selected, f.label] : selected.filter((l) => l !== f.label)),
                        }
                      : null
                    : undefined
                }
                action={canEdit ? <DismissControl f={f} queryKey={queryKey} /> : undefined}
              />
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}

function FixesCard({ fixes }: { fixes: FixBody[] }) {
  return (
    <Card className="overflow-x-auto">
      <CardHeader>
        <CardTitle>{t("review.fixes")}</CardTitle>
      </CardHeader>
      {fixes.length === 0 ? (
        <p className="t-hint p-4">{t("review.fixes.empty")}</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("review.fixes.findings")}</TableHead>
              <TableHead>{t("finding.status")}</TableHead>
              <TableHead>{t("review.fixes.reason")}</TableHead>
              <TableHead>{t("reviews.pull")}</TableHead>
              <TableHead>{t("review.fixes.actor")}</TableHead>
              <TableHead>{t("review.fixes.at")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {fixes.map((x) => (
              <TableRow key={x.id}>
                <TableCell className="t-value">{x.findings.join(", ")}</TableCell>
                <TableCell>
                  <RoundStatus status={x.status} />
                </TableCell>
                <TableCell className="min-w-[12rem]">{x.reason || <span className="t-hint">—</span>}</TableCell>
                <TableCell>
                  {x.pull_url ? (
                    <a href={x.pull_url} className="link t-value" rel="noopener noreferrer" target="_blank">
                      #{x.pull_url.slice(x.pull_url.lastIndexOf("/") + 1)}
                    </a>
                  ) : (
                    <span className="t-hint">—</span>
                  )}
                </TableCell>
                <TableCell className="t-value">{x.actor}</TableCell>
                <TableCell className="t-hint whitespace-nowrap">{new Date(x.at).toLocaleString()}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}

function FailedCommand({ c, canEdit, queryKey }: { c: FailedCommandBody; canEdit: boolean; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const retry = useMutation({ ...retryFailedCommandMutation(), onSuccess: () => qc.invalidateQueries({ queryKey }) });
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[13px]">
        <span className="t-value text-ink">{c.command}</span>
        <span className="t-value">{c.author}</span>
        <span className="t-hint">{new Date(c.at).toLocaleString()}</span>
        {c.retried_at ? (
          <span className="t-hint">{t("review.failed.retried", { at: new Date(c.retried_at).toLocaleString() })}</span>
        ) : null}
        {canEdit ? (
          <Button size="sm" className="ml-auto" mutation={retry} onClick={() => retry.mutate({ path: { id: c.id } })}>
            {t("review.failed.retry")}
          </Button>
        ) : null}
      </div>
      <p>{c.reason}</p>
      <ErrorAlert error={retry.error} />
    </li>
  );
}

function FailedCommandsCard({ data, canEdit, queryKey }: { data: FailedCommandBody[]; canEdit: boolean; queryKey: readonly unknown[] }) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("review.failed")}</CardTitle>
      </CardHeader>
      {data.length === 0 ? (
        <p className="t-hint p-4">{t("review.failed.empty")}</p>
      ) : (
        <ul aria-label={t("review.failed")} className="divide-y divide-hairline">
          {data.map((c) => (
            <FailedCommand key={c.id} c={c} canEdit={canEdit} queryKey={queryKey} />
          ))}
        </ul>
      )}
    </Card>
  );
}

/** ReviewMeta is the line under the title: the pull request, its branch, mode and links. */
function ReviewMeta({ data }: { data: ReviewBody }) {
  const posts = data.mode === "comment" || data.mode === "gate";
  return (
    <div className="-mt-3 mb-6 flex flex-wrap items-center gap-x-4 gap-y-2 text-[13px]">
      <span className="t-value">
        {data.repository}#{data.number}
      </span>
      <span className="t-hint">{data.author}</span>
      <span className="flex items-center gap-1.5">
        <span className="t-label">{t("review.branch")}</span>
        <span className="t-value text-ink">{data.head_ref}</span>
      </span>
      <ModeBadge mode={data.mode} />
      <span>{t(`review.state.${data.state}` as CopyKey)}</span>
      <a href={data.html_url} className="link" rel="noopener noreferrer" target="_blank">
        {t("review.github")}
      </a>
      {posts && data.summary_url ? (
        <a href={data.summary_url} className="link" rel="noopener noreferrer" target="_blank">
          {t("review.summary_link")}
        </a>
      ) : null}
      {posts && data.check_url ? (
        <a href={data.check_url} className="link" rel="noopener noreferrer" target="_blank">
          {t("review.check_link")}
        </a>
      ) : null}
    </div>
  );
}

function Review() {
  const { owner, name, number } = Route.useParams();
  const { me } = Route.useRouteContext();
  const path = { owner, name, number: Number(number) };
  const opts = getReviewOptions({ path });
  const q = useQuery({
    ...opts,
    refetchInterval: (query) => {
      const d = query.state.data;
      if (!d) return false;
      const waiting = d.last_round ? liveStatus(d.last_round.status) : d.state === "open" && d.mode !== "off";
      return waiting || d.fixes.some((x) => liveStatus(x.status)) ? 2000 : false;
    },
  });
  const canEdit = atLeast(me.role, "maintainer");
  return (
    <>
      <p className="t-crumb mb-2">
        <Link to="/reviews" className="link">
          {t("reviews.back")}
        </Link>
      </p>
      <PageHead
        title={q.data?.title || `${owner}/${name}#${number}`}
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
        <>
          <ReviewMeta data={q.data} />
          <div className="grid gap-5">
            {q.data.mode === "shadow" ? (
              <>
                <Alert>{t("review.shadow")}</Alert>
                {q.data.preview?.summary ? (
                  <Card>
                    <CardHeader>
                      <CardTitle>{t("review.preview.summary")}</CardTitle>
                    </CardHeader>
                    <div className="p-4">
                      <SafeMarkdown text={q.data.preview.summary} />
                    </div>
                  </Card>
                ) : null}
              </>
            ) : null}
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
            <Ledger path={path} data={q.data} canEdit={canEdit} queryKey={opts.queryKey} />
            <FixesCard fixes={q.data.fixes} />
            <FailedCommandsCard data={q.data.failed_commands} canEdit={canEdit} queryKey={opts.queryKey} />
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
        </>
      )}
    </>
  );
}
