import { Link } from "@tanstack/react-router";
import type { RunnerTestResult } from "@/client/types.gen";
import { Badge } from "@/components/ui/badge";
import { t } from "@/lib/copy";

/** RunnerTestOutcome shows the outcome of a no-op agent run, with the Skael pull when the test had one. */
export function RunnerTestOutcome({ r }: { r: RunnerTestResult }) {
  const ok = r.status === "done";
  return (
    <div role="status" className="fade-in grid gap-2 text-[13px]">
      <div className="flex flex-wrap items-center gap-3">
        <Badge dot tone={ok ? "success" : "danger"}>
          {ok ? t("runner.test.passed") : t("runner.test.failed")}
        </Badge>
        {r.harness_version ? <span className="t-value text-ink">{r.harness_version}</span> : null}
        <span className="t-hint tabular">{t("runner.test.duration", { s: (r.duration_ms / 1000).toFixed(1) })}</span>
        {r.run_id ? (
          <Link to="/runs/$id" params={{ id: r.run_id }} className="link">
            {t("runner.test.run")}
          </Link>
        ) : null}
      </div>
      {!ok && r.exit_reason ? <p className="t-hint">{r.exit_reason}</p> : null}
      {r.skael ? (
        <div className="flex flex-wrap items-center gap-3">
          <Badge dot tone={r.skael.ok ? "success" : "danger"}>
            {r.skael.ok ? t("skills.skael.test.passed", { set: r.skael.set }) : t("skills.skael.test.failed", { set: r.skael.set })}
          </Badge>
          <span className="t-hint min-w-0 flex-1">{r.skael.detail}</span>
        </div>
      ) : null}
    </div>
  );
}
