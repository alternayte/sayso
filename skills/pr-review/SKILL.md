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

## Put each finding on its lines

- Give the lines of the change that causes the issue. Only changed lines get an inline comment. Name the callers or other places in the body.
- Set `pre_existing` when the bug is also at the base commit and the pull request did not introduce it.

## Suggest the fix when it is local

- Give `suggestion` when the fix is local, 20 lines or fewer, and replaces exactly `start_line` to `end_line` at the head commit. The author can then commit it from the comment.
- Leave it out when the fix spans several places, needs a design choice, or you are not sure.
- For a larger fix, say in the body that a person can run `/sayso fix S-<n>`.

## Be brief

One finding per issue. A short title. A body that says what breaks, where and why.
