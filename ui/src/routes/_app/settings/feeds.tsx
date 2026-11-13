import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  deleteFeedMutation,
  listFeedsOptions,
  listFeedsQueryKey,
  putFeedMutation,
  testFeedMutation,
} from "@/client/@tanstack/react-query.gen";
import type { FeedBody, FeedInput } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead, Segmented } from "@/components/app-ui";
import { SecretField, type SecretValue } from "@/components/secret-field";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/feeds")({ beforeLoad: requireRole("admin"), component: Feeds });

const kinds = ["npm", "nuget", "pypi", "maven", "goproxy", "github"] as const;
type Kind = (typeof kinds)[number];
type Mode = "proxy" | "inject";
type Auth = "basic" | "bearer" | "header";

function Feeds() {
  const list = useQuery(listFeedsOptions());
  const [editing, setEditing] = useState<string | null>(null);
  return (
    <>
      <PageHead
        title={t("feeds.title")}
        hint={t("feeds.hint")}
        actions={
          editing === null ? (
            <Button size="sm" onClick={() => setEditing("")}>
              {t("feeds.add")}
            </Button>
          ) : null
        }
      />
      <Card>
        {editing === "" ? <FeedForm onDone={() => setEditing(null)} /> : null}
        {list.isPending ? (
          <Skeleton className="m-4 h-20" />
        ) : list.error ? (
          <ErrorAlert error={list.error} className="m-4" onRetry={() => list.refetch()} />
        ) : list.data.length === 0 && editing !== "" ? (
          <p className="t-hint p-4">{t("feeds.empty")}</p>
        ) : (
          <ul className="divide-y divide-hairline">
            {list.data.map((f) =>
              editing === f.name ? (
                <li key={f.name}>
                  <FeedForm current={f} onDone={() => setEditing(null)} />
                </li>
              ) : (
                <FeedRow key={f.name} f={f} onEdit={() => setEditing(f.name)} />
              ),
            )}
          </ul>
        )}
      </Card>
    </>
  );
}

function InjectWarning({ by }: { by?: string }) {
  return (
    <Alert tone="warning">
      <p className="text-ink">{t("feeds.inject.warning")}</p>
      {by ? <p className="t-hint">{t("feeds.inject.confirmed_by", { login: by })}</p> : null}
    </Alert>
  );
}

function FeedRow({ f, onEdit }: { f: FeedBody; onEdit: () => void }) {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: listFeedsQueryKey() });
  const test = useMutation({ ...testFeedMutation(), onSettled: refresh });
  const del = useMutation({ ...deleteFeedMutation(), onSuccess: refresh });
  const scope = f.allowed_repositories.length === 0 ? t("feeds.scope.all") : f.allowed_repositories.join(", ");
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="t-value text-ink">{f.name}</span>
        <span className="badge">{f.kind}</span>
        <Badge dot tone={f.mode === "inject" ? "warning" : "success"}>
          {f.mode === "inject" ? t("feeds.mode.inject") : t("feeds.mode.proxy")}
        </Badge>
        <span className="t-hint min-w-0 flex-1 truncate">{f.kind === "github" ? f.repos.join(", ") : f.upstream_url}</span>
        <Button size="sm" mutation={test} onClick={() => test.mutate({ path: { name: f.name } })}>
          {t("feeds.test")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("feeds.edit")}
        </Button>
        <Button size="sm" variant="ghost" mutation={del} onClick={() => del.mutate({ path: { name: f.name } })}>
          {t("feeds.delete")}
        </Button>
      </div>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[13px]">
        <dt className="t-hint">{t("feeds.proxy_url")}</dt>
        <dd className="t-value min-w-0 truncate">{f.proxy_url}</dd>
        <dt className="t-hint">{t("feeds.allowed")}</dt>
        <dd className="min-w-0 truncate">{scope}</dd>
        <dt className="t-hint">{t("feeds.cache")}</dt>
        <dd>{f.cache_enabled ? t("feeds.cache.on", { mb: String(f.cache_max_mb) }) : t("feeds.cache.off")}</dd>
      </dl>
      {f.credential_exposed ? <InjectWarning by={f.inject_confirmed_by} /> : null}
      {test.data ? (
        <Badge dot tone={test.data.ok ? "success" : "danger"}>
          {test.data.ok ? t("feeds.test.passed") : t("feeds.test.failed")} · {test.data.detail}
        </Badge>
      ) : null}
      <ErrorAlert error={test.error ?? del.error} />
    </li>
  );
}

