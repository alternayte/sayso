import { Badge } from "@/components/ui/badge";
import { t } from "@/lib/copy";

export const modes = ["off", "shadow", "comment", "gate"] as const;
export type Mode = (typeof modes)[number];

export function modeLabel(m: string): string {
  switch (m) {
    case "shadow":
      return t("mode.shadow");
    case "comment":
      return t("mode.comment");
    case "gate":
      return t("mode.gate");
    default:
      return t("mode.off");
  }
}

/** ModeBadge shows a repository mode. A mode that runs the agent has a dot. */
export function ModeBadge({ mode }: { mode: string }) {
  return <Badge dot={mode !== "off"}>{modeLabel(mode)}</Badge>;
}
