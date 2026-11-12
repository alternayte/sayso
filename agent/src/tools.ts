import { execFileSync } from "node:child_process";
import { existsSync, readFileSync, statSync } from "node:fs";
import { join, normalize } from "node:path";
import { StringEnum } from "@mariozechner/pi-ai";
import type {
  ExtensionAPI,
  ExtensionFactory,
} from "@mariozechner/pi-coding-agent";
import { Type } from "typebox";
import type { NewEvent } from "./api.js";
import type {
  Assessment,
  Finding,
  LedgerEntry,
  LedgerUpdate,
  Patch,
  ReplyResult,
} from "./protocol/index.js";
import type { Stage } from "./prompts.js";
import {
  evidenceKinds,
  LIMIT_REACHED,
  severities,
  validateAssessment,
  validateChallenge,
  validateFinding,
  validateReply,
  validateUpdate,
  type FindingInput,
  type HeadFiles,
} from "./validate.js";

/** headFiles reads files of a worktree, inside it only. */
export function headFiles(root: string): HeadFiles {
  return {
    lines(path: string) {
      const p = normalize(path).replace(/^\/+/, "");
      if (p.startsWith("..")) return undefined;
      const full = join(root, p);
      if (!existsSync(full) || !statSync(full).isFile()) return undefined;
      return readFileSync(full, "utf8").replace(/\n$/, "").split("\n");
    },
  };
}

/** Collector keeps what the tools of one run accepted. */
export class Collector {
  findings: Finding[] = [];
  updates: LedgerUpdate[] = [];
  assessment: Assessment | undefined;
  reply: ReplyResult | undefined;
  patch: Patch | undefined;
  /** Results of the challenge stage, by candidate ID. */
  challenge = new Map<
    string,
    { outcome: "confirmed" | "refuted" | "downgraded"; reason: string }
  >();
  limitReached = false;
}

export interface ToolsContext {
  stage: Stage;
  collector: Collector;
  head: HeadFiles;
  headDir: string;
  ledger: LedgerEntry[];
  execution: boolean;
  maxFindings: number;
  /** Candidates of the challenge stage. */
  candidates: Finding[];
  /** Finding of a reply or fix run. */
  findingId?: string;
  emit: (e: NewEvent) => void;
}

const text = (t: string) => ({
  content: [{ type: "text" as const, text: t }],
  details: {},
});

/** failure throws, so pi marks the tool result as an error and the model sees the reason. */
function failure(reason: string): never {
  throw new Error(reason);
}

