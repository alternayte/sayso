import { type QueryKey, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { previewPromptOptions } from "@/client/@tanstack/react-query.gen";
import type { GuidanceBody, Version } from "@/client/types.gen";
import { Button, ErrorAlert, Segmented } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";

/**
 * GuidanceEditor edits one guidance scope. A save adds the new version to the
 * history at once; on error the history rolls back, the editor shakes and the
 * error shows inline (§16.5).
 */
export function GuidanceEditor({
  id,
  limit,
  queryKey,
  data,
  save,
  canEdit,
}: {
  id: string;
  limit: number;
  queryKey: QueryKey;
  data: GuidanceBody;
  save: (text: string) => Promise<GuidanceBody>;
  canEdit: boolean;
}) {
  const qc = useQueryClient();
  const [text, setText] = useState(data.current?.text ?? "");
  const [shake, setShake] = useState(0);
  const [open, setOpen] = useState<number | null>(null);
  const mutation = useMutation({
    mutationFn: save,
    onMutate: async (value: string) => {
      await qc.cancelQueries({ queryKey });
      const before = qc.getQueryData<GuidanceBody>(queryKey);
      if (before) {
        const next: Version = {
          version: (before.current?.version ?? 0) + 1,
          text: value,
          author: t("guidance.you"),
          created_at: new Date().toISOString(),
        };
        qc.setQueryData<GuidanceBody>(queryKey, { current: next, history: [next, ...before.history] });
      }
      return { before };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.before) qc.setQueryData(queryKey, ctx.before);
      setShake((n) => n + 1);
    },
    onSettled: () => void qc.invalidateQueries({ queryKey }),
  });
  const count = [...text].length;
  const changed = text !== (data.current?.text ?? "");
  return (
    <div className="grid gap-4">
      <form
        key={shake}
        className={`grid gap-1.5 ${shake > 0 ? "shake" : ""}`}
        onSubmit={(e) => {
          e.preventDefault();
          mutation.mutate(text);
        }}
      >
        <label className="t-label" htmlFor={id}>
          {t("guidance.text")}
        </label>
        <textarea
          id={id}
          aria-describedby={`${id}-hint`}
          className="control control-mono min-h-40"
          value={text}
          readOnly={!canEdit}
          onChange={(e) => setText(e.target.value)}
        />
        <div className="flex flex-wrap items-center gap-3">
          <span id={`${id}-hint`} className={`t-hint tabular ${count > limit ? "text-danger" : ""}`}>
            {t("guidance.count", { n: count.toLocaleString(), limit: limit.toLocaleString() })}
          </span>
          <span className="flex-1" />
          {canEdit ? (
            <Button type="submit" variant="primary" size="sm" mutation={mutation} disabled={!changed || count > limit}>
              {t("guidance.save")}
            </Button>
          ) : null}
        </div>
        <ErrorAlert error={mutation.error} />
      </form>
      <div className="grid gap-1.5">
        <span className="t-label">{t("guidance.history")}</span>
        {data.history.length === 0 ? (
          <p className="t-hint">{t("guidance.history.empty")}</p>
        ) : (
          <ol aria-label={t("guidance.history")} className="divide-y divide-hairline rounded-md border border-hairline">
            {data.history.map((v) => (
              <li key={v.version} className="grid gap-2 px-3 py-2">
                <button
                  type="button"
                  aria-expanded={open === v.version}
                  className="press-row flex w-full items-center gap-3 text-left text-[13px]"
                  onClick={() => setOpen(open === v.version ? null : v.version)}
                >
                  <span className="t-value tabular text-ink">v{v.version}</span>
                  {v.version === data.current?.version ? (
                    <Badge dot tone="accent">
                      {t("guidance.active")}
                    </Badge>
                  ) : null}
                  <span className="t-hint min-w-0 flex-1 truncate">{v.author}</span>
                  <span className="t-hint tabular">{new Date(v.created_at).toLocaleString()}</span>
                </button>
                {open === v.version ? (
                  <pre className="control control-mono fade-in max-h-60 overflow-auto whitespace-pre-wrap">
                    {v.text || t("guidance.empty")}
                  </pre>
                ) : null}
              </li>
            ))}
          </ol>
        )}
      </div>
    </div>
  );
}

const stages = ["review", "challenge", "reply", "fix"] as const;
type Stage = (typeof stages)[number];

/** PromptPreview shows the assembled prompt of one repository and stage, read-only (REQ-CFG-011). */
export function PromptPreview({ repository }: { repository: string }) {
  const [stage, setStage] = useState<Stage>("review");
  const [owner = "", name = ""] = repository.split("/");
  const q = useQuery({
    ...previewPromptOptions({ path: { owner, name }, query: { stage } }),
    enabled: repository !== "",
    placeholderData: (p) => p,
  });
  return (
    <div className="grid gap-3">
      <Segmented
        label={t("guidance.preview.stage")}
        options={stages.map((s) => ({ value: s, label: s }))}
        value={stage}
        onChange={setStage}
      />
      {repository === "" ? (
        <p className="t-hint">{t("guidance.preview.pick")}</p>
      ) : q.isPending ? (
        <Skeleton className="h-40 w-full" />
      ) : q.error ? (
        <ErrorAlert error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <div className={`grid gap-3 ${q.isFetching ? "progress-line" : ""}`}>
          <p className="t-hint tabular">
            {t("guidance.preview.versions", {
              global: q.data.versions.global || "–",
              repository: q.data.versions.repository || "–",
              sha: q.data.prompt.sha256.slice(0, 12),
            })}
          </p>
          <ol aria-label={t("guidance.preview.layers")} className="grid gap-3">
            {q.data.prompt.layers.map((l) => (
              <li key={l.name} className="grid gap-1.5">
                <Badge tone={l.name === "contract" ? "accent" : undefined}>{t(`guidance.layer.${l.name}`)}</Badge>
                <pre tabIndex={0} className="control control-mono max-h-72 overflow-auto whitespace-pre-wrap">
                  {l.text}
                </pre>
              </li>
            ))}
          </ol>
        </div>
      )}
    </div>
  );
}
