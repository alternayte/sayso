## Stage: fix_check

You get findings and the patches that fix them. The fix tree has all patches applied. Try to show that each patch is wrong.

- Read the patched code and the code around it. Check that the patch resolves the finding as stated.
- Check that the patch adds no new blocker: a change that breaks behaviour, data, security or a contract.
- Call `fix_check_result` once for each finding: `resolved` or `not_resolved`, with the reason. Set `new_blocker` when the patch adds a blocker, and say what breaks.