/** saysoTools returns the extension with the Sayso tools of one stage (§8.6). */
export function saysoTools(ctx: ToolsContext): ExtensionFactory {
  return (pi: ExtensionAPI) => {
    const c = ctx.collector;
    if (ctx.stage === "review") {
      pi.registerTool({
        name: "record_finding",
        label: "Record finding",
        description:
          "Report one issue of the pull request with its location at the head commit and its evidence.",
        parameters: Type.Object({
          severity: StringEnum(severities),
          category: Type.String({
            description:
              "For example correctness, security, data, performance, tests, api",
          }),
          title: Type.String({ maxLength: 100 }),
          body: Type.String({ maxLength: 2000 }),
          path: Type.String({
            description: "Path relative to the repository root",
          }),
          start_line: Type.Integer({ minimum: 1 }),
          end_line: Type.Integer({ minimum: 1 }),
          evidence: Type.Object({
            kind: StringEnum(evidenceKinds),
            detail: Type.String(),
            test_path: Type.Optional(Type.String()),
          }),
          suggestion: Type.Optional(
            Type.String({
              description: "Replacement for the lines, 20 lines or fewer",
            }),
          ),
          same_as: Type.Optional(
            Type.String({
              description:
                "ID of the ledger finding that this repeats, for example S-4",
            }),
          ),
        }),
        async execute(_id, params) {
          const f = params as FindingInput;
          const reason = validateFinding(f, {
            head: ctx.head,
            ledgerIds: new Set(ctx.ledger.map((l) => l.id)),
            execution: ctx.execution,
            accepted: c.findings.length,
            maxFindings: ctx.maxFindings,
          });
          if (reason) {
            if (reason.startsWith(LIMIT_REACHED)) c.limitReached = true;
            failure(reason);
          }
          const finding: Finding = {
            candidate_id: `c${c.findings.length + 1}`,
            severity: f.severity as Finding["severity"],
            category: f.category,
            title: f.title,
            body: f.body,
            path: f.path,
            start_line: f.start_line,
            end_line: f.end_line,
            evidence: {
              kind: f.evidence.kind as Finding["evidence"]["kind"],
              detail: f.evidence.detail,
              test_path: f.evidence.test_path,
            },
            suggestion: f.suggestion || undefined,
            same_as: f.same_as || undefined,
          };
          c.findings.push(finding);
          ctx.emit({
            stage: "review",
            type: "finding",
            name: finding.candidate_id,
            status: finding.severity,
            preview: `${finding.path}:${finding.start_line} ${finding.title}`,
            payload: finding,
          });
          return text(`Recorded ${finding.candidate_id}.`);
        },
      });
      pi.registerTool({
        name: "update_finding",
        label: "Update finding",
        description:
          "Check one open finding of the ledger: still_open, fixed or withdrawn, with a reason and an optional new location.",
        parameters: Type.Object({
          finding_id: Type.String(),
          status: StringEnum(["still_open", "fixed", "withdrawn"] as const),
          reason: Type.String(),
          path: Type.Optional(Type.String()),
          start_line: Type.Optional(Type.Integer({ minimum: 1 })),
          end_line: Type.Optional(Type.Integer({ minimum: 1 })),
        }),
        async execute(_id, u) {
          const open = new Set(
            ctx.ledger.filter((l) => l.status === "open").map((l) => l.id),
          );
          const reason = validateUpdate(u, open, ctx.head);
          if (reason) failure(reason);
          c.updates = c.updates.filter((x) => x.finding_id !== u.finding_id);
          c.updates.push(u as LedgerUpdate);
          return text(`Updated ${u.finding_id}.`);
        },
      });
      pi.registerTool({
        name: "set_assessment",
        label: "Set assessment",
        description:
          "The summary of the review: TL;DR of 3 sentences or fewer, risk, where people should look, impact outside the diff.",
        parameters: Type.Object({
          tldr: Type.String(),
          risk: StringEnum(["low", "medium", "high", "critical"] as const),
          risk_reasons: Type.Array(Type.String()),
          where_to_look: Type.Array(
            Type.Object({ path_or_area: Type.String(), why: Type.String() }),
            { maxItems: 6 },
          ),
          impact: Type.Array(
            Type.Object({ area: Type.String(), why: Type.String() }),
            { maxItems: 6 },
          ),
        }),
        async execute(_id, a) {
          const reason = validateAssessment(a);
          if (reason) failure(reason);
          c.assessment = {
            tldr: a.tldr,
            risk: a.risk,
            risk_reasons: a.risk_reasons,
            where_to_look: a.where_to_look.map((w) => ({
              target: w.path_or_area,
              why: w.why,
            })),
            impact: a.impact,
          };
          return text("Assessment set.");
        },
      });
    }
    if (ctx.stage === "challenge") {
      pi.registerTool({
        name: "challenge_result",
        label: "Challenge result",
        description:
          "The result for one candidate finding: confirmed, refuted or downgraded, with the reason.",
        parameters: Type.Object({
          candidate_id: Type.String(),
          outcome: StringEnum(["confirmed", "refuted", "downgraded"] as const),
          reason: Type.String(),
          evidence: Type.Optional(Type.String()),
        }),
        async execute(_id, r) {
          const pending = new Set(
            ctx.candidates
              .map((x) => x.candidate_id)
              .filter((id) => !c.challenge.has(id)),
          );
          const reason = validateChallenge(r, pending);
          if (reason) failure(reason);
          c.challenge.set(r.candidate_id, {
            outcome: r.outcome,
            reason: r.reason,
          });
          ctx.emit({
            stage: "challenge",
            type: "note",
            name: "challenge_result",
            status: r.outcome,
            preview: `${r.candidate_id}: ${r.reason}`,
          });
          return text(`Recorded ${r.outcome} for ${r.candidate_id}.`);
        },
      });
    }
    if (ctx.stage === "reply") {
      pi.registerTool({
        name: "reply",
        label: "Reply",
        description:
          "Answer the person in the thread: concede, hold or clarify.",
        parameters: Type.Object({
          text: Type.String({ maxLength: 1500 }),
          action: StringEnum(["concede", "hold", "clarify"] as const),
        }),
        async execute(_id, r) {
          if (c.reply) failure("reply was called already");
          const reason = validateReply(r);
          if (reason) failure(reason);
          c.reply = {
            finding_id: ctx.findingId ?? "",
            text: r.text,
            action: r.action,
          };
          return text("Reply recorded.");
        },
      });
    }
    if (ctx.stage === "fix") {
      pi.registerTool({
        name: "propose_patch",
        label: "Propose patch",
        description:
          "One unified diff against the head commit that fixes the finding, with a title and a description.",
        parameters: Type.Object({
          diff: Type.String(),
          title: Type.String({ maxLength: 100 }),
          description: Type.String(),
        }),
        async execute(_id, p) {
          if (c.patch) failure("propose_patch was called already");
          try {
            // The runtime runs this check, not the agent, so it works with execution off.
            execFileSync("git", ["apply", "--check", "-"], {
              cwd: ctx.headDir,
              input: p.diff,
              stdio: ["pipe", "pipe", "pipe"],
            });
          } catch (e) {
            failure(
              `git apply --check failed: ${(e as { stderr?: Buffer }).stderr?.toString().trim() ?? String(e)}`,
            );
          }
          c.patch = {
            finding_id: ctx.findingId ?? "",
            title: p.title,
            description: p.description,
            diff: p.diff,
          };
          return text("Patch recorded.");
        },
      });
    }
  };
}

/** required lists the tool calls a stage must make before it ends (§8.6). */
export function missingOutput(
  stage: Stage,
  c: Collector,
  candidates: Finding[],
): string | undefined {
  switch (stage) {
    case "review":
      return c.assessment ? undefined : "set_assessment was not called";
    case "challenge": {
      const missing = candidates
        .filter((x) => !c.challenge.has(x.candidate_id))
        .map((x) => x.candidate_id);
      return missing.length
        ? `challenge_result is missing for ${missing.join(", ")}`
        : undefined;
    }
    case "reply":
      return c.reply ? undefined : "reply was not called";
    case "fix":
      return c.patch ? undefined : "propose_patch was not called";
  }
}
