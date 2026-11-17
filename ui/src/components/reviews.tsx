import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { listRepositoriesOptions, listReviewsOptions } from "@/client/@tanstack/react-query.gen";
import type { ListReviewsData, ReviewItem } from "@/client/types.gen";
import { ErrorAlert, Segmented } from "@/components/app-ui";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { t, type CopyKey } from "@/lib/copy";
import { ModeBadge } from "@/lib/mode";
import { liveStatus, RoundStatus, sha7, Verdict } from "@/lib/review";

type Query = NonNullable<ListReviewsData["query"]>;
type PullState = NonNullable<Query["state"]>;
type RoundFilter = NonNullable<Query["round_status"]>;

export type ReviewsSearch = { state?: Exclude<PullState, "">; round_status?: Exclude<RoundFilter, "">; repository?: string };

const pullStates = ["open", "paused", "closed", "merged"] as const;
const roundStates = ["queued", "running", "done", "failed", "cancelled"] as const;

function pick<T extends string>(v: unknown, allowed: readonly T[]): T | undefined {
  return allowed.find((a) => a === v);
}

/** validateReviewsSearch keeps only known filter values from the URL. */
export function validateReviewsSearch(s: Record<string, unknown>): ReviewsSearch {
  const out: ReviewsSearch = {};
  const state = pick(s.state, pullStates);
  const round = pick(s.round_status, roundStates);
  if (state) out.state = state;
  if (round) out.round_status = round;
  if (typeof s.repository === "string" && s.repository.includes("/")) out.repository = s.repository;
  return out;
}

/**
 * live reports whether a row can change soon: its latest round is queued or running, or the pull
 * request is open on a repository that is on and has no round yet (the debounce still waits).
 */
function live(r: ReviewItem) {
  if (r.last_round) return liveStatus(r.last_round.status);
  return r.state === "open" && r.mode !== "off";
}

/** useReviews lists reviews and polls every 2 s while a shown row is live. */
export function useReviews(query: Query) {
  return useQuery({
    ...listReviewsOptions({ query }),
    placeholderData: keepPreviousData,
    refetchInterval: (q) => (q.state.data?.some(live) ? 2000 : false),
  });
}

/** ReviewsList is the filtered reviews list. The caller keeps the filters, for example in the URL. */
export function ReviewsList({ search, onSearch }: { search: ReviewsSearch; onSearch: (s: ReviewsSearch) => void }) {
  const list = useReviews(search);
  const repos = useQuery(listRepositoriesOptions());
  const filtered = Boolean(search.state || search.round_status || search.repository);
  return (
    <>
      <div className="mb-4 flex flex-wrap items-end gap-4">
        <div className="grid max-w-full gap-1.5 overflow-x-auto">
          <span className="t-label" aria-hidden="true">
            {t("reviews.filter.state")}
          </span>
          <Segmented
            label={t("reviews.filter.state")}
            options={(["", ...pullStates] as const).map((v) => ({
              value: v,
              label: v ? t(`review.state.${v}`) : t("reviews.filter.all"),
            }))}
            value={search.state ?? ""}
            onChange={(v) => onSearch({ ...search, state: v || undefined })}
          />
        </div>
        <div className="grid max-w-full gap-1.5 overflow-x-auto">
          <span className="t-label" aria-hidden="true">
            {t("reviews.filter.round")}
          </span>
          <Segmented
            label={t("reviews.filter.round")}
            options={(["", ...roundStates] as const).map((v) => ({
              value: v,
              label: v ? t(`round.status.${v}`) : t("reviews.filter.all"),
            }))}
            value={search.round_status ?? ""}
            onChange={(v) => onSearch({ ...search, round_status: v || undefined })}
          />
        </div>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="reviews-repo">
            {t("reviews.filter.repository")}
          </label>
          <select
            id="reviews-repo"
            className="control control-mono w-auto"
            value={search.repository ?? ""}
            disabled={repos.isPending}
            onChange={(e) => onSearch({ ...search, repository: e.target.value || undefined })}
          >
            <option value="">{t("reviews.filter.repository.all")}</option>
            {search.repository && !repos.data?.some((r) => r.full_name === search.repository) ? (
              <option value={search.repository}>{search.repository}</option>
            ) : null}
            {(repos.data ?? []).map((r) => (
              <option key={r.id} value={r.full_name}>
                {r.full_name}
              </option>
            ))}
          </select>
        </div>
      </div>
      <ErrorAlert error={repos.error} className="mb-4" />
      {list.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : list.error ? (
        <ErrorAlert error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.length === 0 ? (
        <Card className="p-6">
          <p className="t-hint">{filtered ? t("reviews.empty.filtered") : t("home.empty")}</p>
        </Card>
      ) : (
        <ReviewsTable rows={list.data} fetching={list.isFetching} />
      )}
    </>
  );
}

/** ReviewsTable shows one row per review: the pull request, its state, mode and latest round. */
export function ReviewsTable({ rows, fetching }: { rows: ReviewItem[]; fetching?: boolean }) {
  return (
    <Card className="relative overflow-x-auto">
      {fetching ? <div className="progress-line" role="progressbar" aria-label={t("loading.list")} /> : null}
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("reviews.pull")}</TableHead>
            <TableHead>{t("reviews.state")}</TableHead>
            <TableHead>{t("reviews.mode")}</TableHead>
            <TableHead>{t("reviews.round")}</TableHead>
            <TableHead>{t("reviews.verdict")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((r) => {
            const [owner = "", name = ""] = r.repository.split("/");
            return (
              <TableRow key={r.id}>
                <TableCell className="min-w-[18rem]">
                  <Link to="/reviews/$owner/$name/$number" params={{ owner, name, number: String(r.number) }} className="link">
                    {r.title || `${r.repository}#${r.number}`}
                  </Link>
                  <span className="mt-0.5 flex flex-wrap gap-x-2">
                    <span className="t-value t-hint">
                      {r.repository}#{r.number}
                    </span>
                    <span className="t-hint">{r.author}</span>
                  </span>
                </TableCell>
                <TableCell>{t(`review.state.${r.state}` as CopyKey)}</TableCell>
                <TableCell>
                  <ModeBadge mode={r.mode} />
                </TableCell>
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
  );
}
