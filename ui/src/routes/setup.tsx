import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import { CenteredPage } from "@/app/brand";
import { getSetupStateOptions } from "@/client/@tanstack/react-query.gen";
import { createSetupSession } from "@/client/sdk.gen";
import { Button, ErrorAlert } from "@/components/app-ui";
import { Skeleton } from "@/components/ui/skeleton";
import { isStatus } from "@/lib/api";
import { t } from "@/lib/copy";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Card, CardHeader } from "@/components/ui/card";

export const Route = createFileRoute("/setup")({ component: Setup });

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

function Checklist() {
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
          <div>
            <p className="text-ink">{t("setup.step.app")}</p>
            <p className="t-hint">{t("setup.step.app.hint")}</p>
          </div>
        </li>
      </ol>
    </Card>
  );
}
