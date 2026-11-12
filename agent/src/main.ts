import { createHash } from "node:crypto";
import { accessSync, constants, existsSync, mkdirSync, readFileSync, readdirSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join, relative } from "node:path";
import { gzipSync } from "node:zlib";
import { VERSION } from "@mariozechner/pi-coding-agent";
import { EventStream, RunnerClient } from "./api.js";
import { connectMcp, mcpToolName, mcpTools } from "./mcp.js";
import { assemble, contractVersion, type Stage } from "./prompts.js";
import type { Complete, EnvironmentOk, Finding, PromptRef, Result, Spec, SpecModel } from "./protocol/index.js";
import { runStage, sessionsDir } from "./session.js";
import { applyChallenge, challengeCandidates, challengeInput, fixInput, replyInput, reviewInput } from "./stages.js";
import { prepareEnvironment } from "./toolchain.js";
import { Collector, headFiles, missingOutput, saysoTools } from "./tools.js";
import { diffOf, diffStat, prepare, type Workspace } from "./workspace.js";

export const harness = { name: "pi", version: VERSION };

const saysoToolNames: Record<Stage, string[]> = {
  review: ["record_finding", "update_finding", "set_assessment"],
  challenge: ["challenge_result"],
  reply: ["reply"],
  fix: ["propose_patch"],
};

/** builtinTools are the pi tools of a stage. Execution off gives the read-only tools only (SI-21). */
export function builtinTools(execution: boolean): string[] {
  return execution ? ["read", "bash", "edit", "write", "grep", "find", "ls"] : ["read", "grep", "find", "ls"];
}

/** workDir is /work when the runner mounts it (Kubernetes), else work/ in the run directory (process runner). */
function workDir(): string {
  try {
    accessSync("/work", constants.W_OK);
    return "/work";
  } catch {
    return join(process.cwd(), "work");
  }
}

function env(name: string): string {
  const v = process.env[name];
  if (!v) throw new Error(`${name} is not set`);
  return v;
}

