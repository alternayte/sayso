import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { getRepositoryOptions, listRepositoriesQueryKey, setRepositoryModeMutation } from "@/client/@tanstack/react-query.gen";
import type { RepositoryBody } from "@/client/types.gen";
import { ErrorAlert, PageHead, Segmented } from "@/components/app-ui";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";
import { modes, modeLabel, type Mode } from "@/lib/mode";
import { atLeast } from "@/lib/session";

export const Route = createFileRoute("/_app/repositories/$owner/$name")({ component: Repository });

function Repository() {
  const { owner, name } = Route.useParams();
  const { me } = Route.useRouteContext();
  const opts = getRepositoryOptions({ path: { owner, name } });
  const repo = useQuery(opts);
  return (
    <>
      <p className="t-crumb mb-2">
        <Link to="/repositories" className="link">
          {t("repos.back")}
        </Link>
      </p>
      <PageHead title={`${owner}/${name}`} />
      {repo.isPending ? (
        <Skeleton className="h-32 w-full" />
      ) : repo.error ? (
        <ErrorAlert error={repo.error} onRetry={() => repo.refetch()} />
      ) : (
        <ModeCard repo={repo.data} canEdit={atLeast(me.role, "maintainer")} queryKey={opts.queryKey} />
      )}
    </>
  );
}

function ModeCard({ repo, canEdit, queryKey }: { repo: RepositoryBody; canEdit: boolean; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const [shake, setShake] = useState(0);
  const [owner = "", name = ""] = repo.full_name.split("/");
  // Optimistic: the control moves at once and rolls back with a shake when the save fails.
  const save = useMutation({
    ...setRepositoryModeMutation(),
    onMutate: async (vars) => {
      await qc.cancelQueries({ queryKey });
      const before = qc.getQueryData<RepositoryBody>(queryKey);
      if (before && vars.body) qc.setQueryData<RepositoryBody>(queryKey, { ...before, mode: vars.body.mode });
      return { before };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.before) qc.setQueryData(queryKey, ctx.before);
      setShake((n) => n + 1);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey });
      void qc.invalidateQueries({ queryKey: listRepositoriesQueryKey() });
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("repos.mode.label")}</CardTitle>
      </CardHeader>
      <div className="grid gap-3 p-4">
        <p className="t-hint max-w-[640px]">{t("repos.mode.hint")}</p>
        <div className="flex flex-wrap items-center gap-3">
          <Segmented
            key={shake}
            className={shake > 0 ? "shake" : ""}
            label={t("repos.mode.label")}
            options={modes.map((m) => ({ value: m, label: modeLabel(m) }))}
            value={repo.mode as Mode}
            disabled={!canEdit}
            onChange={(mode) => save.mutate({ path: { owner, name }, body: { mode } })}
          />
          {save.isSuccess && !save.isPending ? (
            <span role="status" className="t-hint fade-in">
              {t("repos.mode.saved")}
            </span>
          ) : null}
        </div>
        {!canEdit ? <p className="t-hint">{t("repos.mode.readonly")}</p> : null}
        {save.isError ? (
          <p role="alert" className="text-[13px] text-danger">
            {t("repos.mode.failed")}
          </p>
        ) : null}
      </div>
    </Card>
  );
}
