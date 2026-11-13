import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { getSkillsOptions, getSkillsQueryKey, putSkaelMutation } from "@/client/@tanstack/react-query.gen";
import type { SkaelBody } from "@/client/types.gen";
import { Button, ErrorAlert, PageHead } from "@/components/app-ui";
import { SecretField, type SecretValue } from "@/components/secret-field";
import { RunnerTestOutcome } from "@/components/runner-test";
import { testSkael } from "@/client/sdk.gen";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { t } from "@/lib/copy";
import { requireRole } from "@/lib/guard";

export const Route = createFileRoute("/_app/settings/skills")({ beforeLoad: requireRole("admin"), component: Skills });

function Skills() {
  const q = useQuery(getSkillsOptions());
  return (
    <>
      <PageHead title={t("skills.title")} hint={t("skills.hint")} />
      {q.isPending ? (
        <Skeleton className="h-40" />
      ) : q.error ? (
        <ErrorAlert error={q.error} onRetry={() => q.refetch()} />
      ) : (
        <div className="grid gap-6">
          <SkaelForm key={JSON.stringify(q.data.skael)} current={q.data.skael} />
          <Card>
            <CardHeader>
              <CardTitle>{t("skills.builtin")}</CardTitle>
            </CardHeader>
            <div className="grid gap-4 p-4">
              <p className="t-hint max-w-[640px]">{t("skills.builtin.hint")}</p>
              {Object.entries(q.data.builtin)
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([path, text]) => (
                  <section key={path} className="grid gap-1.5">
                    <h3 className="t-value text-ink">{path}</h3>
                    <pre tabIndex={0} aria-label={path} className="control control-mono max-h-80 overflow-auto whitespace-pre-wrap">
                      {text}
                    </pre>
                  </section>
                ))}
            </div>
          </Card>
        </div>
      )}
    </>
  );
}

function lines(s: string): string[] {
  return s
    .split(/[\n,]/)
    .map((x) => x.trim())
    .filter(Boolean);
}

function SkaelForm({ current }: { current: SkaelBody }) {
  const test = useMutation({ mutationFn: async () => (await testSkael({ throwOnError: true })).data });
  const qc = useQueryClient();
  const [f, setF] = useState({
    url: current.url,
    default_set: current.default_set,
    allowed_sets: current.allowed_sets.join("\n"),
    review_skill: current.review_skill || "pr-review",
    activation_hook: current.activation_hook,
  });
  const [agentKey, setAgentKey] = useState<SecretValue>(undefined);
  const [reporterKey, setReporterKey] = useState<SecretValue>(undefined);
  const save = useMutation({ ...putSkaelMutation(), onSuccess: () => qc.invalidateQueries({ queryKey: getSkillsQueryKey() }) });
  const keyOf = (v: SecretValue) => (v && (v.value || v.env) ? v : undefined);
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("skills.skael")}</CardTitle>
      </CardHeader>
      <form
        className="grid max-w-[640px] gap-4 p-4"
        onSubmit={(e) => {
          e.preventDefault();
          save.mutate({
            body: {
              url: f.url,
              default_set: f.default_set,
              allowed_sets: lines(f.allowed_sets),
              review_skill: f.review_skill,
              activation_hook: f.activation_hook,
              agent_key: keyOf(agentKey),
              reporter_key: keyOf(reporterKey),
            },
          });
        }}
      >
        <p className="t-hint">{t("skills.skael.hint")}</p>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="skael-url">
            {t("skills.skael.url")}
          </label>
          <Input mono id="skael-url" value={f.url} onChange={(e) => setF({ ...f, url: e.target.value.trim() })} />
        </div>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="skael-default-set">
            {t("skills.skael.default_set")}
          </label>
          <Input mono id="skael-default-set" value={f.default_set} onChange={(e) => setF({ ...f, default_set: e.target.value.trim() })} />
        </div>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="skael-allowed-sets">
            {t("skills.skael.allowed_sets")}
          </label>
          <textarea
            id="skael-allowed-sets"
            aria-describedby="skael-allowed-sets-hint"
            className="control control-mono min-h-16"
            value={f.allowed_sets}
            onChange={(e) => setF({ ...f, allowed_sets: e.target.value })}
          />
          <span id="skael-allowed-sets-hint" className="t-hint">
            {t("skills.skael.allowed_sets.hint")}
          </span>
        </div>
        <div className="grid gap-1.5">
          <label className="t-label" htmlFor="skael-review-skill">
            {t("skills.skael.review_skill")}
          </label>
          <Input
            mono
            id="skael-review-skill"
            value={f.review_skill}
            onChange={(e) => setF({ ...f, review_skill: e.target.value.trim() })}
          />
        </div>
        <div className="flex items-start gap-3">
          <Switch
            id="skael-hook"
            checked={f.activation_hook}
            onCheckedChange={(activation_hook) => setF({ ...f, activation_hook })}
            aria-describedby="skael-hook-hint"
          />
          <div className="grid gap-1">
            <label htmlFor="skael-hook" className="text-ink">
              {t("skills.skael.hook")}
            </label>
            <span id="skael-hook-hint" className="t-hint">
              {t("skills.skael.hook.hint")}
            </span>
          </div>
        </div>
        <SecretField label={t("skills.skael.agent_key")} field={current.agent_key} value={agentKey} onChange={setAgentKey} />
        <p className="t-hint -mt-2">{t("skills.skael.agent_key.hint")}</p>
        <SecretField label={t("skills.skael.reporter_key")} field={current.reporter_key} value={reporterKey} onChange={setReporterKey} />
        <p className="t-hint -mt-2">{t("skills.skael.reporter_key.hint")}</p>
        <ErrorAlert error={save.error} />
        <div className="flex items-center gap-3">
          <Button type="submit" variant="primary" mutation={save}>
            {t("skills.skael.save")}
          </Button>
          {save.isSuccess ? <span className="t-hint">{t("skills.skael.saved")}</span> : null}
          <Button mutation={test} disabled={!current.url || !current.default_set} onClick={() => test.mutate()}>
            {t("skills.skael.test")}
          </Button>
        </div>
        {test.data ? <RunnerTestOutcome r={test.data} /> : null}
        <ErrorAlert error={test.error} />
        <p className="t-hint -mt-2">{t("skills.skael.test.hint")}</p>
      </form>
    </Card>
  );
}
