import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { cancelRoundMutation, getRoundOptions, getRoundQueryKey } from "@/client/@tanstack/react-query.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SafeMarkdown } from "@/components/markdown";
import { Alert } from "@/components/ui/alert";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t } from "@/lib/copy";
import { RoundStatus, sha7, Verdict } from "@/lib/review";
import { FindingRow } from "../reviews/$owner.$name.$number";

export const Route = createFileRoute("/_app/rounds/$id")({ component: Round });

function Round() {
  const { id } = Route.useParams();
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
