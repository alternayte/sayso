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

/** seedGitHub points GitHub at the fake and stores the acme App, with secrets that reference
 *  the variables of tests/e2e/compose.e2e.yaml. The manifest flow replaces this in a later slice. */
export function seedGitHub() {
  const url = JSON.stringify(stack.fakeURL);
  sql(`UPDATE settings SET value = '${url}'::jsonb, revision = revision + 1 WHERE key IN ('github_web_url', 'github_api_url')`);
  sql(`INSERT INTO secrets (id, name, kind, env_name, updated_by) VALUES
    (gen_random_uuid(), 'github.client_secret', 'env_ref', 'E2E_GH_CLIENT_SECRET', 'e2e'),
    (gen_random_uuid(), 'github.private_key', 'env_ref', 'E2E_GH_PRIVATE_KEY', 'e2e'),
    (gen_random_uuid(), 'github.webhook_secret', 'env_ref', 'E2E_GH_WEBHOOK_SECRET', 'e2e')
    ON CONFLICT (name) DO NOTHING`);
  sql(`INSERT INTO github_app (id, app_id, slug, org_login, client_id, client_secret_id, private_key_id, webhook_secret_id, html_url)
    SELECT gen_random_uuid(), 77, 'sayso-acme', 'acme', 'Iv1.acmeclient',
      (SELECT id FROM secrets WHERE name = 'github.client_secret'),
      (SELECT id FROM secrets WHERE name = 'github.private_key'),
      (SELECT id FROM secrets WHERE name = 'github.webhook_secret'),
      '${stack.fakeURL}/apps/sayso-acme'
    WHERE NOT EXISTS (SELECT 1 FROM github_app)`);
}

/** signIn runs the GitHub sign-in through the fake as login. */
export async function signIn(page: Page, login: string) {
  const res = await fetch(`${stack.fakeURL.replace("github.fake.localhost", "127.0.0.1")}/_fake/signin-as/${login}`, { method: "POST" });
  if (!res.ok) throw new Error(`fake sign-in: ${res.status}`);
  await page.goto("/signin");
  await page.getByRole("link", { name: "Sign in with GitHub" }).click();
  await page.waitForURL((u) => !u.pathname.startsWith("/auth/") && !u.pathname.startsWith("/login/"));
}
