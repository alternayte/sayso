import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { CenteredPage } from "@/app/brand";
import { getSetupStateOptions } from "@/client/@tanstack/react-query.gen";
import { createAppManifest, createSetupSession } from "@/client/sdk.gen";
import { Button, ErrorAlert } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { isStatus } from "@/lib/api";
import { t } from "@/lib/copy";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardHeader } from "@/components/ui/card";

type Search = { error?: string };

export const Route = createFileRoute("/setup")({
  validateSearch: (s: Record<string, unknown>): Search => (typeof s.error === "string" ? { error: s.error } : {}),
  component: Setup,
});

function Setup() {
  const state = useQuery(getSetupStateOptions());
  if (state.isPending) {
    return (
      <CenteredPage>
        <Skeleton className="h-40 w-full" />
      </CenteredPage>
    );
  }
  if (state.error) {
    return (
      <CenteredPage>
        <ErrorAlert error={state.error} onRetry={() => state.refetch()} />
      </CenteredPage>
    );
  }
  return <CenteredPage>{state.data.setup_session ? <Checklist /> : <TokenForm />}</CenteredPage>;
}

function TokenForm() {
  const [token, setToken] = useState("");
  const qc = useQueryClient();
  const submit = useMutation({
    mutationFn: () => createSetupSession({ body: { token } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: getSetupStateOptions().queryKey }),
  });
  return (
    <form
      className="card p-6"
      onSubmit={(e) => {
        e.preventDefault();
        submit.mutate();
      }}
    >
      <h1 className="t-title">{t("setup.title")}</h1>
      <p className="mt-2">{t("setup.token.body")}</p>
      <Label className="mt-5 block" htmlFor="setup-token">
        {t("setup.token.label")}
      </Label>
      <Input
        mono
        id="setup-token"
        className="mt-1.5"
        type="password"
        autoComplete="off"
        value={token}
        aria-invalid={isStatus(submit.error, 401) || undefined}
        onChange={(e) => setToken(e.target.value)}
      />
      {isStatus(submit.error, 401) ? (
        <p role="alert" className="mt-2 text-[13px] text-danger">
          {t("setup.token.wrong")}
        </p>
      ) : (
        <ErrorAlert error={submit.error} className="mt-3" />
      )}
      <Button type="submit" variant="primary" className="mt-5 w-full" mutation={submit} disabled={token === ""}>
        {t("setup.token.submit")}
      </Button>
    </form>
  );
}

/** postManifest sends the browser to GitHub with the App manifest, as the manifest flow needs a form post. */
function postManifest(actionURL: string, manifest: string) {
  const form = document.createElement("form");
  form.method = "post";
  form.action = actionURL;
  const field = document.createElement("input");
  field.type = "hidden";
  field.name = "manifest";
  field.value = manifest;
  form.append(field);
  document.body.append(form);
  form.submit();
}

function Checklist() {
  const { error } = Route.useSearch();
  const [org, setOrg] = useState("");
  const create = useMutation({
    mutationFn: async () => (await createAppManifest({ body: { org }, throwOnError: true })).data,
    onSuccess: (form) => postManifest(form.action_url, form.manifest),
  });
  return (
    <Card>
      <CardHeader>
        <h1 className="t-section">{t("setup.checklist")}</h1>
      </CardHeader>
      <ol className="p-4">
        <li className="flex items-start gap-3" aria-current="step">
          <span className="step-marker" data-state="current">
            1
          </span>
          <div className="min-w-0 flex-1">
            <p className="text-ink">{t("setup.step.app")}</p>
            <p className="t-hint">{t("setup.step.app.hint")}</p>
            <form
              className="mt-4"
              onSubmit={(e) => {
                e.preventDefault();
                create.mutate();
              }}
            >
              <Label htmlFor="setup-org">{t("setup.app.org")}</Label>
              <Input
                mono
                id="setup-org"
                className="mt-1.5"
                autoComplete="off"
                value={org}
                onChange={(e) => setOrg(e.target.value.trim())}
              />
              <p className="t-hint mt-1.5">{t("setup.app.org.hint")}</p>
              {error === "bad_state" || error === "github" ? (
                <p role="alert" className="mt-2 text-[13px] text-danger">
                  {t(error === "bad_state" ? "setup.error.bad_state" : "setup.error.github")}
                </p>
              ) : null}
              <ErrorAlert error={create.error} className="mt-3" />
              <Button type="submit" variant="primary" className="mt-4" mutation={create} disabled={org === ""}>
                {t("setup.app.create")}
              </Button>
            </form>
          </div>
        </li>
      </ol>
    </Card>
  );
}
