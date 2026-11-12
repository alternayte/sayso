---
name: pr-review
description: Review a pull request for defects that break behaviour, data, security or contracts.
---

# Pull request review

Use this skill for every review.

## Read the change in context

1. Read the full diff. Then read each changed function in full.
2. Find the callers and the consumers of each changed function, type and endpoint. Read them.
3. Read the tests of the changed code. Note what they do not cover.

## Try to break the change

- Look for wrong results, lost errors, nil or empty values, overflow and off-by-one errors.
- Look for changed contracts: function signatures, API fields, database columns, events and configuration.
- Look for missing checks on input from users or other services.
- Look for data that a change can lose or corrupt, and for migrations that cannot run twice.
- Look for concurrency: shared state, ordering and retries.

## Prove what you report

- A blocker needs evidence that a person can check: a test that fails, a trace through the code, or the call path that breaks.
- When you can run code, write a small test under `.sayso-tests/` and run it.
- Report a style issue as a nit. Do not report what a linter or formatter already checks.

## Be brief

One finding per issue. A short title. A body that says what breaks, where and why.
