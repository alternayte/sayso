import { execFile } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { promisify } from "node:util";
import type { ExtensionFactory } from "@mariozechner/pi-coding-agent";
import type { NewEvent } from "./api.js";
import type { Skael, SkaelManifest, SkillLoaded } from "./protocol/index.js";

const run = promisify(execFile);

export interface Pulled {
  /** Folder of the skills: <dir>/.agents/skills. */
  skillsDir: string;
  manifest: SkaelManifest;
}

/**
 * pullSkael writes ~/.skael/config.json with the agent key and runs
 * `skael pull` for the set. It always passes --no-hooks: the hook that the
 * CLI installs posts to a route that Skael refuses for service account keys,
 * so the runtime reports activations itself (skaelActivation). A failed pull
 * returns undefined and records an error event, so the run uses the next
 * skill source (REQ-SKL-003).
 */
export async function pullSkael(
  sk: Skael,
  dir: string,
  emit: (e: NewEvent) => void,
): Promise<Pulled | undefined> {
  const home = homedir();
  mkdirSync(join(home, ".skael"), { recursive: true });
  writeFileSync(
    join(home, ".skael", "config.json"),
    JSON.stringify({
      endpoint: sk.url,
      api_key: sk.agent_key ?? "",
      scope: "project",
      skills: [],
    }),
    { mode: 0o600 },
  );
  const args = ["pull", "--json", "--set", sk.set, "--dir", dir, "--no-hooks"];
  try {
    const { stdout } = await run("skael", args, {
      timeout: 120_000,
      maxBuffer: 16 << 20,
    });
    const out = JSON.parse(stdout) as {
      set: string;
      resolution_id: string;
      skills: { name: string; version: number; sha256: string }[];
    };
    const manifest: SkaelManifest = {
      set: out.set,
      resolution_id: out.resolution_id,
      skills: out.skills.map((s) => ({
        name: s.name,
        version: s.version,
        sha256: s.sha256,
      })),
    };
    emit({
      type: "skill_load",
      name: `skael ${sk.set}`,
      status: "pulled",
      preview: `${manifest.skills.length} skills of ${sk.set}, resolution ${manifest.resolution_id}`,
      payload: manifest,
    });
    return { skillsDir: join(dir, ".agents", "skills"), manifest };
  } catch (e) {
    const err = e as { stderr?: string; message?: string };
    emit({
      type: "error",
      name: "skael pull",
      status: "failed",
      preview: `Skael pull of ${sk.set} failed; the run uses the next skill source: ${(err.stderr || err.message || String(e)).slice(0, 400)}`,
    });
    return undefined;
  }
}

export interface Activation {
  skael: Skael;
  pulled: Pulled;
  stage: string;
  cwd: string;
  /** loaded is the skills_loaded list of the result; reported reads are marked or added. */
  loaded: SkillLoaded[];
  emit: (e: NewEvent) => void;
}

/**
 * skaelActivation reports to Skael, with the agent key, the first read of
 * each pulled SKILL.md in a stage (§14.1). It posts to /api/events/batch
 * under the round ID, so the activation joins the run that the API reports
 * the outcome for. The result marks the entry as reported, and the API does
 * not send it again.
 */
export function skaelActivation(a: Activation): ExtensionFactory {
  const role = a.stage === "challenge" ? "challenger" : "reviewer";
  const done = new Set<string>();
  return (pi) => {
    pi.on("tool_call", async (event) => {
      if (event.toolName !== "read" || !a.skael.external_run_id)
        return undefined;
      const path = resolve(
        a.cwd,
        String((event.input as { path?: unknown }).path ?? ""),
      );
      if (
        basename(path) !== "SKILL.md" ||
        dirname(dirname(path)) !== a.pulled.skillsDir
      )
        return undefined;
      const name = basename(dirname(path));
      const entry = a.pulled.manifest.skills.find((s) => s.name === name);
      if (!entry || done.has(name)) return undefined;
      done.add(name);
      const body = {
        events: [
          {
            skill: name,
            version: entry.version,
            agent: "pi",
            role,
            external_run_id: a.skael.external_run_id,
            time: new Date().toISOString(),
            set: a.pulled.manifest.set,
            resolution_id: a.pulled.manifest.resolution_id,
          },
        ],
      };
      try {
        const res = await fetch(
          `${a.skael.url.replace(/\/+$/, "")}/api/events/batch`,
          {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              Authorization: `Bearer ${a.skael.agent_key ?? ""}`,
              "Idempotency-Key": `sayso-${a.skael.external_run_id}-${a.stage}-${name}`,
            },
            body: JSON.stringify(body),
            signal: AbortSignal.timeout(10_000),
          },
        );
        if (!res.ok)
          throw new Error(
            `status ${res.status}: ${(await res.text()).slice(0, 300)}`,
          );
        const cur = a.loaded.find(
          (l) => l.name === name && l.stage === a.stage,
        );
        if (cur) cur.reported = true;
        else
          a.loaded.push({
            name,
            version: entry.version,
            stage: a.stage,
            reported: true,
          });
        a.emit({
          stage: a.stage,
          type: "skill_load",
          name,
          status: "activation_reported",
          preview: `Skael recorded the activation of ${name} ${entry.version}`,
        });
      } catch (e) {
        a.emit({
          stage: a.stage,
          type: "error",
          name: "skael activation",
          status: "failed",
          preview:
            `The activation of ${name} did not reach Skael; the API reports it with the outcome: ${e instanceof Error ? e.message : String(e)}`.slice(
              0,
              400,
            ),
        });
      }
      return undefined;
    });
  };
}
