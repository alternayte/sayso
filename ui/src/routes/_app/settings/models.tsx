import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  createProviderMutation,
  deleteProviderMutation,
  listProfilesOptions,
  listProfilesQueryKey,
  listProviderPresetsOptions,
  listProvidersOptions,
  listProvidersQueryKey,
  putProfileMutation,
  testProviderMutation,
  updateProviderMutation,
} from "@/client/@tanstack/react-query.gen";
import type { ProfileBody, ProviderBody, ProviderInput, Smoke, Target } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SecretField, type SecretValue } from "@/components/secret-field";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/models")({ beforeLoad: requireRole("admin"), component: Models });

const apiTypes: ProviderInput["api_type"][] = ["openai-completions", "openai-responses", "anthropic-messages", "google-generative-ai"];
const authStyles: ProviderInput["auth_style"][] = ["bearer", "x_api_key", "api_key", "header"];

function Models() {
  return (
    <>
      <PageHead title={t("models.title")} hint={t("models.hint")} />
      <div className="grid gap-5">
        <Providers />
        <Profiles />
      </div>
    </>
  );
}

function smokeBadge(s: Smoke | undefined) {
  if (!s) return <Badge>{t("providers.untested")}</Badge>;
  const ok = s.checks.every((c) => c.ok);
  return (
    <Badge dot tone={ok ? "success" : "danger"}>
      {ok ? t("providers.passed") : t("providers.failed")}
    </Badge>
  );
}

function Providers() {
  const list = useQuery(listProvidersOptions());
  const [editing, setEditing] = useState<string | null>(null);
  return (
    <Card>
      <CardHeader className="flex items-center justify-between gap-3">
        <CardTitle>{t("providers.title")}</CardTitle>
        {editing === null ? (
          <Button size="sm" onClick={() => setEditing("")}>
            {t("providers.add")}
          </Button>
        ) : null}
      </CardHeader>
      {editing === "" ? <ProviderForm onDone={() => setEditing(null)} /> : null}
      {list.isPending ? (
        <Skeleton className="m-4 h-20" />
      ) : list.error ? (
        <ErrorAlert error={list.error} className="m-4" onRetry={() => list.refetch()} />
      ) : list.data.length === 0 && editing !== "" ? (
        <p className="t-hint p-4">{t("providers.empty")}</p>
      ) : (
        <ul className="divide-y divide-hairline">
          {list.data.map((p) =>
            editing === p.name ? (
              <li key={p.name}>
                <ProviderForm current={p} onDone={() => setEditing(null)} />
              </li>
            ) : (
              <ProviderRow key={p.name} p={p} onEdit={() => setEditing(p.name)} />
            ),
          )}
        </ul>
      )}
    </Card>
  );
}

function ProviderRow({ p, onEdit }: { p: ProviderBody; onEdit: () => void }) {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: listProvidersQueryKey() });
  const test = useMutation({ ...testProviderMutation(), onSettled: refresh });
  const del = useMutation({ ...deleteProviderMutation(), onSuccess: refresh });
  const smoke = test.data ?? p.smoke;
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="t-value text-ink">{p.name}</span>
        <span className="badge">{p.api_type}</span>
        {smokeBadge(smoke)}
        <span className="t-hint min-w-0 flex-1 truncate">{p.base_url}</span>
        <Button size="sm" mutation={test} onClick={() => test.mutate({ path: { name: p.name }, body: {} })}>
          {t("providers.test")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("providers.edit")}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          mutation={del}
          disabled={p.used_by.length > 0}
          onClick={() => del.mutate({ path: { name: p.name } })}
        >
          {t("providers.delete")}
        </Button>
      </div>
      {p.key.set ? <span className="t-hint t-value">•••• {p.key.last4 || p.key.env_name}</span> : null}
      {p.used_by.length > 0 ? <span className="t-hint">{t("providers.used_by", { profiles: p.used_by.join(", ") })}</span> : null}
      {smoke ? (
        <ul className="grid gap-1">
          {smoke.checks.map((c) => (
            <li key={c.name} className="flex flex-wrap items-center gap-2 text-[13px]">
              <Badge dot tone={c.ok ? "success" : "danger"}>
                {c.name}
              </Badge>
              <span className="min-w-0 flex-1 break-words">{c.detail}</span>
              <span className="t-hint t-value">{c.ms} ms</span>
            </li>
          ))}
        </ul>
      ) : null}
      {smoke && !smoke.checks.find((c) => c.name === "tool_call")?.ok ? (
        <p role="alert" className="text-[13px] text-danger">
          {t("providers.no_tools")}
        </p>
      ) : null}
      <ErrorAlert error={test.error ?? del.error} />
    </li>
  );
}

function headersText(h: Record<string, string>) {
  return Object.entries(h)
    .map(([k, v]) => `${k}: ${v}`)
    .join("\n");
}

function parseHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of s.split("\n")) {
    const i = line.indexOf(":");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

function ProviderForm({ current, onDone }: { current?: ProviderBody; onDone: () => void }) {
  const qc = useQueryClient();
  const presets = useQuery(listProviderPresetsOptions());
  const [f, setF] = useState({
    name: current?.name ?? "",
    api_type: (current?.api_type ?? "openai-completions") as ProviderInput["api_type"],
    base_url: current?.base_url ?? "",
    auth_style: (current?.auth_style ?? "bearer") as ProviderInput["auth_style"],
    auth_header: current?.auth_header ?? "",
    models: (current?.models ?? []).join("\n"),
    headers: headersText(current?.headers ?? {}),
    max_concurrency: current?.max_concurrency ?? 0,
    tokens_per_minute: current?.tokens_per_minute ?? 0,
  });
  const [key, setKey] = useState<SecretValue>(undefined);
  const done = async () => {
    await qc.invalidateQueries({ queryKey: listProvidersQueryKey() });
    onDone();
  };
  const create = useMutation({ ...createProviderMutation(), onSuccess: done });
  const update = useMutation({ ...updateProviderMutation(), onSuccess: done });
  const save = current ? update : create;
  const body = (): ProviderInput => ({
    name: f.name,
    api_type: f.api_type,
    base_url: f.base_url,
    auth_style: f.auth_style,
    auth_header: f.auth_style === "header" ? f.auth_header : undefined,
    key: key && (key.value || key.env) ? key : undefined,
    models: f.models
      .split("\n")
      .map((m) => m.trim())
      .filter(Boolean),
    headers: parseHeaders(f.headers),
    max_concurrency: f.max_concurrency,
    tokens_per_minute: f.tokens_per_minute,
  });
  return (
    <form
      className="grid max-w-[640px] gap-4 border-b border-hairline p-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (current) update.mutate({ path: { name: current.name }, body: body() });
        else create.mutate({ body: body() });
      }}
    >
      {!current ? (
        <label className="grid gap-1.5">
          <span className="t-label">{t("providers.preset")}</span>
          <select
            className="control"
            defaultValue=""
            onChange={(e) => {
              const p = presets.data?.find((x) => x.id === e.target.value);
              if (p)
                setF({
                  ...f,
                  api_type: p.api_type as ProviderInput["api_type"],
                  base_url: p.base_url,
                  auth_style: p.auth_style as ProviderInput["auth_style"],
                  auth_header: p.auth_header ?? "",
                  headers: headersText(p.headers ?? {}),
                });
            }}
          >
            <option value="">{t("providers.preset.none")}</option>
            {(presets.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.label}
              </option>
            ))}
          </select>
        </label>
      ) : null}
      <label className="grid gap-1.5">
        <span className="t-label">{t("providers.name")}</span>
        <Input mono value={f.name} disabled={!!current} onChange={(e) => setF({ ...f, name: e.target.value.trim() })} />
      </label>
      <div className="grid gap-4 sm:grid-cols-2">
        <label className="grid gap-1.5">
          <span className="t-label">{t("providers.api_type")}</span>
          <select
            className="control"
            value={f.api_type}
            onChange={(e) => setF({ ...f, api_type: e.target.value as ProviderInput["api_type"] })}
          >
            {apiTypes.map((a) => (
              <option key={a}>{a}</option>
            ))}
          </select>
        </label>
        <label className="grid gap-1.5">
          <span className="t-label">{t("providers.auth_style")}</span>
          <select
            className="control"
            value={f.auth_style}
            onChange={(e) => setF({ ...f, auth_style: e.target.value as ProviderInput["auth_style"] })}
          >
            {authStyles.map((a) => (
              <option key={a}>{a}</option>
            ))}
          </select>
        </label>
      </div>
      <label className="grid gap-1.5">
        <span className="t-label">{t("providers.base_url")}</span>
        <Input mono value={f.base_url} onChange={(e) => setF({ ...f, base_url: e.target.value.trim() })} />
      </label>
      {f.auth_style === "header" ? (
        <label className="grid gap-1.5">
          <span className="t-label">{t("providers.auth_header")}</span>
          <Input mono value={f.auth_header} onChange={(e) => setF({ ...f, auth_header: e.target.value.trim() })} />
        </label>
      ) : null}
      <SecretField label={t("providers.key")} field={current?.key} value={key} onChange={setKey} />
      <label className="grid gap-1.5">
        <span className="t-label">{t("providers.models")}</span>
        <textarea className="control control-mono min-h-20" value={f.models} onChange={(e) => setF({ ...f, models: e.target.value })} />
        <span className="t-hint">{t("providers.models.hint")}</span>
      </label>
      <label className="grid gap-1.5">
        <span className="t-label">{t("providers.headers")}</span>
        <textarea className="control control-mono min-h-16" value={f.headers} onChange={(e) => setF({ ...f, headers: e.target.value })} />
        <span className="t-hint">{t("providers.headers.hint")}</span>
      </label>
      <div className="grid gap-4 sm:grid-cols-2">
        <label className="grid gap-1.5">
          <span className="t-label">{t("providers.max_concurrency")}</span>
          <Input
            mono
            type="number"
            min={0}
            value={f.max_concurrency}
            onChange={(e) => setF({ ...f, max_concurrency: Number(e.target.value) })}
          />
        </label>
        <label className="grid gap-1.5">
          <span className="t-label">{t("providers.tpm")}</span>
          <Input
            mono
            type="number"
            min={0}
            value={f.tokens_per_minute}
            onChange={(e) => setF({ ...f, tokens_per_minute: Number(e.target.value) })}
          />
        </label>
      </div>
      <span className="t-hint">{t("providers.limits.hint")}</span>
      <ErrorAlert error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" mutation={save} disabled={f.name === "" || f.base_url === ""}>
          {t("providers.save")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("providers.cancel")}
        </Button>
      </div>
    </form>
  );
}

