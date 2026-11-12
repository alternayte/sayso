import { spawn } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import type { NewEvent } from "./api.js";
import type { EnvironmentOk, Overlay } from "./protocol/index.js";

export interface StepResult {
  step: string;
  status: "ok" | "failed" | "skipped";
  detail: string;
}

/** sh runs a command with a deadline and returns its exit code and the last part of its output. */
export function sh(cmd: string, args: string[], cwd: string, deadline: number, env: NodeJS.ProcessEnv = process.env): Promise<{ code: number; out: string }> {
  return new Promise((resolve) => {
    const left = deadline - Date.now();
    if (left <= 0) {
      resolve({ code: -1, out: "the setup timeout passed" });
      return;
    }
    const p = spawn(cmd, args, { cwd, env, stdio: ["ignore", "pipe", "pipe"], detached: true });
    let out = "";
    const keep = (b: Buffer) => {
      out = (out + b.toString()).slice(-4000);
    };
    p.stdout.on("data", keep);
    p.stderr.on("data", keep);
    const timer = setTimeout(() => {
      try {
        process.kill(-p.pid!, "SIGKILL");
      } catch {
        p.kill("SIGKILL");
      }
      out += "\nthe setup timeout passed";
    }, left);
    p.on("error", (e) => {
      clearTimeout(timer);
      resolve({ code: -1, out: e.message });
    });
    p.on("close", (code) => {
      clearTimeout(timer);
      resolve({ code: code ?? -1, out });
    });
  });
}

/** toolVersions maps the version files of a repository to mise tools (§8.10). */
export function toolVersions(dir: string): Record<string, string> {
  const tools: Record<string, string> = {};
  const read = (f: string) => (existsSync(join(dir, f)) ? readFileSync(join(dir, f), "utf8") : undefined);
  const gomod = read("go.mod");
  const goVer = gomod?.match(/^go\s+(\d+\.\d+(?:\.\d+)?)/m)?.[1];
  if (goVer) tools.go = goVer;
  const globalJSON = read("global.json");
  if (globalJSON) {
    try {
      const v = (JSON.parse(globalJSON) as { sdk?: { version?: string } }).sdk?.version;
      if (v) tools.dotnet = v;
    } catch {
      // A broken global.json leaves the preinstalled SDK in use.
    }
  }
  const node = (read(".nvmrc") ?? read(".node-version"))?.trim().replace(/^v/, "");
  if (node) tools.node = node;
  return tools;
}

/** restoreSteps lists the restore command of each detected stack. */
export function restoreSteps(dir: string): { name: string; cmd: string; args: string[] }[] {
  const has = (f: string) => existsSync(join(dir, f));
  const steps: { name: string; cmd: string; args: string[] }[] = [];
  if (has("go.mod")) steps.push({ name: "go mod download", cmd: "go", args: ["mod", "download"] });
  const dotnet = readdirSync(dir).some((f) => f.endsWith(".sln") || f.endsWith(".csproj") || f.endsWith(".slnx"));
  if (dotnet) steps.push({ name: "dotnet restore", cmd: "dotnet", args: ["restore"] });
  if (has("package-lock.json")) steps.push({ name: "npm ci", cmd: "npm", args: ["ci", "--no-audit", "--no-fund"] });
  else if (has("pnpm-lock.yaml")) steps.push({ name: "pnpm install", cmd: "pnpm", args: ["install", "--frozen-lockfile"] });
  else if (has("bun.lock") || has("bun.lockb")) steps.push({ name: "bun install", cmd: "bun", args: ["install", "--frozen-lockfile"] });
  return steps;
}

/**
 * prepareEnvironment installs toolchains, restores dependencies and runs the
 * overlay setup commands, all within one timeout. A failed step creates an
 * env event and a note for the review stage. It never fails the round
 * (REQ-RUN-013).
 */
export async function prepareEnvironment(
  dir: string,
  overlay: Overlay,
  timeoutSeconds: number,
  emit: (e: NewEvent) => void,
): Promise<{ steps: EnvironmentOk[]; note: string | undefined; env: NodeJS.ProcessEnv }> {
  const deadline = Date.now() + timeoutSeconds * 1000;
  const steps: EnvironmentOk[] = [];
  let env: NodeJS.ProcessEnv = { ...process.env };
  const failures: string[] = [];
  const record = (step: string, status: EnvironmentOk["status"], detail: string, ms: number) => {
    steps.push({ step, status, detail: detail.slice(-1000) });
    emit({ type: "env", name: step, status, duration_ms: ms, preview: detail.slice(-500), payload: { step, status, detail } });
    if (status === "failed") failures.push(`${step}: ${detail.split("\n").filter(Boolean).slice(-1)[0] ?? "failed"}`);
  };
  const step = async (name: string, cmd: string, args: string[]) => {
    const start = Date.now();
    const r = await sh(cmd, args, dir, deadline, env);
    record(name, r.code === 0 ? "ok" : "failed", r.code === 0 ? r.out.split("\n").filter(Boolean).slice(-3).join("\n") : r.out, Date.now() - start);
    return r.code === 0;
  };

  if (overlay.toolchain !== "none") {
    const hasMise = existsSync(join(dir, "mise.toml")) || existsSync(join(dir, ".mise.toml")) || existsSync(join(dir, ".tool-versions"));
    if (!hasMise) {
      const tools = toolVersions(dir);
      if (Object.keys(tools).length > 0) {
        // The global mise configuration selects the versions, so no repository file changes.
        const cfgDir = join(homedir(), ".config", "mise");
        mkdirSync(cfgDir, { recursive: true });
        writeFileSync(join(cfgDir, "config.toml"), "[tools]\n" + Object.entries(tools).map(([t, v]) => `${t} = "${v}"`).join("\n") + "\n");
      }
    }
    if (hasMise) await step("mise trust", "mise", ["trust", "--yes", dir]);
    if (await step("mise install", "mise", ["install", "--yes"])) {
      const r = await sh("mise", ["env", "--json"], dir, deadline, env);
      if (r.code === 0) {
        try {
          env = { ...env, ...(JSON.parse(r.out) as Record<string, string>) };
        } catch {
          record("mise env", "failed", "mise env did not answer JSON", 0);
        }
      }
    }
  }
  if (overlay.restore !== "none") {
    for (const s of restoreSteps(dir)) await step(s.name, s.cmd, s.args);
  }
  for (const cmd of overlay.setup ?? []) await step(`setup: ${cmd}`, "bash", ["-lc", cmd]);
  const note = failures.length ? `Build environment not available: ${failures.join("; ")}` : undefined;
  if (note) emit({ type: "note", name: "environment", status: "failed", preview: note });
  return { steps, note, env };
}
