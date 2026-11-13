import { useMutation } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { applyConfigMutation, planConfigMutation } from "@/client/@tanstack/react-query.gen";
import { exportConfig } from "@/client/sdk.gen";
import type { ConfigChange } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { Badge } from "@/components/ui/badge";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/config")({ beforeLoad: requireRole("admin"), component: Config });

function Config() {
  return (
    <>
      <PageHead title={t("config.title")} hint={t("config.hint")} />
      <div className="grid gap-5">
        <ExportCard />
        <ImportCard />
      </div>
    </>
  );
}

function ExportCard() {
  const exp = useMutation({
    mutationFn: async () => (await exportConfig({ throwOnError: true })).data,
    onSuccess: (d) => {
      const url = URL.createObjectURL(new Blob([d.yaml], { type: "application/yaml" }));
      const a = document.createElement("a");
      a.href = url;
      a.download = "sayso-config.yaml";
      a.click();
      URL.revokeObjectURL(url);
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("config.export")}</CardTitle>
      </CardHeader>
      <div className="grid gap-3 p-4">
        <p className="t-hint max-w-[640px]">{t("config.export.hint")}</p>
        <div>
          <Button size="sm" mutation={exp} onClick={() => exp.mutate()}>
            {t("config.export.download")}
          </Button>
        </div>
        <ErrorAlert error={exp.error} />
      </div>
    </Card>
  );
}

const opTone = { add: "success", change: "warning", remove: "danger" } as const;

function ImportCard() {
  const [yaml, setYaml] = useState("");
  const plan = useMutation({ ...planConfigMutation() });
  const apply = useMutation({ ...applyConfigMutation() });
  const changes = plan.data?.changes;
  const reset = () => {
    plan.reset();
    apply.reset();
  };
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("config.import")}</CardTitle>
      </CardHeader>
      <div className="grid gap-4 p-4">
        <p className="t-hint max-w-[640px]">{t("config.import.hint")}</p>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="config-file">
            {t("config.import.file")}
          </label>
          <input
            id="config-file"
            type="file"
            accept=".yaml,.yml,application/yaml,text/yaml"
            className="control"
            onChange={async (e) => {
              const f = e.target.files?.[0];
              if (!f) return;
              setYaml(await f.text());
              reset();
            }}
          />
        </div>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="config-yaml">
            {t("config.import.yaml")}
          </label>
          <textarea
            id="config-yaml"
            className="control control-mono min-h-48"
            value={yaml}
            onChange={(e) => {
              setYaml(e.target.value);
              reset();
            }}
          />
        </div>
        <div>
          <Button size="sm" mutation={plan} disabled={yaml.trim() === ""} onClick={() => plan.mutate({ body: { yaml } })}>
            {t("config.import.diff")}
          </Button>
        </div>
        <ErrorAlert error={plan.error} />
        {changes ? (
          <section aria-label={t("config.diff")} className="fade-in grid gap-3">
            <p role="status" className="t-value text-ink">
              {changes.length === 0 ? t("config.diff.none") : t("config.diff.count", { n: changes.length })}
            </p>
            {changes.length > 0 ? <DiffList changes={changes} /> : null}
            {changes.length > 0 && !apply.isSuccess ? (
              <div className="flex gap-2">
                <Button variant="primary" size="sm" mutation={apply} onClick={() => apply.mutate({ body: { yaml, changes } })}>
                  {t("config.apply")}
                </Button>
                <Button variant="ghost" size="sm" onClick={reset}>
                  {t("config.cancel")}
                </Button>
              </div>
            ) : null}
            {apply.isSuccess ? (
              <Badge dot tone="success">
                {t("config.applied", { n: apply.data.changes.length })}
              </Badge>
            ) : null}
            <ErrorAlert error={apply.error} />
          </section>
        ) : null}
      </div>
    </Card>
  );
}

function DiffList({ changes }: { changes: ConfigChange[] }) {
  return (
    <ul className="divide-y divide-hairline rounded-md border border-hairline">
      {changes.map((c) => (
        <li key={`${c.section}/${c.name}`} className="grid gap-1.5 px-3 py-2 text-[13px]">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={opTone[c.op]}>{t(`config.op.${c.op}`)}</Badge>
            <span className="t-hint">{c.section}</span>
            <span className="t-value text-ink">{c.name}</span>
          </div>
          {c.fields.length > 0 ? (
            <dl className="grid grid-cols-[minmax(8rem,auto)_minmax(0,1fr)] gap-x-3 gap-y-1">
              {c.fields.map((f) => (
                <div key={f.field} className="contents">
                  <dt className="t-hint">{f.field}</dt>
                  <dd className="control-mono min-w-0 break-words whitespace-pre-wrap">
                    {c.op === "change" ? (
                      <>
                        <del className="text-muted">{f.old || "–"}</del> → <ins className="text-ink no-underline">{f.new || "–"}</ins>
                      </>
                    ) : (
                      f.new || f.old
                    )}
                  </dd>
                </div>
              ))}
            </dl>
          ) : null}
        </li>
      ))}
    </ul>
  );
}
