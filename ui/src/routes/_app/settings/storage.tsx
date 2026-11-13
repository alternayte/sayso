import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  deleteStoreMutation,
  listStoresOptions,
  listStoresQueryKey,
  putStoreMutation,
  testStoreMutation,
} from "@/client/@tanstack/react-query.gen";
import type { StoreBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead, Segmented } from "@/components/app-ui";
import { SecretField, type SecretValue } from "@/components/secret-field";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/storage")({ beforeLoad: requireRole("admin"), component: Storage });

type Driver = "postgres" | "s3" | "azblob";

function Storage() {
  const list = useQuery(listStoresOptions());
  const [editing, setEditing] = useState<string | null>(null);
  return (
    <>
      <PageHead
        title={t("storage.title")}
        hint={t("storage.hint")}
        actions={
          editing === null ? (
            <Button size="sm" onClick={() => setEditing("")}>
              {t("storage.add")}
            </Button>
          ) : null
        }
      />
      <Card>
        {editing === "" ? <StoreForm onDone={() => setEditing(null)} /> : null}
        {list.isPending ? (
          <Skeleton className="m-4 h-20" />
        ) : list.error ? (
          <ErrorAlert error={list.error} className="m-4" onRetry={() => void list.refetch()} />
        ) : (
          <ul className="divide-y divide-hairline">
            {list.data.map((s) =>
              editing === s.name ? (
                <li key={s.name}>
                  <StoreForm current={s} onDone={() => setEditing(null)} />
                </li>
              ) : (
                <StoreRow key={s.name} s={s} onEdit={() => setEditing(s.name)} />
              ),
            )}
          </ul>
        )}
      </Card>
    </>
  );
}

function StoreRow({ s, onEdit }: { s: StoreBody; onEdit: () => void }) {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: listStoresQueryKey() });
  const test = useMutation({ ...testStoreMutation() });
  const del = useMutation({ ...deleteStoreMutation(), onSuccess: refresh });
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="t-value text-ink">{s.name}</span>
        <span className="badge">{s.driver}</span>
        {s.default ? (
          <Badge dot tone="accent">
            {t("storage.default")}
          </Badge>
        ) : null}
        <span className="t-hint min-w-0 flex-1 truncate">{t("storage.summary", { days: s.retention_days, objects: s.objects })}</span>
        <Button size="sm" mutation={test} onClick={() => test.mutate({ path: { name: s.name } })}>
          {t("storage.test")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("storage.edit")}
        </Button>
        {!s.default && s.objects === 0 ? (
          <Button size="sm" variant="ghost" mutation={del} onClick={() => del.mutate({ path: { name: s.name } })}>
            {t("storage.delete")}
          </Button>
        ) : null}
      </div>
      {test.data ? (
        <Badge dot tone={test.data.ok ? "success" : "danger"}>
          {test.data.ok ? t("storage.test.passed") : t("storage.test.failed")} · {test.data.detail}
        </Badge>
      ) : null}
      <ErrorAlert error={test.error ?? del.error} />
    </li>
  );
}

type Fields = { [k: string]: string | boolean };

const driverFields: Record<Driver, { key: string; label: string; kind: "text" | "bool" }[]> = {
  postgres: [],
  s3: [
    { key: "endpoint", label: "storage.s3.endpoint", kind: "text" },
    { key: "region", label: "storage.s3.region", kind: "text" },
    { key: "bucket", label: "storage.s3.bucket", kind: "text" },
    { key: "prefix", label: "storage.prefix", kind: "text" },
    { key: "access_key", label: "storage.s3.access_key", kind: "text" },
    { key: "path_style", label: "storage.s3.path_style", kind: "bool" },
    { key: "insecure", label: "storage.s3.insecure", kind: "bool" },
  ],
  azblob: [
    { key: "account_url", label: "storage.az.account_url", kind: "text" },
    { key: "container", label: "storage.az.container", kind: "text" },
    { key: "prefix", label: "storage.prefix", kind: "text" },
  ],
};