function Profiles() {
  const list = useQuery(listProfilesOptions());
  const providers = useQuery(listProvidersOptions());
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("profiles.title")}</CardTitle>
      </CardHeader>
      <p className="t-hint px-4 pt-3">{t("profiles.hint")}</p>
      {list.isPending ? (
        <Skeleton className="m-4 h-20" />
      ) : list.error ? (
        <ErrorAlert error={list.error} className="m-4" onRetry={() => list.refetch()} />
      ) : (
        <ul className="divide-y divide-hairline">
          {list.data.map((p) => (
            <ProfileEditor key={p.name} profile={p} providers={providers.data ?? []} />
          ))}
        </ul>
      )}
    </Card>
  );
}

function ProfileEditor({ profile, providers }: { profile: ProfileBody; providers: ProviderBody[] }) {
  const qc = useQueryClient();
  const [targets, setTargets] = useState<Target[]>(profile.targets);
  const [budget, setBudget] = useState(profile.round_budget_usd);
  const save = useMutation({ ...putProfileMutation(), onSuccess: () => qc.invalidateQueries({ queryKey: listProfilesQueryKey() }) });
  const set = (i: number, patch: Partial<Target>) => setTargets(targets.map((x, j) => (i === j ? { ...x, ...patch } : x)));
  return (
    <li className="grid gap-3 p-4">
      <h3 className="t-section">{profile.name}</h3>
      {targets.length === 0 ? <p className="t-hint">{t("profiles.none")}</p> : null}
      {targets.map((x, i) => (
        <div key={i} className="grid gap-2 sm:grid-cols-[1fr_1fr_repeat(3,90px)_auto] sm:items-end">
          <label className="grid gap-1">
            <span className="t-label">{t("profiles.provider")}</span>
            <select className="control" value={x.provider} onChange={(e) => set(i, { provider: e.target.value })}>
              <option value="" />
              {providers.map((p) => (
                <option key={p.name}>{p.name}</option>
              ))}
            </select>
          </label>
          <label className="grid gap-1">
            <span className="t-label">{t("profiles.model")}</span>
            <Input mono value={x.model} onChange={(e) => set(i, { model: e.target.value.trim() })} />
          </label>
          <label className="grid gap-1">
            <span className="t-label">{t("profiles.price_in")}</span>
            <Input
              mono
              type="number"
              min={0}
              step="any"
              value={x.price_in}
              onChange={(e) => set(i, { price_in: Number(e.target.value) })}
            />
          </label>
          <label className="grid gap-1">
            <span className="t-label">{t("profiles.price_out")}</span>
            <Input
              mono
              type="number"
              min={0}
              step="any"
              value={x.price_out}
              onChange={(e) => set(i, { price_out: Number(e.target.value) })}
            />
          </label>
          <label className="grid gap-1">
            <span className="t-label">{t("profiles.price_cache")}</span>
            <Input
              mono
              type="number"
              min={0}
              step="any"
              value={x.price_cache_read}
              onChange={(e) => set(i, { price_cache_read: Number(e.target.value) })}
            />
          </label>
          <Button size="sm" variant="ghost" onClick={() => setTargets(targets.filter((_, j) => j !== i))}>
            {t("profiles.remove")}
          </Button>
        </div>
      ))}
      <span className="t-hint">{t("profiles.prices.hint")}</span>
      <div className="flex flex-wrap items-end gap-3">
        <Button
          size="sm"
          onClick={() => setTargets([...targets, { provider: "", model: "", price_in: 0, price_out: 0, price_cache_read: 0 }])}
        >
          {t("profiles.add_target")}
        </Button>
        <label className="grid gap-1">
          <span className="t-label">{t("profiles.budget")}</span>
          <Input
            mono
            className="w-32"
            type="number"
            min={0}
            step="any"
            value={budget}
            onChange={(e) => setBudget(Number(e.target.value))}
          />
        </label>
        <Button mutation={save} onClick={() => save.mutate({ path: { name: profile.name }, body: { targets, round_budget_usd: budget } })}>
          {t("profiles.save")}
        </Button>
      </div>
      <span className="t-hint">{t("profiles.budget.hint")}</span>
      <ErrorAlert error={save.error} />
    </li>
  );
}