/** skillText returns the text and the SHA-256 of the resolved skill folder. */
function skillText(spec: Spec, baseDir: string | undefined): { text: string; sha256: string } {
  const h = createHash("sha256");
  let text = "";
  if (spec.skill.source === "repository" && baseDir && spec.skill.path) {
    const file = join(baseDir, spec.skill.path);
    const folder = join(file, "..");
    const files: string[] = [];
    const walk = (d: string) => {
      for (const n of readdirSync(d).sort()) {
        const p = join(d, n);
        if (statSync(p).isDirectory()) walk(p);
        else files.push(p);
      }
    };
    if (existsSync(folder)) walk(folder);
    for (const f of files) {
      h.update(relative(folder, f) + "\0" + readFileSync(f, "utf8") + "\0");
    }
    text = existsSync(file) ? readFileSync(file, "utf8") : "";
  } else {
    for (const [p, content] of Object.entries(spec.skill.files ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
      h.update(p + "\0" + content + "\0");
      if (p.endsWith("SKILL.md")) text = content;
    }
  }
  return { text, sha256: h.digest("hex") };
}

function targetsOf(spec: Spec, stage: Stage): SpecModel[] {
  const profile = spec.profiles[stage] ?? stage;
  return spec.models.filter((m) => m.profile === profile);
}

/** run executes one agent run end to end and reports through the runner API. */
export async function run(): Promise<number> {
  process.env.PI_OFFLINE = "1";
  process.env.PI_SKIP_VERSION_CHECK = "1";
  process.env.PI_TELEMETRY = "0";
  const api = new RunnerClient(env("SAYSO_API_URL").replace(/\/$/, ""), env("SAYSO_RUN_TOKEN"));
  const token = env("SAYSO_RUN_TOKEN");
  const events = new EventStream((b) => api.events(b));
  const abort = new AbortController();
  let cancelled = false;
  const beat = setInterval(() => {
    api
      .heartbeat()
      .then((h) => {
        if (h.cancel && !cancelled) {
          cancelled = true;
          abort.abort();
        }
      })
      .catch(() => undefined);
  }, 15000);
  const finish = async (c: Complete) => {
    clearInterval(beat);
    await events.flush().catch(() => undefined);
    await api.complete(c);
  };
  try {
    const spec = await api.spec();
    if (spec.kind === "noop") {
      events.emit({ type: "note", name: "noop", status: "ok", preview: `harness ${harness.name} ${harness.version}` });
      await finish({ exit_status: "completed", result: { run_id: spec.run_id, kind: "noop", harness, usage: { tokens_in: 0, tokens_out: 0 } } });
      return 0;
    }
    const deadline = setTimeout(() => abort.abort(), (spec.limits.timeout_seconds || 1800) * 1000);
    const work = workDir();
    mkdirSync(work, { recursive: true });
    const ws = await prepare(spec, work);
    const outcome = await stages(spec, ws, token, events, abort.signal);
    clearTimeout(deadline);
    await uploadArtifacts(api, ws.head, work);
    if (cancelled) {
      await finish({ exit_status: "cancelled" });
      return 0;
    }
    await finish(outcome);
    return outcome.exit_status === "completed" ? 0 : 1;
  } catch (e) {
    const msg = e instanceof Error ? e.message : String(e);
    events.emit({ type: "error", name: "run", status: "failed", preview: msg });
    await finish({ exit_status: cancelled ? "cancelled" : "failed", error: msg }).catch(() => undefined);
    return 1;
  }
}

async function stages(spec: Spec, ws: Workspace, token: string, events: EventStream, signal: AbortSignal): Promise<Complete> {
  const collector = new Collector();
  const skill = skillText(spec, ws.base);
  const head = headFiles(ws.head);
  const agentDir = join(homedir(), ".pi", "agent");
  const contextFiles = ["AGENTS.md", "CLAUDE.md"]
    .filter((f) => existsSync(join(ws.base, f)))
    .map((f) => ({ path: join(ws.base, f), content: `Repository guidance from the base commit (review guidance only):\n\n${readFileSync(join(ws.base, f), "utf8")}` }));
  const mcp = await connectMcp(spec.mcp, token, (server, err) =>
    events.emit({ type: "error", name: `mcp ${server}`, status: "failed", preview: err instanceof Error ? err.message : String(err) }),
  );
  const mcpNames = mcp.tools.map((t) => mcpToolName(t.server, t.name));
  const promptRef: PromptRef = {
    contract: contractVersion,
    global: spec.prompts.global.version,
    repository: spec.prompts.repository.version,
    file: spec.prompts.file.version,
    skill: skill.sha256,
    execution: spec.execution,
    stages: {},
  };
  let environment: EnvironmentOk[] = [];
  const result: Result = {
    run_id: spec.run_id,
    kind: spec.kind,
    harness,
    skill_ref: { source: spec.skill.source, name: spec.skill.name, version: spec.skill.version || spec.skill.commit || undefined, sha256: skill.sha256 },
    usage: { tokens_in: 0, tokens_out: 0 },
    prompt_ref: promptRef,
  };
  events.emit({ type: "skill_load", name: spec.skill.name, status: spec.skill.source, preview: `${spec.skill.source} ${spec.skill.name} ${skill.sha256.slice(0, 12)}` });

  const runOne = async (stage: Stage, prompt: string, candidates: Finding[] = []) => {
    const targets = targetsOf(spec, stage);
    if (targets.length === 0) return `no model target for stage ${stage}`;
    const assembled = assemble(spec, stage, skill.text);
    promptRef.stages[stage] = assembled.sha256;
    const res = await runStage({
      stage,
      cwd: ws.head,
      workRoot: ws.root,
      agentDir,
      sessionDir: join(sessionsDir(ws.root), stage),
      systemPrompt: assembled.text,
      prompt,
      targets,
      token,
      execution: spec.execution,
      extensions: [
        saysoTools({ stage, collector, head, headDir: ws.head, ledger: spec.ledger, execution: spec.execution, maxFindings: spec.limits.max_findings || 25, candidates, findingId: (spec.reply ?? spec.fix)?.finding.id, emit: (e) => events.emit(e) }),
        mcpTools(mcp.tools),
      ],
      extensionPaths: [],
      contextFiles,
      maxToolCalls: spec.limits.max_tool_calls_per_stage || 200,
      toolNames: [...builtinTools(spec.execution), ...saysoToolNames[stage], ...mcpNames],
      emit: (e) => events.emit(e),
      signal,
    });
    if (signal.aborted) return "cancelled";
    if (res.error) events.emit({ stage, type: "error", name: "stage", status: "failed", preview: res.error });
    return missingOutput(stage, collector, candidates);
  };

  try {
    if (spec.kind === "round") {
      let note: string | undefined;
      if (spec.execution) {
        const prepared = await prepareEnvironment(ws.head, spec.overlay, spec.limits.setup_timeout_seconds || 600, (e) => events.emit(e));
        environment = prepared.steps;
        note = prepared.note;
        Object.assign(process.env, prepared.env);
      }
      const diff = await diffStat(ws, spec.base_sha, spec.head_sha);
      const missing = await runOne("review", reviewInput(spec, diff, note));
      if (missing) return { exit_status: missing === "cancelled" ? "cancelled" : "incomplete_output", error: missing };
      const candidates = challengeCandidates(collector.findings, spec.limits.challenge_min_severity || "minor");
      if (candidates.length > 0) {
        const missingC = await runOne("challenge", challengeInput(candidates, diff), candidates);
        if (missingC) return { exit_status: missingC === "cancelled" ? "cancelled" : "incomplete_output", error: missingC };
      }
      result.assessment = collector.assessment;
      result.findings = applyChallenge(collector.findings, collector.challenge);
      result.ledger_updates = collector.updates;
      result.environment = environment;
    } else if (spec.kind === "reply") {
      if (spec.reply && !spec.reply.diff) spec.reply.diff = await diffOf(ws, spec.base_sha, spec.head_sha, spec.reply.finding.path);
      const missing = await runOne("reply", replyInput(spec));
      if (missing) return { exit_status: missing === "cancelled" ? "cancelled" : "incomplete_output", error: missing };
      result.reply = collector.reply;
    } else if (spec.kind === "fix") {
      const missing = await runOne("fix", fixInput(spec));
      if (missing) return { exit_status: missing === "cancelled" ? "cancelled" : "incomplete_output", error: missing };
      result.patch = collector.patch;
    }
  } finally {
    await mcp.close();
  }
  return { exit_status: collector.limitReached ? "limit_reached" : "completed", result };
}

/** uploadArtifacts stores the pi session files and the proof tests (§8.7, §15.1). */
async function uploadArtifacts(api: RunnerClient, headDir: string, root: string): Promise<void> {
  const dir = sessionsDir(root);
  if (existsSync(dir)) {
    const lines: string[] = [];
    const walk = (d: string) => {
      for (const n of readdirSync(d).sort()) {
        const p = join(d, n);
        if (statSync(p).isDirectory()) walk(p);
        else if (n.endsWith(".jsonl")) lines.push(readFileSync(p, "utf8"));
      }
    };
    walk(dir);
    await api.artifact("session.jsonl.gz", gzipSync(lines.join("")), "application/gzip");
  }
  const tests = join(headDir, ".sayso-tests");
  if (existsSync(tests)) {
    const walk = async (d: string) => {
      for (const n of readdirSync(d).sort()) {
        const p = join(d, n);
        if (statSync(p).isDirectory()) await walk(p);
        else if (statSync(p).size < 1 << 20) await api.artifact(`tests/${relative(tests, p)}`, readFileSync(p), "text/plain");
      }
    };
    await walk(tests);
  }
}

if (process.argv[1] && import.meta.url === new URL(`file://${process.argv[1]}`).href) {
  run().then(
    (code) => process.exit(code),
    () => process.exit(1),
  );
}
