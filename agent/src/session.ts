import { mkdirSync } from "node:fs";
import { join, resolve } from "node:path";
import {
  AuthStorage,
  createAgentSession,
  DefaultResourceLoader,
  isToolCallEventType,
  ModelRegistry,
  SessionManager,
  SettingsManager,
  type AgentSessionEvent,
  type ExtensionFactory,
} from "@mariozechner/pi-coding-agent";
import type { NewEvent } from "./api.js";
import type { SpecModel } from "./protocol/index.js";

export interface StageSession {
  stage: string;
  cwd: string;
  /** Root that writes must stay inside (/work). */
  workRoot: string;
  agentDir: string;
  sessionDir: string;
  systemPrompt: string;
  prompt: string;
  targets: SpecModel[];
  token: string;
  execution: boolean;
  extensions: ExtensionFactory[];
  extensionPaths: string[];
  contextFiles: { path: string; content: string }[];
  maxToolCalls: number;
  toolNames: string[];
  emit: (e: NewEvent) => void;
  signal: AbortSignal;
}

/** Errors that move the session to the next target (§12.2). */
export function retryable(message: string | undefined): boolean {
  if (!message) return false;
  return /\b(429|5\d\d)\b|rate.?limit|overloaded|ECONN|ENOTFOUND|ETIMEDOUT|fetch failed|socket|network|terminated/i.test(
    message,
  );
}

function piModel(t: SpecModel) {
  return {
    id: t.model,
    name: t.model,
    reasoning: !!t.reasoning && t.reasoning !== "off",
    input: ["text"] as ("text" | "image")[],
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
    contextWindow: t.context_window || 128000,
    maxTokens: t.max_tokens || 16384,
    compat: t.compat as never,
  };
}

/**
 * runStage runs one pi session with explicit resources only (§8.5): no
 * project extensions, settings, skills or context files from the head
 * worktree. Models go through the model proxy with the run token.
 */
export async function runStage(
  s: StageSession,
): Promise<{ error?: string; sessionFile?: string }> {
  const auth = AuthStorage.inMemory();
  const registry = ModelRegistry.inMemory(auth);
  const providers = new Map<string, SpecModel[]>();
  for (const t of s.targets)
    providers.set(t.provider, [...(providers.get(t.provider) ?? []), t]);
  for (const [name, ts] of providers) {
    registry.registerProvider(name, {
      baseUrl: ts[0]!.base_url,
      api: ts[0]!.api as never,
      apiKey: s.token,
      models: ts.map(piModel),
    });
    auth.setRuntimeApiKey(name, s.token);
  }
  const settings = SettingsManager.inMemory({
    compaction: { enabled: true },
    retry: { enabled: false },
    enableInstallTelemetry: false,
    enableSkillCommands: false,
  });
  let toolCalls = 0;
  const guard: ExtensionFactory = (pi) => {
    pi.on("tool_call", (event) => {
      toolCalls++;
      if (toolCalls > s.maxToolCalls)
        return {
          block: true,
          reason: `limit_reached: ${s.maxToolCalls} tool calls in this stage`,
        };
      if (
        isToolCallEventType("write", event) ||
        isToolCallEventType("edit", event)
      ) {
        const p = resolve(s.cwd, event.input.path);
        if (p !== s.workRoot && !p.startsWith(s.workRoot + "/"))
          return { block: true, reason: "writes outside /work are refused" };
      }
      return undefined;
    });
  };
  const loader = new DefaultResourceLoader({
    cwd: s.cwd,
    agentDir: s.agentDir,
    settingsManager: settings,
    noExtensions: true,
    noSkills: true,
    noPromptTemplates: true,
    noThemes: true,
    noContextFiles: true,
    additionalExtensionPaths: s.extensionPaths,
    extensionFactories: [guard, ...s.extensions],
    agentsFilesOverride: () => ({ agentsFiles: s.contextFiles }),
    systemPromptOverride: () => s.systemPrompt,
  });
  await loader.reload();
  for (const x of loader.getExtensions().errors)
    s.emit({
      type: "error",
      name: "extension",
      status: "failed",
      preview: `${x.path}: ${x.error}`.slice(0, 400),
    });
  mkdirSync(s.sessionDir, { recursive: true });
  const first = registry.find(s.targets[0]!.provider, s.targets[0]!.model);
  if (!first) return { error: "no model target for this stage" };
  const { session } = await createAgentSession({
    cwd: s.cwd,
    agentDir: s.agentDir,
    authStorage: auth,
    modelRegistry: registry,
    model: first,
    thinkingLevel: (s.targets[0]!.reasoning as never) || "off",
    tools: s.toolNames,
    resourceLoader: loader,
    sessionManager: SessionManager.create(s.cwd, s.sessionDir),
    settingsManager: settings,
  });
  const started = new Map<string, number>();
  const unsubscribe = session.subscribe((ev: AgentSessionEvent) => {
    if (ev.type === "tool_execution_start")
      started.set(ev.toolCallId, Date.now());
    if (ev.type === "tool_execution_end") {
      const begin = started.get(ev.toolCallId) ?? Date.now();
      const out = JSON.stringify(ev.result ?? "");
      s.emit({
        stage: s.stage,
        type: "tool_call",
        name: ev.toolName,
        status: ev.isError ? "error" : "ok",
        duration_ms: Date.now() - begin,
        preview: out.slice(0, 500),
        payload: { tool: ev.toolName, result: ev.result },
      });
    }
  });
  const onAbort = () => void session.abort();
  s.signal.addEventListener("abort", onAbort);
  let error: string | undefined;
  try {
    let target = 0;
    let message = s.prompt;
    for (;;) {
      await session.prompt(message);
      error = session.agent.state.errorMessage;
      if (s.signal.aborted) {
        error = "cancelled";
        break;
      }
      if (!error || !retryable(error) || target + 1 >= s.targets.length) break;
      // Fallback: move the session to the next target and continue.
      target++;
      const next = s.targets[target]!;
      const m = registry.find(next.provider, next.model);
      if (!m) break;
      s.emit({
        stage: s.stage,
        type: "note",
        name: "fallback",
        status: "switched",
        preview: `Switched to ${next.provider}/${next.model}: ${error}`.slice(
          0,
          500,
        ),
      });
      await session.setModel(m);
      message =
        "The model call failed and the session moved to another model. Continue the stage from where it stopped.";
    }
  } finally {
    s.signal.removeEventListener("abort", onAbort);
    unsubscribe();
  }
  const sessionFile = session.sessionFile;
  session.dispose();
  return { error, sessionFile };
}

export const sessionsDir = (root: string) => join(root, ".sayso", "sessions");
