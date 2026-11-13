import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  createMcpServerMutation,
  deleteMcpServerMutation,
  listMcpServersOptions,
  listMcpServersQueryKey,
  testMcpServerMutation,
  updateMcpServerMutation,
} from "@/client/@tanstack/react-query.gen";
import type { ServerBody, ServerInput, ToolInfo } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SecretField, type SecretValue } from "@/components/secret-field";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/mcp")({ beforeLoad: requireRole("admin"), component: McpServers });

function McpServers() {
  const list = useQuery(listMcpServersOptions());
  const [editing, setEditing] = useState<string | null>(null);
  return (
    <>
      <PageHead
        title={t("mcp.title")}
        hint={t("mcp.hint")}
        actions={
          editing === null ? (
            <Button size="sm" onClick={() => setEditing("")}>
              {t("mcp.add")}
            </Button>
          ) : null
        }
      />
      <Card>
        {editing === "" ? <ServerForm onDone={() => setEditing(null)} /> : null}
        {list.isPending ? (
          <Skeleton className="m-4 h-20" />
        ) : list.error ? (
          <ErrorAlert error={list.error} className="m-4" onRetry={() => list.refetch()} />
        ) : list.data.length === 0 && editing !== "" ? (
          <p className="t-hint p-4">{t("mcp.empty")}</p>
        ) : (
          <ul className="divide-y divide-hairline">
            {list.data.map((s) =>
              editing === s.name ? (
                <li key={s.name}>
                  <ServerForm current={s} onDone={() => setEditing(null)} />
                </li>
              ) : (
                <ServerRow key={s.name} s={s} onEdit={() => setEditing(s.name)} />
              ),
            )}
          </ul>
        )}
      </Card>
    </>
  );
}

function ToolList({ tools }: { tools: ToolInfo[] }) {
  if (tools.length === 0) return <p className="t-hint">{t("mcp.tools.none")}</p>;
  return (
    <ul className="grid gap-1">
      {tools.map((x) => (
        <li key={x.name} className="flex flex-wrap items-center gap-2 text-[13px]">
          <span className="t-value text-ink">{x.name}</span>
          <span className="badge">{x.read_only ? t("mcp.tool.readonly") : t("mcp.tool.write")}</span>
          <Badge dot tone={x.allowed ? "success" : "danger"}>
            {x.allowed ? t("mcp.tool.allowed") : t("mcp.tool.blocked")}
          </Badge>
          <span className="t-hint min-w-0 flex-1 truncate">{x.description}</span>
        </li>
      ))}
    </ul>
  );
}

function ServerRow({ s, onEdit }: { s: ServerBody; onEdit: () => void }) {
  const qc = useQueryClient();
  const refresh = () => qc.invalidateQueries({ queryKey: listMcpServersQueryKey() });
  const test = useMutation({ ...testMcpServerMutation(), onSettled: refresh });
  const del = useMutation({ ...deleteMcpServerMutation(), onSuccess: refresh });
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="t-value text-ink">{s.name}</span>
        <span className="t-hint min-w-0 flex-1 truncate">{s.url}</span>
        <Button size="sm" mutation={test} onClick={() => test.mutate({ path: { name: s.name } })}>
          {t("mcp.test")}
        </Button>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("mcp.edit")}
        </Button>
        <Button size="sm" variant="ghost" mutation={del} onClick={() => del.mutate({ path: { name: s.name } })}>
          {t("mcp.delete")}
        </Button>
      </div>
      <ToolList tools={test.data ?? s.tools} />
      <ErrorAlert error={test.error ?? del.error} />
    </li>
  );
}

function lines(s: string): string[] {
  return s
    .split("\n")
    .map((x) => x.trim())
    .filter(Boolean);
}

function parseHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of lines(s)) {
    const i = line.indexOf(":");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

function ServerForm({ current, onDone }: { current?: ServerBody; onDone: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState({
    name: current?.name ?? "",
    url: current?.url ?? "",
    headers: Object.entries(current?.headers ?? {})
      .map(([k, v]) => `${k}: ${v}`)
      .join("\n"),
    secret_header: current?.secret_header ?? "",
    allow_write: current?.allow_write ?? false,
    patterns: (current?.allow_tools ?? []).join("\n"),
  });
  const [secret, setSecret] = useState<SecretValue>(undefined);
  const done = async () => {
    await qc.invalidateQueries({ queryKey: listMcpServersQueryKey() });
    onDone();
  };
  const create = useMutation({ ...createMcpServerMutation(), onSuccess: done });
  const update = useMutation({ ...updateMcpServerMutation(), onSuccess: done });
  const save = current ? update : create;
  const body = (): ServerInput => ({
    name: f.name,
    url: f.url,
    headers: parseHeaders(f.headers),
    secret_header: f.secret_header || undefined,
    secret: secret && (secret.value || secret.env) ? secret : undefined,
    allow_write: f.allow_write,
    allow_tools: f.allow_write ? lines(f.patterns) : [],
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
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="mcp-name">
          {t("mcp.name")}
        </label>
        <Input mono id="mcp-name" value={f.name} disabled={!!current} onChange={(e) => setF({ ...f, name: e.target.value.trim() })} />
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="mcp-url">
          {t("mcp.url")}
        </label>
        <Input mono id="mcp-url" value={f.url} onChange={(e) => setF({ ...f, url: e.target.value.trim() })} />
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="mcp-headers">
          {t("mcp.headers")}
        </label>
        <textarea
          id="mcp-headers"
          aria-describedby="mcp-headers-hint"
          className="control control-mono min-h-16"
          value={f.headers}
          onChange={(e) => setF({ ...f, headers: e.target.value })}
        />
        <span id="mcp-headers-hint" className="t-hint">
          {t("mcp.headers.hint")}
        </span>
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="mcp-secret-header">
          {t("mcp.secret_header")}
        </label>
        <Input mono id="mcp-secret-header" value={f.secret_header} onChange={(e) => setF({ ...f, secret_header: e.target.value.trim() })} />
      </div>
      <SecretField label={t("mcp.secret")} field={current?.secret} value={secret} onChange={setSecret} />
      <div className="flex items-start gap-3">
        <Switch
          id="mcp-allow-write"
          checked={f.allow_write}
          onCheckedChange={(v) => setF({ ...f, allow_write: v })}
          aria-describedby="mcp-allow-write-hint"
        />
        <div className="grid gap-1">
          <label htmlFor="mcp-allow-write" className="text-ink">
            {t("mcp.allow_write")}
          </label>
          <span id="mcp-allow-write-hint" className="t-hint">
            {t("mcp.allow_write.hint")}
          </span>
        </div>
      </div>
      {f.allow_write ? (
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="mcp-patterns">
            {t("mcp.patterns")}
          </label>
          <textarea
            id="mcp-patterns"
            aria-describedby="mcp-patterns-hint"
            className="control control-mono min-h-16"
            value={f.patterns}
            onChange={(e) => setF({ ...f, patterns: e.target.value })}
          />
          <span id="mcp-patterns-hint" className="t-hint">
            {t("mcp.patterns.hint")}
          </span>
        </div>
      ) : null}
      <ErrorAlert error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" mutation={save} disabled={f.name === "" || f.url === ""}>
          {t("mcp.save")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("mcp.cancel")}
        </Button>
      </div>
    </form>
  );
}
