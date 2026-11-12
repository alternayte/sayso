import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import type { Page } from "@playwright/test";

function readEnv(): Record<string, string> {
  const file = process.env.E2E_ENV_FILE;
  if (!file) throw new Error("E2E_ENV_FILE is not set: run the tests with just e2e");
  return Object.fromEntries(
    readFileSync(file, "utf8")
      .split("\n")
      .filter((l) => l.includes("="))
      .map((l) => [l.slice(0, l.indexOf("=")), l.slice(l.indexOf("=") + 1)]),
  );
}

const env = readEnv();

export const stack = {
  baseURL: env.E2E_BASE_URL!,
  fakeURL: env.E2E_FAKE_URL!,
  setupToken: env.E2E_SETUP_TOKEN!,
  project: env.E2E_PROJECT!,
};

/** sql runs a statement in the stack database and returns the rows as tab-separated text. */
export function sql(query: string): string {
  return execFileSync("docker", ["exec", `${stack.project}-postgres-1`, "psql", "-U", "sayso", "-d", "sayso", "-At", "-c", query], {
    encoding: "utf8",
  }).trim();
}

/** pointGitHubAtFake sets the GitHub URL settings to the fake. Setup cannot change settings, so the test does it. */
export function pointGitHubAtFake() {
  const url = JSON.stringify(stack.fakeURL);
  sql(`UPDATE settings SET value = '${url}'::jsonb, revision = revision + 1 WHERE key IN ('github_web_url', 'github_api_url') AND value <> '${url}'::jsonb`);
}

/** fake calls a control route of the fake GitHub. */
export async function fake(path: string) {
  const res = await fetch(`${stack.fakeURL.replace("github.fake.localhost", "127.0.0.1")}${path}`, { method: "POST" });
  if (!res.ok) throw new Error(`fake ${path}: ${res.status}`);
}

/** signIn runs the GitHub sign-in through the fake as login. */
export async function signIn(page: Page, login: string) {
  await fake(`/_fake/signin-as/${login}`);
  await page.goto("/signin");
  await page.getByRole("link", { name: "Sign in with GitHub" }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/auth/") && !u.pathname.startsWith("/login/"));
}
