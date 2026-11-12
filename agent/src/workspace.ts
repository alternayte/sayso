import { execFile } from "node:child_process";
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { promisify } from "node:util";
import type { Spec } from "./protocol/index.js";

const run = promisify(execFile);

export interface Workspace {
  root: string;
  repo: string;
  head: string;
  base: string;
}

async function git(
  cwd: string,
  args: string[],
  env?: NodeJS.ProcessEnv,
): Promise<string> {
  const { stdout } = await run("git", args, {
    cwd,
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0", ...env },
    maxBuffer: 64 << 20,
  });
  return stdout.trim();
}

/**
 * prepare clones the repository with the narrowed token (blobless), fetches
 * the base and head commits, creates the head and base worktrees and removes
 * the token from the git configuration (§8.3).
 */
export async function prepare(spec: Spec, root: string): Promise<Workspace> {
  const ws: Workspace = {
    root,
    repo: join(root, "repo"),
    head: join(root, "head"),
    base: join(root, "base"),
  };
  mkdirSync(ws.repo, { recursive: true });
  const auth = `Authorization: Basic ${Buffer.from(`x-access-token:${spec.repository.token ?? ""}`).toString("base64")}`;
  await git(ws.repo, ["init", "--quiet"]);
  await git(ws.repo, ["remote", "add", "origin", spec.repository.clone_url]);
  await git(ws.repo, ["config", "http.extraHeader", auth]);
  await git(ws.repo, ["config", "remote.origin.promisor", "true"]);
  await git(ws.repo, [
    "config",
    "remote.origin.partialclonefilter",
    "blob:none",
  ]);
  try {
    await git(ws.repo, [
      "fetch",
      "--quiet",
      "--filter=blob:none",
      "origin",
      spec.base_sha,
      spec.head_sha,
    ]);
    await git(ws.repo, [
      "worktree",
      "add",
      "--quiet",
      "--detach",
      ws.head,
      spec.head_sha,
    ]);
    await git(ws.repo, [
      "worktree",
      "add",
      "--quiet",
      "--detach",
      ws.base,
      spec.base_sha,
    ]);
  } finally {
    await git(ws.repo, ["config", "--unset-all", "http.extraHeader"]).catch(
      () => undefined,
    );
  }
  // Lazy blob fetches after this point would need the token again; fetch
  // nothing more and keep the worktrees complete.
  await git(ws.repo, [
    "config",
    "remote.origin.url",
    spec.repository.clone_url,
  ]);
  return ws;
}

/** diffStat returns git diff --stat between base and head. */
export async function diffStat(
  ws: Workspace,
  base: string,
  head: string,
): Promise<{ stat: string; files: string[] }> {
  const stat = await git(ws.repo, ["diff", "--stat", base, head]);
  const files = (await git(ws.repo, ["diff", "--name-only", base, head]))
    .split("\n")
    .filter(Boolean);
  return { stat, files };
}

/** diffOf returns the diff of one file between base and head. */
export async function diffOf(
  ws: Workspace,
  base: string,
  head: string,
  path: string,
): Promise<string> {
  return git(ws.repo, ["diff", base, head, "--", path]);
}
