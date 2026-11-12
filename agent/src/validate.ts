/**
 * Validation of the Sayso tool inputs (§8.6). Each function returns the
 * reason for the model, or undefined when the input is valid. Guidance
 * cannot change these rules (SI-19).
 */

export const severities = ["blocker", "major", "minor", "nit"] as const;
export type Severity = (typeof severities)[number];
export const evidenceKinds = ["test", "trace", "call_path", "reasoning"] as const;

export interface FindingInput {
  severity: string;
  category: string;
  title: string;
  body: string;
  path: string;
  start_line: number;
  end_line: number;
  evidence: { kind: string; detail: string; test_path?: string };
  suggestion?: string;
  same_as?: string;
}

/** HeadFiles reads files of /work/head for validation. */
export interface HeadFiles {
  /** lines returns the lines of a file at the head commit, or undefined when it does not exist. */
  lines(path: string): string[] | undefined;
}

export interface FindingContext {
  head: HeadFiles;
  /** IDs of the ledger findings (S-<n>). */
  ledgerIds: Set<string>;
  /** False when code execution is off (SI-21). */
  execution: boolean;
  accepted: number;
  maxFindings: number;
}

export const limits = { title: 100, body: 2000, suggestionLines: 20, reply: 1500 };

/** LIMIT_REACHED is the reason when a run has reported the most findings it may. */
export const LIMIT_REACHED = "limit_reached";

export function validateFinding(f: FindingInput, ctx: FindingContext): string | undefined {
  if (ctx.accepted >= ctx.maxFindings) return `${LIMIT_REACHED}: this run already reported ${ctx.maxFindings} findings`;
  if (!(severities as readonly string[]).includes(f.severity)) return `severity must be one of ${severities.join(", ")}`;
  if (!f.category.trim()) return "category is required";
  if (!f.title.trim()) return "title is required";
  if (f.title.length > limits.title) return `title has ${f.title.length} characters; the limit is ${limits.title}`;
  if (f.body.length > limits.body) return `body has ${f.body.length} characters; the limit is ${limits.body}`;
  if (!(evidenceKinds as readonly string[]).includes(f.evidence.kind)) return `evidence kind must be one of ${evidenceKinds.join(", ")}`;
  if (f.evidence.kind === "test" && !ctx.execution) return "evidence kind test is refused: code execution is off for this repository";
  if (f.evidence.kind === "test" && !f.evidence.test_path) return "evidence kind test needs test_path";
  if (!f.evidence.detail.trim()) return "evidence detail is required";
  const lines = ctx.head.lines(f.path);
  if (!lines) return `path ${f.path} does not exist at the head commit`;
  if (!Number.isInteger(f.start_line) || !Number.isInteger(f.end_line) || f.start_line < 1 || f.end_line < f.start_line || f.end_line > lines.length) {
    return `lines ${f.start_line}-${f.end_line} are not in range: ${f.path} has ${lines.length} lines`;
  }
  if (f.suggestion !== undefined && f.suggestion !== "") {
    const s = f.suggestion.replace(/\n$/, "").split("\n");
    if (s.length > limits.suggestionLines) return `suggestion has ${s.length} lines; the limit is ${limits.suggestionLines}`;
    const current = lines.slice(f.start_line - 1, f.end_line).join("\n");
    if (s.join("\n") === current) return "suggestion does not change the lines it replaces";
  }
  if (f.same_as && !ctx.ledgerIds.has(f.same_as)) return `same_as ${f.same_as} is not a finding of the ledger`;
  return undefined;
}

export interface UpdateInput {
  finding_id: string;
  status: string;
  reason: string;
  path?: string;
  start_line?: number;
  end_line?: number;
}

export function validateUpdate(u: UpdateInput, open: Set<string>, head: HeadFiles): string | undefined {
  if (!open.has(u.finding_id)) return `${u.finding_id} is not an open finding of the ledger`;
  if (!["still_open", "fixed", "withdrawn"].includes(u.status)) return "status must be still_open, fixed or withdrawn";
  if (!u.reason.trim()) return "reason is required";
  if (u.path !== undefined) {
    const lines = head.lines(u.path);
    if (!lines) return `path ${u.path} does not exist at the head commit`;
    if (u.start_line !== undefined && (u.start_line < 1 || (u.end_line ?? u.start_line) > lines.length)) return "the new location is not in range";
  }
  return undefined;
}

export interface AssessmentInput {
  tldr: string;
  risk: string;
  risk_reasons: string[];
  where_to_look: { path_or_area: string; why: string }[];
  impact: { area: string; why: string }[];
}

export function validateAssessment(a: AssessmentInput): string | undefined {
  const sentences = a.tldr.split(/(?<=[.!?])\s+/).filter((s) => s.trim() !== "");
  if (sentences.length === 0) return "tldr is required";
  if (sentences.length > 3) return `tldr has ${sentences.length} sentences; the limit is 3`;
  if (!["low", "medium", "high", "critical"].includes(a.risk)) return "risk must be low, medium, high or critical";
  if (a.risk_reasons.length === 0) return "give one or more risk reasons";
  if (a.where_to_look.length > 6) return "where_to_look has more than 6 entries";
  if (a.impact.length > 6) return "impact has more than 6 entries";
  return undefined;
}

export function validateChallenge(c: { candidate_id: string; outcome: string; reason: string }, pending: Set<string>): string | undefined {
  if (!pending.has(c.candidate_id)) return `${c.candidate_id} is not a candidate of this challenge, or it has a result already`;
  if (!["confirmed", "refuted", "downgraded"].includes(c.outcome)) return "outcome must be confirmed, refuted or downgraded";
  if (!c.reason.trim()) return "reason is required";
  return undefined;
}

export function validateReply(r: { text: string; action: string }): string | undefined {
  if (!r.text.trim()) return "text is required";
  if (r.text.length > limits.reply) return `text has ${r.text.length} characters; the limit is ${limits.reply}`;
  if (!["concede", "hold", "clarify"].includes(r.action)) return "action must be concede, hold or clarify";
  return undefined;
}

const order: Record<Severity, number> = { blocker: 0, major: 1, minor: 2, nit: 3 };

/** atOrAbove reports whether a severity is at or above the challenge threshold. */
export function atOrAbove(sev: string, threshold: string): boolean {
  return order[sev as Severity] <= order[threshold as Severity];
}

/** downgrade drops one severity level. A downgraded nit is withdrawn. */
export function downgrade(sev: string): Severity | "withdrawn" {
  const i = order[sev as Severity];
  return i >= 3 ? "withdrawn" : severities[i + 1]!;
}
