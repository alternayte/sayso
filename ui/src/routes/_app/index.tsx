import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { listReviewsOptions } from "@/client/@tanstack/react-query.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t, type CopyKey } from "@/lib/copy";
import { RoundStatus, sha7, Verdict } from "@/lib/review";

export const Route = createFileRoute("/_app/")({ component: Home });

function Home() {
  const list = useQuery(listReviewsOptions());
  return (
    <>
      <PageHead title={t("home.title")} hint={t("home.hint")} />
      {list.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : list.error ? (
        <ErrorAlert error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.length === 0 ? (
        <Card className="p-6">
          <p className="t-hint">{t("home.empty")}</p>
        </Card>
      ) : (
        <Card className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("reviews.pull")}</TableHead>
                <TableHead>{t("reviews.state")}</TableHead>
                <TableHead>{t("reviews.round")}</TableHead>
                <TableHead>{t("reviews.verdict")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.data.map((r) => {
                const [owner = "", name = ""] = r.repository.split("/");
                return (
                  <TableRow key={r.id}>
                    <TableCell>
                      <Link to="/reviews/$owner/$name/$number" params={{ owner, name, number: String(r.number) }} className="link t-value">
                        {r.repository}#{r.number}
                      </Link>
                      <span className="t-hint ml-2">{r.author}</span>
                    </TableCell>
                    <TableCell>{t(`review.state.${r.state}` as CopyKey)}</TableCell>
                    <TableCell>
                      {r.last_round ? (
                        <span className="flex items-center gap-2">
                          <RoundStatus status={r.last_round.status} />
                          <span className="t-value t-hint">{sha7(r.last_round.head_sha)}</span>
                        </span>
                      ) : (
                        <span className="t-hint">—</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <Verdict verdict={r.last_round?.verdict} />
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </Card>
      )}
    </>
  );
}
