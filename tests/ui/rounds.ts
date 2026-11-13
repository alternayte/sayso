import { expect } from "@playwright/test";
import { sql, stack } from "./stack";

const fakeBase = () =>
  stack.fakeURL.replace("github.fake.localhost", "127.0.0.1");

export async function fakeJSON<T>(
  path: string,
  body: unknown = {},
): Promise<T> {
  const res = await fetch(`${fakeBase()}${path}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`fake ${path}: ${res.status}`);
  return (await res.json()) as T;
}

export type State = {
  checks: {
    HeadSHA: string;
    Status: string;
    Conclusion: string;
    Title: string;
  }[];
  issue_comments: { Body: string }[];
  review_comments: { Number: number; Body: string }[];
};

export async function state(repo: string, n: number): Promise<State> {
  const res = await fetch(`${fakeBase()}/_fake/state?repo=${repo}&number=${n}`);
  const s = (await res.json()) as Partial<State>;
  return {
    checks: s.checks ?? [],
    issue_comments: s.issue_comments ?? [],
    review_comments: s.review_comments ?? [],
  };
}

export const noCheck = `// Package ledger records payments.
package ledger

import "errors"

// ErrNegative is returned for a negative amount.
var ErrNegative = errors.New("ledger: negative amount")

// Ledger holds the balance of one account in cents.
type Ledger struct {
	balance int64
}

// Post adds amount cents to the balance.
func (l *Ledger) Post(amount int64) error {
	// Refunds pass negative amounts now.
	_ = ErrNegative
	_ = amount
	l.balance += amount
	return nil
}

// Balance returns the balance in cents.
func (l *Ledger) Balance() int64 { return l.balance }
`;

/** open commits the script marker to main, sets the mode and opens a pull request. */
export async function open(repo: string, script: string, mode: string) {
  sql(
    `UPDATE repositories SET mode = '${mode}', execution = false WHERE full_name = '${repo}'`,
  );
  await fakeJSON(`/_fake/main?repo=${repo}`, {
    files: { ".sayso.yml": `instructions: "[script:${script}]"\n` },
  });
  return fakeJSON<{ number: number; head_sha: string }>(
    `/_fake/pulls?repo=${repo}`,
    { files: { "ledger/ledger.go": noCheck } },
  );
}

export function roundOf(repo: string, n: number, status = ""): string {
  return sql(
    `SELECT ro.id FROM rounds ro JOIN reviews rv ON rv.id = ro.review_id JOIN repositories rp ON rp.id = rv.repository_id
     WHERE rp.full_name = '${repo}' AND rv.pr_number = ${n} ${status ? `AND ro.status = '${status}'` : ""} ORDER BY ro.created_at DESC LIMIT 1`,
  );
}

export async function waitRound(
  repo: string,
  n: number,
  status: string,
  ms = 120_000,
) {
  await expect
    .poll(() => roundOf(repo, n, status), { timeout: ms, intervals: [1000] })
    .not.toBe("");
  return roundOf(repo, n, status);
}
