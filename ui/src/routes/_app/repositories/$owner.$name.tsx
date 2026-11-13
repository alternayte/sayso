import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import {
  getRepositoryOptions,
  listRepositoriesQueryKey,
  setRepositoryFeedsMutation,
  setRepositoryMcpMutation,
  setRepositoryReviewMutation,
  setRepositoryModeMutation,
} from "@/client/@tanstack/react-query.gen";
import type { RepositoryBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead, Segmented } from "@/components/app-ui";
import { Alert } from "@/components/ui/alert";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
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
        <div className="grid gap-5">
          <ModeCard repo={repo.data} canEdit={atLeast(me.role, "maintainer")} queryKey={opts.queryKey} />
          <McpCard
            key={repo.data.mcp_servers.join(",")}
            repo={repo.data}
            canEdit={atLeast(me.role, "maintainer")}
            queryKey={opts.queryKey}
          />
          <ReviewCard
            key={`${repo.data.execution}-${repo.data.challenge_min_severity}`}
            repo={repo.data}
            canEdit={atLeast(me.role, "maintainer")}
            queryKey={opts.queryKey}
          />
          <FeedsCard key={repo.data.feeds.join(",")} repo={repo.data} canEdit={atLeast(me.role, "maintainer")} queryKey={opts.queryKey} />
        </div>
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

function McpCard({ repo, canEdit, queryKey }: { repo: RepositoryBody; canEdit: boolean; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const [owner = "", name = ""] = repo.full_name.split("/");
  const [chosen, setChosen] = useState<string[]>(repo.mcp_servers);
  const save = useMutation({ ...setRepositoryMcpMutation(), onSuccess: () => qc.invalidateQueries({ queryKey }) });
  const available = repo.mcp_available ?? [];
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("repos.mcp.label")}</CardTitle>
      </CardHeader>
      <div className="grid gap-3 p-4">
        <p className="t-hint max-w-[640px]">{t("repos.mcp.hint")}</p>
        {available.length === 0 ? <p className="t-hint">{t("repos.mcp.none")}</p> : null}
        <ul className="grid gap-2">
          {available.map((s) => (
            <li key={s}>
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  disabled={!canEdit}
                  checked={chosen.includes(s)}
                  onChange={(e) => setChosen(e.target.checked ? [...chosen, s] : chosen.filter((x) => x !== s))}
                />
                <span className="t-value text-ink">{s}</span>
              </label>
            </li>
          ))}
        </ul>
        {canEdit && available.length > 0 ? (
          <div className="flex items-center gap-3">
            <Button mutation={save} onClick={() => save.mutate({ path: { owner, name }, body: { servers: chosen } })}>
              {t("repos.mcp.save")}
            </Button>
          </div>
        ) : null}
        <ErrorAlert error={save.error} />
      </div>
    </Card>
  );
}

function FeedsCard({ repo, canEdit, queryKey }: { repo: RepositoryBody; canEdit: boolean; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const [owner = "", name = ""] = repo.full_name.split("/");
  const [chosen, setChosen] = useState<string[]>(repo.feeds);
  const save = useMutation({ ...setRepositoryFeedsMutation(), onSuccess: () => qc.invalidateQueries({ queryKey }) });
  const available = repo.feeds_available ?? [];
  const exposed = available.filter((f) => f.inject && repo.feeds.includes(f.name)).map((f) => f.name);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("repos.feeds.label")}</CardTitle>
      </CardHeader>
      <div className="grid gap-3 p-4">
        <p className="t-hint max-w-[640px]">{t("repos.feeds.hint")}</p>
        {exposed.length > 0 ? <Alert tone="warning">{t("repos.feeds.inject_warning", { feeds: exposed.join(", ") })}</Alert> : null}
        {available.length === 0 ? <p className="t-hint">{t("repos.feeds.none")}</p> : null}
        <ul className="grid gap-2">
          {available.map((f) => (
            <li key={f.name}>
              <label className="flex items-center gap-2">
                <input
                  type="checkbox"
                  disabled={!canEdit}
                  checked={chosen.includes(f.name)}
                  onChange={(e) => setChosen(e.target.checked ? [...chosen, f.name] : chosen.filter((x) => x !== f.name))}
                />
                <span className="t-value text-ink">{f.name}</span>
                {f.inject ? (
                  <Badge dot tone="warning">
                    {t("feeds.mode.inject")}
                  </Badge>
                ) : null}
              </label>
            </li>
          ))}
        </ul>
        {canEdit && available.length > 0 ? (
          <div className="flex items-center gap-3">
            <Button mutation={save} onClick={() => save.mutate({ path: { owner, name }, body: { feeds: chosen } })}>
              {t("repos.feeds.save")}
            </Button>
          </div>
        ) : null}
        <ErrorAlert error={save.error} />
      </div>
    </Card>
  );
}

type Severity = "blocker" | "major" | "minor" | "nit";

/** ReviewCard sets code execution and the challenge threshold (REQ-UI-011). */
function ReviewCard({ repo, canEdit, queryKey }: { repo: RepositoryBody; canEdit: boolean; queryKey: readonly unknown[] }) {
  const qc = useQueryClient();
  const [owner = "", name = ""] = repo.full_name.split("/");
  const [execution, setExecution] = useState(repo.execution);
  const [threshold, setThreshold] = useState<Severity>(repo.challenge_min_severity);
  const save = useMutation({ ...setRepositoryReviewMutation(), onSuccess: () => qc.invalidateQueries({ queryKey }) });
  const changed = execution !== repo.execution || threshold !== repo.challenge_min_severity;
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("repos.review.label")}</CardTitle>
      </CardHeader>
      <div className="grid gap-4 p-4">
        <div className="flex items-start gap-3">
          <Switch
            id="repo-execution"
            checked={execution}
            disabled={!canEdit}
            onCheckedChange={setExecution}
            aria-describedby="repo-execution-hint"
          />
          <div className="grid gap-1">
            <label htmlFor="repo-execution" className="text-ink">
              {t("repos.execution")}
            </label>
            <span id="repo-execution-hint" className="t-hint">
              {t("repos.execution.hint")}
            </span>
          </div>
        </div>
        <div className="grid gap-1.5">
          <span className="t-label" aria-hidden="true">
            {t("repos.threshold")}
          </span>
          <Segmented
            label={t("repos.threshold")}
            disabled={!canEdit}
            className="justify-self-start"
            options={(["blocker", "major", "minor", "nit"] as const).map((v) => ({ value: v, label: t(`severity.${v}`) }))}
            value={threshold}
            onChange={setThreshold}
          />
          <span className="t-hint">{t("repos.threshold.hint")}</span>
        </div>
        {canEdit ? (
          <div className="flex items-center gap-3">
            <Button
              mutation={save}
              disabled={!changed}
              onClick={() => save.mutate({ path: { owner, name }, body: { execution, challenge_min_severity: threshold } })}
            >
              {t("repos.review.save")}
            </Button>
          </div>
        ) : null}
        <ErrorAlert error={save.error} />
      </div>
    </Card>
  );
}
