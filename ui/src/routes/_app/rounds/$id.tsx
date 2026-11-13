import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { cancelRoundMutation, getRoundOptions, getRoundQueryKey, saveSteeringReviewMutation } from "@/client/@tanstack/react-query.gen";
import type { SignalBody, SteeringNote } from "@/client/types.gen";
import { Input } from "@/components/ui/input";
import { atLeast } from "@/lib/session";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SafeMarkdown } from "@/components/markdown";
import { Alert } from "@/components/ui/alert";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t } from "@/lib/copy";
import { RoundStatus, sha7, Verdict } from "@/lib/review";
import { FindingRow } from "../reviews/$owner.$name.$number";

export const Route = createFileRoute("/_app/rounds/$id")({
  loader: ({ context, params }) => void context.queryClient.prefetchQuery(getRoundOptions({ path: { id: params.id } })),
  component: Round,
});

function SteeringCard({ id, signals, saved, canEdit }: { id: string; signals: SignalBody[]; saved?: SteeringNote; canEdit: boolean }) {
  const qc = useQueryClient();
  const [tags, setTags] = useState((saved?.tags ?? []).join(", "));
  const [note, setNote] = useState(saved?.note ?? "");
  const save = useMutation({
    ...saveSteeringReviewMutation(),
    onSuccess: () => qc.invalidateQueries({ queryKey: getRoundQueryKey({ path: { id } }) }),
  });
  if (signals.length === 0 && !saved) return null;
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("round.steering")}</CardTitle>
      </CardHeader>
      <div className="grid gap-4 p-4">
        <ul aria-label={t("review.signals")} className="grid gap-1 text-[13px]">
          {signals.map((s) => (
            <li key={`${s.type}-${s.at}`} className="flex flex-wrap gap-3">
              <span className="t-value text-ink">{s.type}</span>
              {s.finding ? <span className="t-value">{s.finding}</span> : null}
              <span className="t-hint">{s.actor}</span>
            </li>
          ))}
        </ul>
        {saved ? <p className="t-hint">{t("round.steering.saved", { author: saved.author })}</p> : null}
        {canEdit ? (
          <form
            className="grid max-w-[640px] gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate({
                path: { id },
                body: {
                  tags: tags
                    .split(",")
                    .map((x) => x.trim())
                    .filter(Boolean),
                  note,
                },
              });
            }}
          >
            <div className="grid gap-1.5">
              <label className="t-label" htmlFor="steer-tags">
                {t("round.steering.tags")}
              </label>
              <Input mono id="steer-tags" value={tags} onChange={(e) => setTags(e.target.value)} />
            </div>
            <div className="grid gap-1.5">
              <label className="t-label" htmlFor="steer-note">
                {t("round.steering.note")}
              </label>
              <textarea id="steer-note" className="control min-h-20" value={note} onChange={(e) => setNote(e.target.value)} />
            </div>
            <ErrorAlert error={save.error} />
            <div>
              <Button type="submit" variant="primary" mutation={save}>
                {t("round.steering.save")}
              </Button>
            </div>
          </form>
        ) : null}
      </div>
    </Card>
  );
}

