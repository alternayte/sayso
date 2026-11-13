import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Check } from "lucide-react";
import { getSetupChecklistOptions } from "@/client/@tanstack/react-query.gen";
import type { Step } from "@/client/types.gen";
import { ErrorAlert, PageHead } from "@/components/app-ui";
import { Card, CardHeader } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { t } from "@/lib/copy";

/**
 * SetupChecklist is the home page of an admin until each step is done (§16.3).
 * Each step shows its state as text and a marker, not by colour only.
 */
export function SetupChecklist({ steps }: { steps: Step[] }) {
  const current = steps.findIndex((s) => !s.done);
  const done = steps.filter((s) => s.done).length;
  return (
    <Card>
      <CardHeader>
        <h2 className="t-section">{t("setup.checklist")}</h2>
        <span className="t-hint tabular">{t("checklist.progress", { done, total: steps.length })}</span>
      </CardHeader>
      <ol aria-label={t("setup.checklist")} className="grid gap-4 p-4">
        {steps.map((s, i) => {
          const state = s.done ? "done" : i === current ? "current" : "todo";
          return (
            <li key={s.key} className="flex items-start gap-3" aria-current={state === "current" ? "step" : undefined} data-state={state}>
              <span className="step-marker" data-state={state} aria-hidden="true">
                {s.done ? <Check className="h-3 w-3" /> : i + 1}
              </span>
              <div className="min-w-0 flex-1">
                <p className="text-ink">
                  <Link to={s.link} className="link">
                    {t(`checklist.${s.key}`)}
                  </Link>
                </p>
                <p className="t-hint">{s.done ? t("checklist.done") : t(`checklist.${s.key}.hint`)}</p>
              </div>
            </li>
          );
        })}
      </ol>
    </Card>
  );
}

/** useChecklist reads the checklist for admins only; other roles never see it. */
export function useChecklist(admin: boolean) {
  return useQuery({ ...getSetupChecklistOptions(), enabled: admin });
}

export function ChecklistGate({ admin, children }: { admin: boolean; children: React.ReactNode }) {
  const q = useChecklist(admin);
  if (!admin) return <>{children}</>;
  if (q.isPending) return <Skeleton className="h-60 w-full" />;
  if (q.error) return <ErrorAlert error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data.complete)
    return (
      <>
        <PageHead title={t("setup.title")} hint={t("checklist.hint")} />
        <SetupChecklist steps={q.data.steps} />
      </>
    );
  return <>{children}</>;
}
