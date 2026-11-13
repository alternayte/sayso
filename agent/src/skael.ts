import { execFile } from "node:child_process";
import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import type { NewEvent } from "./api.js";
import type { Skael, SkaelManifest } from "./protocol/index.js";

const run = promisify(execFile);

export interface Pulled {
  /** Folder of the skills: <dir>/.agents/skills. */
  skillsDir: string;
  manifest: SkaelManifest;
  /** The pi activation extension, when the hook is on. */
  extension?: string;
}

/** extensionPath is where skael pull installs the pi activation hook (§14.1). */
export const extensionPath = () => join(homedir(), ".pi", "agent", "extensions", "skael-tracking.ts");

/**
 * pullSkael writes ~/.skael/config.json with the agent key and runs
 * `skael pull` for the set. With the hook off it passes --no-hooks. A failed
 * pull returns undefined and records an error event, so the run uses the
 * next skill source (REQ-SKL-003).
 */
export async function pullSkael(sk: Skael, dir: string, emit: (e: NewEvent) => void): Promise<Pulled | undefined> {
  const home = homedir();
  mkdirSync(join(home, ".skael"), { recursive: true });
  writeFileSync(join(home, ".skael", "config.json"), JSON.stringify({ endpoint: sk.url, api_key: sk.agent_key ?? "", scope: "project", skills: [] }), { mode: 0o600 });
  // skael detects pi by this folder, and installs the hook there.
  if (sk.hook) mkdirSync(join(home, ".pi", "agent"), { recursive: true });
  const args = ["pull", "--json", "--set", sk.set, "--dir", dir];
  if (!sk.hook) args.push("--no-hooks");
  try {
    const { stdout } = await run("skael", args, { timeout: 120_000, maxBuffer: 16 << 20 });
    const out = JSON.parse(stdout) as { set: string; resolution_id: string; skills: { name: string; version: number; sha256: string }[] };
    const manifest: SkaelManifest = { set: out.set, resolution_id: out.resolution_id, skills: out.skills.map((s) => ({ name: s.name, version: s.version, sha256: s.sha256 })) };
    emit({ type: "skill_load", name: `skael ${sk.set}`, status: "pulled", preview: `${manifest.skills.length} skills of ${sk.set}, resolution ${manifest.resolution_id}`, payload: manifest });
    const ext = extensionPath();
    return { skillsDir: join(dir, ".agents", "skills"), manifest, extension: sk.hook && existsSync(ext) ? ext : undefined };
  } catch (e) {
    const err = e as { stderr?: string; message?: string };
    emit({ type: "error", name: "skael pull", status: "failed", preview: `Skael pull of ${sk.set} failed; the run uses the next skill source: ${(err.stderr || err.message || String(e)).slice(0, 400)}` });
    return undefined;
  }
}