function lines(s: string): string[] {
  return s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

function FeedForm({ current, onDone }: { current?: FeedBody; onDone: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState({
    name: current?.name ?? "",
    kind: (current?.kind ?? "npm") as Kind,
    upstream_url: current?.upstream_url ?? "",
    auth_style: (current?.auth_style || "basic") as Auth,
    auth_header: current?.auth_header ?? "",
    username: current?.username ?? "",
    mode: (current?.mode ?? "proxy") as Mode,
    match_urls: (current?.match_urls ?? []).join("\n"),
    repos: (current?.repos ?? []).join("\n"),
    allowed: (current?.allowed_repositories ?? []).join("\n"),
    cache_enabled: current?.cache_enabled ?? true,
    cache_max_mb: String(current?.cache_max_mb ?? 2048),
  });
  const [secret, setSecret] = useState<SecretValue>(undefined);
  const [confirm, setConfirm] = useState(false);
  const needsConfirm = f.mode === "inject" && current?.mode !== "inject";
  const save = useMutation({
    ...putFeedMutation(),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: listFeedsQueryKey() });
      onDone();
    },
  });
  const github = f.kind === "github";
  const body = (): FeedInput => ({
    name: f.name,
    kind: f.kind,
    upstream_url: github ? undefined : f.upstream_url,
    auth_style: github ? undefined : f.auth_style,
    auth_header: !github && f.auth_style === "header" ? f.auth_header : undefined,
    username: !github && f.auth_style === "basic" ? f.username : undefined,
    secret: !github && secret && (secret.value || secret.env) ? secret : undefined,
    mode: github ? "proxy" : f.mode,
    match_urls: github ? [] : lines(f.match_urls),
    repos: github ? lines(f.repos) : [],
    allowed_repositories: lines(f.allowed),
    confirm_inject: needsConfirm ? confirm : undefined,
    cache_enabled: f.cache_enabled,
    cache_max_mb: Number(f.cache_max_mb) || 0,
  });
  return (
    <form
      className="grid max-w-[640px] gap-4 border-b border-hairline p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate({ path: { name: f.name }, body: body() });
      }}
    >
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="feed-name">
          {t("feeds.name")}
        </label>
        <Input mono id="feed-name" value={f.name} disabled={!!current} onChange={(e) => setF({ ...f, name: e.target.value.trim() })} />
      </div>
      <Choice
        label={t("feeds.kind")}
        options={kinds.map((k) => ({ value: k, label: k }))}
        value={f.kind}
        onChange={(kind) => setF({ ...f, kind })}
      />
      {github ? (
        <TextList
          id="feed-repos"
          label={t("feeds.repos")}
          hint={t("feeds.repos.hint")}
          value={f.repos}
          onChange={(repos) => setF({ ...f, repos })}
        />
      ) : (
        <>
          <div className="grid gap-1.5">
            <label className="t-label" htmlFor="feed-upstream">
              {t("feeds.upstream_url")}
            </label>
            <Input mono id="feed-upstream" value={f.upstream_url} onChange={(e) => setF({ ...f, upstream_url: e.target.value.trim() })} />
          </div>
          <Choice
            label={t("feeds.auth_style")}
            options={[
              { value: "basic", label: t("feeds.auth.basic") },
              { value: "bearer", label: t("feeds.auth.bearer") },
              { value: "header", label: t("feeds.auth.header") },
            ]}
            value={f.auth_style}
            onChange={(auth_style) => setF({ ...f, auth_style })}
          />
          {f.auth_style === "basic" ? (
            <div className="grid gap-1.5">
              <label className="t-label" htmlFor="feed-username">
                {t("feeds.username")}
              </label>
              <Input mono id="feed-username" value={f.username} onChange={(e) => setF({ ...f, username: e.target.value.trim() })} />
            </div>
          ) : null}
          {f.auth_style === "header" ? (
            <div className="grid gap-1.5">
              <label className="t-label" htmlFor="feed-header">
                {t("feeds.auth_header")}
              </label>
              <Input mono id="feed-header" value={f.auth_header} onChange={(e) => setF({ ...f, auth_header: e.target.value.trim() })} />
            </div>
          ) : null}
          <SecretField label={t("feeds.secret")} field={current?.secret} value={secret} onChange={setSecret} />
          <TextList
            id="feed-match"
            label={t("feeds.match_urls")}
            hint={t("feeds.match_urls.hint")}
            value={f.match_urls}
            onChange={(match_urls) => setF({ ...f, match_urls })}
          />
          <Choice
            label={t("feeds.mode")}
            options={[
              { value: "proxy", label: t("feeds.mode.proxy") },
              { value: "inject", label: t("feeds.mode.inject") },
            ]}
            value={f.mode}
            onChange={(mode) => {
              setF({ ...f, mode });
              setConfirm(false);
            }}
          />
          {f.mode === "inject" ? (
            <div className="grid gap-3">
              <InjectWarning by={current?.mode === "inject" ? current.inject_confirmed_by : undefined} />
              {needsConfirm ? (
                <div className="flex items-start gap-3">
                  <Switch id="feed-confirm" checked={confirm} onCheckedChange={setConfirm} />
                  <label htmlFor="feed-confirm" className="text-ink">
                    {t("feeds.inject.confirm")}
                  </label>
                </div>
              ) : null}
            </div>
          ) : null}
        </>
      )}
      <TextList
        id="feed-allowed"
        label={t("feeds.allowed")}
        hint={t("feeds.allowed.hint")}
        value={f.allowed}
        onChange={(allowed) => setF({ ...f, allowed })}
      />
      <div className="flex items-start gap-3">
        <Switch
          id="feed-cache"
          checked={f.cache_enabled}
          onCheckedChange={(cache_enabled) => setF({ ...f, cache_enabled })}
          aria-describedby="feed-cache-hint"
        />
        <div className="grid gap-1">
          <label htmlFor="feed-cache" className="text-ink">
            {t("feeds.cache.label")}
          </label>
          <span id="feed-cache-hint" className="t-hint">
            {t("feeds.cache.hint")}
          </span>
        </div>
      </div>
      {f.cache_enabled ? (
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="feed-cache-mb">
            {t("feeds.cache_max_mb")}
          </label>
          <Input
            mono
            id="feed-cache-mb"
            inputMode="numeric"
            value={f.cache_max_mb}
            onChange={(e) => setF({ ...f, cache_max_mb: e.target.value.replace(/\D/g, "") })}
          />
        </div>
      ) : null}
      <ErrorAlert error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" mutation={save} disabled={f.name === "" || (needsConfirm && !confirm)}>
          {t("feeds.save")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("feeds.cancel")}
        </Button>
      </div>
    </form>
  );
}

function TextList({
  id,
  label,
  hint,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="grid gap-1.5">
      <label className="t-label" htmlFor={id}>
        {label}
      </label>
      <textarea
        id={id}
        aria-describedby={`${id}-hint`}
        className="control control-mono min-h-16"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
      <span id={`${id}-hint`} className="t-hint">
        {hint}
      </span>
    </div>
  );
}

function Choice<T extends string>(p: { label: string; options: { value: T; label: string }[]; value: T; onChange: (v: T) => void }) {
  return (
    <div className="grid gap-1.5">
      <span className="t-label" aria-hidden="true">
        {p.label}
      </span>
      <Segmented {...p} className="justify-self-start" />
    </div>
  );
}
