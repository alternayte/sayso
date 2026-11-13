import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { createFileRoute, Link } from "@tanstack/react-router";
import { listReviewsOptions } from "@/client/@tanstack/react-query.gen";
import { ErrorAlert, PageHead, Segmented } from "@/components/app-ui";
import { ChecklistGate } from "@/components/checklist";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t, type CopyKey } from "@/lib/copy";
import { RoundStatus, sha7, Verdict } from "@/lib/review";

export const Route = createFileRoute("/_app/")({ component: Home });

type State = "" | "open" | "paused" | "closed" | "merged";

function Home() {
  const { me } = Route.useRouteContext();
  const admin = me.role === "admin";
  return (
    <ChecklistGate admin={admin}>
      <Reviews />
    </ChecklistGate>
  );
}

function Reviews() {
  const [state, setState] = useState<State>("");
  const [repo, setRepo] = useState("");
  const list = useQuery({
    ...listReviewsOptions({ query: { state: state || undefined, repository: repo || undefined } }),
    placeholderData: keepPreviousData,
  });
  return (
    <>
      <PageHead title={t("home.title")} hint={t("home.hint")} />
      <div className="mb-4 flex flex-wrap items-end gap-4">
        <Segmented
          label={t("reviews.filter.state")}
          options={(["", "open", "paused", "closed", "merged"] as const).map((v) => ({
            value: v,
            label: v ? t(`review.state.${v}`) : t("reviews.filter.all"),
          }))}
          value={state}
          onChange={setState}
        />
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="reviews-repo">
            {t("reviews.filter.repository")}
          </label>
          <Input mono id="reviews-repo" value={repo} placeholder="acme/payments" onChange={(e) => setRepo(e.target.value.trim())} />
        </div>
      </div>
      {list.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : list.error ? (
        <ErrorAlert error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.length === 0 ? (
        <Card className="p-6">
          <p className="t-hint">{t("home.empty")}</p>
        </Card>
      ) : (
        <Card className="relative overflow-x-auto">
          {list.isFetching ? <div className="progress-line" role="progressbar" aria-label={t("loading.list")} /> : null}
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