function StoreForm({ current, onDone }: { current?: StoreBody; onDone: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(current?.name ?? "");
  const [driver, setDriver] = useState<Driver>((current?.driver as Driver) ?? "s3");
  const [cfg, setCfg] = useState<Fields>((current?.config as Fields) ?? {});
  const [auth, setAuth] = useState<string>(((current?.config as Fields)?.auth as string) ?? "key");
  const [days, setDays] = useState(String(current?.retention_days ?? 90));
  const [isDefault, setDefault] = useState(current?.default ?? false);
  const [secret, setSecret] = useState<SecretValue>(undefined);
  const save = useMutation({
    ...putStoreMutation(),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: listStoresQueryKey() });
      onDone();
    },
  });
  return (
    <form
      className="grid max-w-[640px] gap-4 border-b border-hairline p-4"
      onSubmit={(e) => {
        e.preventDefault();
        const config = driver === "azblob" ? { ...cfg, auth } : cfg;
        save.mutate({
          path: { name },
          body: {
            driver,
            config,
            secret: secret && (secret.value || secret.env) ? secret : undefined,
            retention_days: Number(days) || 90,
            default: isDefault,
          },
        });
      }}
    >
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="store-name">
          {t("storage.name")}
        </label>
        <Input mono id="store-name" value={name} disabled={!!current} onChange={(e) => setName(e.target.value.trim())} />
      </div>
      {!current ? (
        <div className="grid gap-1.5">
          <span className="t-label" aria-hidden="true">
            {t("storage.driver")}
          </span>
          <Segmented
            label={t("storage.driver")}
            options={(["s3", "azblob", "postgres"] as const).map((d) => ({ value: d, label: d }))}
            value={driver}
            onChange={setDriver}
          />
        </div>
      ) : null}
      {driverFields[driver].map((f) =>
        f.kind === "bool" ? (
          <div key={f.key} className="flex items-center gap-3">
            <Switch id={`store-${f.key}`} checked={!!cfg[f.key]} onCheckedChange={(v) => setCfg({ ...cfg, [f.key]: v })} />
            <label htmlFor={`store-${f.key}`} className="text-ink">
              {t(f.label as never)}
            </label>
          </div>
        ) : (
          <div key={f.key} className="grid gap-1.5">
            <label className="t-label" htmlFor={`store-${f.key}`}>
              {t(f.label as never)}
            </label>
            <Input
              mono
              id={`store-${f.key}`}
              value={(cfg[f.key] as string) ?? ""}
              onChange={(e) => setCfg({ ...cfg, [f.key]: e.target.value.trim() })}
            />
          </div>
        ),
      )}
      {driver === "azblob" ? (
        <div className="grid gap-1.5">
          <span className="t-label" aria-hidden="true">
            {t("storage.az.auth")}
          </span>
          <Segmented
            label={t("storage.az.auth")}
            options={[
              { value: "key", label: t("storage.az.auth.key") },
              { value: "connection_string", label: t("storage.az.auth.connection_string") },
              { value: "default", label: t("storage.az.auth.default") },
            ]}
            value={auth}
            onChange={setAuth}
          />
        </div>
      ) : null}
      {driver !== "postgres" ? (
        <SecretField label={t("storage.secret")} field={current?.secret} value={secret} onChange={setSecret} />
      ) : null}
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="store-days">
          {t("storage.retention")}
        </label>
        <Input mono id="store-days" inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value.replace(/\D/g, ""))} />
      </div>
      <div className="flex items-center gap-3">
        <Switch id="store-default" checked={isDefault} onCheckedChange={setDefault} />
        <label htmlFor="store-default" className="text-ink">
          {t("storage.default.label")}
        </label>
      </div>
      <ErrorAlert error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" mutation={save} disabled={name === ""}>
          {t("storage.save")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("storage.cancel")}
        </Button>
      </div>
    </form>
  );
}
