import { Badge } from "@/components/ui/badge";
import { t, type CopyKey } from "@/lib/copy";

const statusTone: Record<string, "success" | "accent" | "warning" | "danger" | undefined> = {
  done: "success",
  running: "accent",
  publishing: "accent",
  queued: "warning",
  failed: "danger",
  budget_exceeded: "danger",
  cancelled: undefined,
};

export function RoundStatus({ status }: { status: string }) {
  return (
    <Badge dot tone={statusTone[status]}>
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
