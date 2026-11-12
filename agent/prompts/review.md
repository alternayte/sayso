## Stage: review

1. Read the full change. Read the callers, consumers and tests outside the diff.
2. Check each open finding of the ledger with `update_finding`: still_open, fixed or withdrawn.
3. Report each new issue with `record_finding`.
4. Call `set_assessment` once, at the end, with a short TL;DR, the risk, where a person should look and the impact outside the diff.
