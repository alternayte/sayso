import { Badge } from "@/components/ui/badge";
import { t, type CopyKey } from "@/lib/copy";
import { useChanged } from "@/motion";

const statusTone: Record<string, "success" | "accent" | "warning" | "danger" | undefined> = {
  done: "success",
  running: "accent",
  publishing: "accent",
  queued: "warning",
  failed: "danger",
  budget_exceeded: "danger",
  cancelled: undefined,
};

/** RoundStatus cross-fades on a change and its dot pulses once; a running dot breathes. */
export function RoundStatus({ status }: { status: string }) {
  const changed = useChanged(status);
  const live = status === "running" || status === "queued" || status === "publishing";
  return (
    <Badge
      key={status}
      dot
      tone={statusTone[status]}
      data-status={status}
      className={`${changed ? "fade-swap ring-pulse" : ""} ${live ? "dot-breathe" : ""}`}
    >
      {t(`round.status.${status}` as CopyKey)}
    </Badge>
  );
}

const verdictTone: Record<string, "success" | "warning" | "danger"> = {
  looks_good: "success",
  changes_suggested: "warning",
  blocking: "danger",
};

export function Verdict({ verdict }: { verdict?: string }) {
  if (!verdict) return <span className="t-hint">—</span>;
  return <Badge tone={verdictTone[verdict]}>{t(`verdict.${verdict}` as CopyKey)}</Badge>;
}

export function sha7(sha: string) {
  return sha.slice(0, 7);
}
