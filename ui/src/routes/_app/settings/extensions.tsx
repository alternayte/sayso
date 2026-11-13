import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  deleteExtensionMutation,
  listExtensionsOptions,
  listExtensionsQueryKey,
  putExtensionMutation,
} from "@/client/@tanstack/react-query.gen";
import type { ExtensionBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SecretField, type SecretValue } from "@/components/secret-field";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/extensions")({ beforeLoad: requireRole("admin"), component: Extensions });

function Extensions() {
  const list = useQuery(listExtensionsOptions());
  const [editing, setEditing] = useState<string | null>(null);
  return (
    <>
      <PageHead
        title={t("extensions.title")}
        hint={t("extensions.hint")}
        actions={
          editing === null ? (
            <Button size="sm" onClick={() => setEditing("")}>
              {t("extensions.add")}
            </Button>
          ) : null
        }
      />
      <Card>
        {editing === "" ? <ExtensionForm onDone={() => setEditing(null)} /> : null}
        {list.isPending ? (
          <Skeleton className="m-4 h-20" />
        ) : list.error ? (
          <ErrorAlert error={list.error} className="m-4" onRetry={() => list.refetch()} />
        ) : list.data.length === 0 && editing !== "" ? (
          <p className="t-hint p-4">{t("extensions.empty")}</p>
        ) : (
          <ul className="divide-y divide-hairline">
            {list.data.map((x) =>
              editing === x.name ? (
                <li key={x.name}>
                  <ExtensionForm current={x} onDone={() => setEditing(null)} />
                </li>
              ) : (
                <ExtensionRow key={x.name} x={x} onEdit={() => setEditing(x.name)} />
              ),
            )}
          </ul>
        )}
      </Card>
    </>
  );
}

function ExtensionRow({ x, onEdit }: { x: ExtensionBody; onEdit: () => void }) {
  const qc = useQueryClient();
  const del = useMutation({ ...deleteExtensionMutation(), onSuccess: () => qc.invalidateQueries({ queryKey: listExtensionsQueryKey() }) });
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="t-value text-ink">{x.name}</span>
        <span className="t-value">{x.spec}</span>
        <Badge dot tone={x.enabled ? "success" : undefined}>
          {x.enabled ? t("extensions.enabled") : t("extensions.disabled")}
        </Badge>
        <span className="t-hint min-w-0 flex-1 truncate">
          {x.repositories.length === 0 ? t("extensions.scope.all") : x.repositories.join(", ")}
        </span>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("extensions.edit")}
        </Button>
        <Button size="sm" variant="ghost" mutation={del} onClick={() => del.mutate({ path: { name: x.name } })}>
          {t("extensions.delete")}
        </Button>
      </div>
      {!x.baked ? <Alert tone="warning">{t("extensions.not_baked")}</Alert> : null}
      <ErrorAlert error={del.error} />
    </li>
  );
}

function lines(s: string): string[] {
  return s
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter(Boolean);
}

function parseEnv(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of s.split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

function ExtensionForm({ current, onDone }: { current?: ExtensionBody; onDone: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState({
    name: current?.name ?? "",
    spec: current?.spec ?? "",
    env: Object.entries(current?.env ?? {})
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    secretNames: Object.keys(current?.secrets ?? {}).join("\n"),
    enabled: current?.enabled ?? true,
    repositories: (current?.repositories ?? []).join("\n"),
  });
  const [secrets, setSecrets] = useState<Record<string, SecretValue>>({});
  const save = useMutation({
    ...putExtensionMutation(),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: listExtensionsQueryKey() });
      onDone();
    },
  });
  const names = lines(f.secretNames);
  return (
    <form
      className="grid max-w-[640px] gap-4 border-b border-hairline p-4"
      onSubmit={(e) => {
        e.preventDefault();
        const out: Record<string, { value?: string; env?: string }> = {};
        for (const n of names) {
          const v = secrets[n];
          if (v && (v.value || v.env)) out[n] = v;
        }
        save.mutate({
          path: { name: f.name },
          body: { spec: f.spec, env: parseEnv(f.env), secrets: out, enabled: f.enabled, repositories: lines(f.repositories) },
        });
      }}
    >
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="ext-name">
          {t("extensions.name")}
        </label>
        <Input mono id="ext-name" value={f.name} disabled={!!current} onChange={(e) => setF({ ...f, name: e.target.value.trim() })} />
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="ext-spec">
          {t("extensions.spec")}
        </label>
        <Input
          mono
          id="ext-spec"
          aria-describedby="ext-spec-hint"
          value={f.spec}
          onChange={(e) => setF({ ...f, spec: e.target.value.trim() })}
        />
        <span id="ext-spec-hint" className="t-hint">
          {t("extensions.spec.hint")}
        </span>
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="ext-env">
          {t("extensions.env")}
        </label>
        <textarea
          id="ext-env"
          aria-describedby="ext-env-hint"
          className="control control-mono min-h-16"
          value={f.env}
          onChange={(e) => setF({ ...f, env: e.target.value })}
        />
        <span id="ext-env-hint" className="t-hint">
          {t("extensions.env.hint")}
        </span>
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="ext-secret-names">
          {t("extensions.secret_names")}
        </label>
        <textarea
          id="ext-secret-names"
          aria-describedby="ext-secret-names-hint"
          className="control control-mono min-h-16"
          value={f.secretNames}
          onChange={(e) => setF({ ...f, secretNames: e.target.value })}
        />
        <span id="ext-secret-names-hint" className="t-hint">
          {t("extensions.secret_names.hint")}
        </span>
      </div>
      {names.map((n) => (
        <SecretField
          key={n}
          label={n}
          field={current?.secrets[n]}
          value={secrets[n]}
          onChange={(v) => setSecrets((s) => ({ ...s, [n]: v }))}
        />
      ))}
      <div className="flex items-start gap-3">
        <Switch id="ext-enabled" checked={f.enabled} onCheckedChange={(enabled) => setF({ ...f, enabled })} />
        <label htmlFor="ext-enabled" className="text-ink">
          {t("extensions.enabled.label")}
        </label>
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="ext-repos">
          {t("extensions.repositories")}
        </label>
        <textarea
          id="ext-repos"
          aria-describedby="ext-repos-hint"
          className="control control-mono min-h-16"
          value={f.repositories}
          onChange={(e) => setF({ ...f, repositories: e.target.value })}
        />
        <span id="ext-repos-hint" className="t-hint">
          {t("extensions.repositories.hint")}
        </span>
      </div>
      <ErrorAlert error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" mutation={save} disabled={f.name === "" || f.spec === ""}>
          {t("extensions.save")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("extensions.cancel")}
        </Button>
      </div>
    </form>
  );
}
