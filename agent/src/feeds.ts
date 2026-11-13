import { execFile } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { homedir } from "node:os";
import { basename, join } from "node:path";
import { promisify } from "node:util";
import type { NewEvent } from "./api.js";
import type { SpecFeed } from "./protocol/index.js";

const run = promisify(execFile);

/** Repository files that can name a feed URL (§8.11). */
export const feedFiles = [".npmrc", "nuget.config", "NuGet.Config", "pip.conf", ".yarnrc.yml", "settings.xml"];

/** credentialOf returns the user name and secret that a run sends to a feed URL. */
function credentialOf(f: SpecFeed, token: string): { user: string; secret: string } {
  if (f.mode === "inject") return { user: f.username || "token", secret: f.credential ?? "" };
  return { user: "x-access-token", secret: token };
}

function withAuth(u: string, user: string, secret: string): string {
  const url = new URL(u);
  url.username = encodeURIComponent(user);
  url.password = encodeURIComponent(secret);
  return url.toString();
}

/** feedBase is the feed URL that the run uses instead of a match URL. */
export function feedBase(f: SpecFeed): string {
  return f.url.replace(/\/$/, "");
}

/**
 * configureFeeds writes the user-level configuration that points package
 * tools at the feeds (the package proxy in proxy mode), and replaces match
 * URLs in repository files, which it marks skip-worktree so that git diff
 * shows only the pull request's changes (REQ-PKG-003).
 */
export async function configureFeeds(feeds: SpecFeed[], token: string, head: string, emit: (e: NewEvent) => void): Promise<NodeJS.ProcessEnv> {
  const env: NodeJS.ProcessEnv = {};
  if (feeds.length === 0) return env;
  const home = homedir();
  const npmrc: string[] = [];
  const nugetSources: string[] = [];
  const nugetCreds: string[] = [];
  const pip: string[] = [];
  const maven: { id: string; url: string; user: string; secret: string }[] = [];
  const goproxies: string[] = [];
  const goprivate: string[] = [];
  for (const f of feeds) {
    const { user, secret } = credentialOf(f, token);
    const base = f.url.replace(/\/$/, "");
    switch (f.kind) {
      case "npm": {
        const host = base.replace(/^https?:/, "");
        if (!npmrc.some((l) => l.startsWith("registry="))) npmrc.push(`registry=${base}/`);
        npmrc.push(f.mode === "inject" && f.auth_style === "basic" ? `${host}/:_auth=${Buffer.from(`${user}:${secret}`).toString("base64")}` : `${host}/:_authToken=${secret}`);
        break;
      }
      case "nuget": {
        const index = f.match_urls.find((m) => m.endsWith("index.json"));
        const suffix = index ? new URL(index).pathname.split("/").slice(-2).join("/") : "v3/index.json";
        nugetSources.push(`    <add key="${f.name}" value="${base}/${suffix}" protocolVersion="3" />`);
        nugetCreds.push(`    <${f.name}>\n      <add key="Username" value="${user}" />\n      <add key="ClearTextPassword" value="${secret}" />\n    </${f.name}>`);
        break;
      }
      case "pypi":
        pip.push(withAuth(`${base}/simple/`, user, secret));
        break;
      case "maven":
        maven.push({ id: f.name, url: base, user, secret });
        break;
      case "goproxy":
        goproxies.push(withAuth(base, user, secret));
        break;
      case "github":
        for (const r of f.repos) {
          goprivate.push(`github.com/${r}`);
          await run("git", ["config", "--global", `url.${withAuth(`${base}/${r}`, user, secret)}.insteadOf`, `https://github.com/${r}`]);
        }
        break;
    }
  }
  if (npmrc.length) {
    npmrc.push("replace-registry-host=always");
    writeFileSync(join(home, ".npmrc"), npmrc.join("\n") + "\n", { mode: 0o600 });
  }
  if (nugetSources.length) {
    const dir = join(home, ".nuget", "NuGet");
    mkdirSync(dir, { recursive: true });
    writeFileSync(
      join(dir, "NuGet.Config"),
      `<?xml version="1.0" encoding="utf-8"?>\n<configuration>\n  <packageSources>\n${nugetSources.join("\n")}\n  </packageSources>\n  <packageSourceCredentials>\n${nugetCreds.join("\n")}\n  </packageSourceCredentials>\n</configuration>\n`,
      { mode: 0o600 },
    );
  }
  if (pip.length) {
    const dir = join(home, ".config", "pip");
    mkdirSync(dir, { recursive: true });
    writeFileSync(join(dir, "pip.conf"), `[global]\nindex-url = ${pip[0]}\n${pip.length > 1 ? `extra-index-url = ${pip.slice(1).join(" ")}\n` : ""}`, { mode: 0o600 });
  }
  if (maven.length) {
    const dir = join(home, ".m2");
    mkdirSync(dir, { recursive: true });
    const servers = maven.map((m) => `    <server><id>${m.id}</id><username>${m.user}</username><password>${m.secret}</password></server>`).join("\n");
    const mirrors = maven.map((m) => `    <mirror><id>${m.id}</id><mirrorOf>*</mirrorOf><url>${m.url}</url></mirror>`).join("\n");
    writeFileSync(join(dir, "settings.xml"), `<settings>\n  <servers>\n${servers}\n  </servers>\n  <mirrors>\n${mirrors}\n  </mirrors>\n</settings>\n`, { mode: 0o600 });
  }
  if (goproxies.length) env.GOPROXY = [...goproxies, "https://proxy.golang.org", "direct"].join(",");
  if (goprivate.length) {
    env.GOPRIVATE = goprivate.join(",");
    env.GONOSUMDB = goprivate.join(",");
    env.GONOSUMCHECK = goprivate.join(",");
  }
  await rewriteRepoFiles(feeds, head, emit);
  return env;
}

/** rewriteRepoFiles replaces match URLs in the repository's feed files and marks them skip-worktree. */
async function rewriteRepoFiles(feeds: SpecFeed[], head: string, emit: (e: NewEvent) => void): Promise<void> {
  const { stdout } = await run("git", ["ls-files"], { cwd: head, maxBuffer: 64 << 20 });
  const files = stdout.split("\n").filter((p) => feedFiles.includes(basename(p)));
  for (const p of files) {
    const full = join(head, p);
    const before = readFileSync(full, "utf8");
    let after = before;
    for (const f of feeds) {
      for (const m of f.match_urls) {
        if (m) after = after.split(m.replace(/\/$/, "")).join(feedBase(f));
      }
    }
    if (after === before) continue;
    writeFileSync(full, after);
    await run("git", ["update-index", "--skip-worktree", "--", p], { cwd: head });
    emit({ type: "env", name: `feed file ${p}`, status: "ok", preview: `${p} points at the package feeds` });
  }
}
