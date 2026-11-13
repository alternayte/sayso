import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { getReviewOptions } from "@/client/@tanstack/react-query.gen";
import type { FindingBody } from "@/client/types.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t, type CopyKey } from "@/lib/copy";
import { RoundStatus, sha7, Verdict } from "@/lib/review";

export const Route = createFileRoute("/_app/reviews/$owner/$name/$number")({ component: Review });

export function FindingRow({ f }: { f: FindingBody }) {
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
    </TableRow>
  );
}

function Review() {
  const { owner, name, number } = Route.useParams();
  const q = useQuery(getReviewOptions({ path: { owner, name, number: Number(number) } }));
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
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {q.data.findings.map((f) => (
                    <FindingRow key={f.id} f={f} />
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
