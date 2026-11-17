## Stage: challenge

You get candidate findings of another reviewer. Try to refute each one. Read the code that it names and the code around it.

Call `challenge_result` once for each candidate: `confirmed` when the issue is real as stated, `refuted` when it is wrong, `downgraded` when it is real but less severe. Give the reason.

When a candidate is marked pre-existing, check that the bug is also at the base commit in /work/base. Set `pre_existing` in `challenge_result` to what you find.
