import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import {
  deleteAgentImageMutation,
  listAgentImagesOptions,
  listAgentImagesQueryKey,
  putAgentImageMutation,
} from "@/client/@tanstack/react-query.gen";
import type { ImageBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/images")({ beforeLoad: requireRole("admin"), component: Images });

function Images() {
  const list = useQuery(listAgentImagesOptions());
  const [editing, setEditing] = useState<string | null>(null);
  return (
    <>
      <PageHead
        title={t("images.title")}
        hint={t("images.hint")}
        actions={
          editing === null ? (
            <Button size="sm" onClick={() => setEditing("")}>
              {t("images.add")}
            </Button>
          ) : null
        }
      />
      <Card>
        {editing === "" ? <ImageForm onDone={() => setEditing(null)} /> : null}
        {list.isPending ? (
          <Skeleton className="m-4 h-20" />
        ) : list.error ? (
          <ErrorAlert error={list.error} className="m-4" onRetry={() => list.refetch()} />
        ) : list.data.length === 0 && editing !== "" ? (
          <p className="t-hint p-4">{t("images.empty")}</p>
        ) : (
          <ul className="divide-y divide-hairline">
            {list.data.map((x) =>
              editing === x.name ? (
                <li key={x.name}>
                  <ImageForm current={x} onDone={() => setEditing(null)} />
                </li>
              ) : (
                <ImageRow key={x.name} x={x} onEdit={() => setEditing(x.name)} />
              ),
            )}
          </ul>
        )}
      </Card>
    </>
  );
}

function ImageRow({ x, onEdit }: { x: ImageBody; onEdit: () => void }) {
  const qc = useQueryClient();
  const del = useMutation({
    ...deleteAgentImageMutation(),
    onSuccess: () => qc.invalidateQueries({ queryKey: listAgentImagesQueryKey() }),
  });
  return (
    <li className="grid gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center gap-3">
        <span className="t-value text-ink">{x.name}</span>
        {x.default ? (
          <Badge dot tone="accent">
            {t("images.default")}
          </Badge>
        ) : null}
        <span className="t-value min-w-0 flex-1 truncate">{x.image}</span>
        <Button size="sm" variant="ghost" onClick={onEdit}>
          {t("images.edit")}
        </Button>
        <Button size="sm" variant="ghost" mutation={del} onClick={() => del.mutate({ path: { name: x.name } })}>
          {t("images.delete")}
        </Button>
      </div>
      <ErrorAlert error={del.error} />
    </li>
  );
}

function ImageForm({ current, onDone }: { current?: ImageBody; onDone: () => void }) {
  const qc = useQueryClient();
  const [f, setF] = useState({ name: current?.name ?? "", image: current?.image ?? "", default: current?.default ?? false });
  const save = useMutation({
    ...putAgentImageMutation(),
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: listAgentImagesQueryKey() });
      onDone();
    },
  });
  return (
    <form
      className="grid max-w-[640px] gap-4 border-b border-hairline p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate({ path: { name: f.name }, body: { image: f.image, default: f.default } });
      }}
    >
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="image-name">
          {t("images.name")}
        </label>
        <Input mono id="image-name" value={f.name} disabled={!!current} onChange={(e) => setF({ ...f, name: e.target.value.trim() })} />
      </div>
      <div className="grid gap-1.5">
        <label className="t-label" htmlFor="image-ref">
          {t("images.image")}
        </label>
        <Input
          mono
          id="image-ref"
          aria-describedby="image-ref-hint"
          value={f.image}
          onChange={(e) => setF({ ...f, image: e.target.value.trim() })}
        />
        <span id="image-ref-hint" className="t-hint">
          {t("images.image.hint")}
        </span>
      </div>
      <div className="flex items-start gap-3">
        <Switch id="image-default" checked={f.default} onCheckedChange={(v) => setF({ ...f, default: v })} />
        <label htmlFor="image-default" className="text-ink">
          {t("images.default.label")}
        </label>
      </div>
      <ErrorAlert error={save.error} />
      <div className="flex gap-2">
        <Button type="submit" variant="primary" mutation={save} disabled={f.name === "" || f.image.length < 3}>
          {t("images.save")}
        </Button>
        <Button variant="ghost" onClick={onDone}>
          {t("images.cancel")}
        </Button>
      </div>
    </form>
  );
}