function Round() {
  const { id } = Route.useParams();
  const { me } = Route.useRouteContext();
  const qc = useQueryClient();
  const live = (s?: string) => s === "queued" || s === "running" || s === "publishing";
  const q = useQuery({ ...getRoundOptions({ path: { id } }), refetchInterval: (query) => (live(query.state.data?.status) ? 2000 : false) });
  const cancel = useMutation({
    ...cancelRoundMutation(),
    onSettled: () => qc.invalidateQueries({ queryKey: getRoundQueryKey({ path: { id } }) }),
  });
  const r = q.data;
  const [owner = "", name = ""] = (r?.repository ?? "/").split("/");
  return (
    <>
      {r ? (
        <p className="t-crumb mb-2">
          <Link to="/reviews/$owner/$name/$number" params={{ owner, name, number: String(r.number) }} className="link">
            {r.repository}#{r.number}
          </Link>
        </p>
      ) : null}
      <PageHead
        title={r ? t("round.title", { sha: sha7(r.head_sha) }) : t("round.loading")}
        actions={
          r ? (
            <div className="flex items-center gap-3">
              {r.agent_run_id ? (
                <Link to="/runs/$id" params={{ id: r.agent_run_id }} className="link">
                  {t("round.run")}
                </Link>
              ) : null}
              <a className="link" href={`/api/v1/rounds/${id}/case`} download>
                {t("round.export")}
              </a>
              {r.temporal_url ? (
                <a className="link" href={r.temporal_url}>
                  {t("temporal.link")}
                </a>
              ) : null}
              {r.can_cancel ? (
                <Button size="sm" variant="ghost" mutation={cancel} onClick={() => cancel.mutate({ path: { id } })}>
                  {t("round.cancel")}
                </Button>
              ) : null}
            </div>
          ) : null
        }
      />
      {q.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : q.error ? (
        <ErrorAlert error={q.error} onRetry={() => q.refetch()} />
      ) : (
        <div className="grid gap-5">
          <ErrorAlert error={cancel.error} />
          <Card className="grid gap-3 p-4">
            <div className="flex flex-wrap items-center gap-3">
              <RoundStatus status={q.data.status} />
              <Verdict verdict={q.data.verdict} />
              {q.data.risk ? <span className="t-hint">{t("round.risk", { risk: q.data.risk })}</span> : null}
              <span className="t-hint">{t("round.trigger", { trigger: q.data.trigger })}</span>
            </div>
            {q.data.reason ? <p className="text-ink">{q.data.reason}</p> : null}
            {q.data.mode === "shadow" ? <Alert>{t("round.shadow")}</Alert> : null}
          </Card>
          {q.data.assessment ? (
            <Card className="grid gap-3 p-4">
              <h2 className="t-section">{t("round.assessment")}</h2>
              <p className="text-ink">{q.data.assessment.tldr}</p>
              {q.data.assessment.impact.length > 0 ? (
                <ul className="grid gap-1 text-[13px]">
                  {q.data.assessment.impact.map((i) => (
                    <li key={i.area}>
                      <span className="t-value text-ink">{i.area}</span> <span className="t-hint">{i.why}</span>
                    </li>
                  ))}
                </ul>
              ) : null}
            </Card>
          ) : null}
          {q.data.findings.length > 0 ? (
            <Card className="overflow-x-auto">
              <CardHeader>
                <CardTitle>{t("round.findings")}</CardTitle>
              </CardHeader>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>{t("finding.id")}</TableHead>
                    <TableHead>{t("finding.severity")}</TableHead>
                    <TableHead>{t("finding.title")}</TableHead>
                    <TableHead>{t("finding.location")}</TableHead>
                    <TableHead>{t("finding.status")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {q.data.findings.map((f) => (
                    <FindingRow key={f.id} f={f} />
                  ))}
                </TableBody>
              </Table>
            </Card>
          ) : null}
          <SteeringCard id={id} signals={q.data.signals} saved={q.data.steering_review} canEdit={atLeast(me.role, "maintainer")} />
          {q.data.preview ? (
            <Card>
              <CardHeader>
                <CardTitle>{t("round.output")}</CardTitle>
              </CardHeader>
              <div className="grid gap-4 p-4">
                <section aria-label={t("round.output.summary")} className="grid gap-2">
                  <h3 className="t-label">{t("round.output.summary")}</h3>
                  <SafeMarkdown text={q.data.preview.summary} />
                </section>
                {q.data.preview.comments.map((c) => (
                  <section key={c.finding_id} aria-label={`${c.path}:${c.line}`} className="grid gap-2 border-t border-hairline pt-4">
                    <h3 className="t-value text-ink">
                      {c.path}:{c.line}
                    </h3>
                    <SafeMarkdown text={c.body} />
                  </section>
                ))}
              </div>
            </Card>
          ) : null}
          {q.data.config ? (
            <Card>
              <CardHeader>
                <CardTitle>{t("round.config")}</CardTitle>
              </CardHeader>
              <pre
                tabIndex={0}
                aria-label={t("round.config")}
                className="control control-mono m-4 max-h-96 overflow-auto whitespace-pre-wrap"
              >
                {JSON.stringify(q.data.config, null, 2)}
              </pre>
            </Card>
          ) : null}
        </div>
      )}
    </>
  );
}
