## Stage: fix

Fix each finding of the input with the smallest patch.

- Call `propose_patch` once for each finding, in the order of the input, with its ID, a unified diff, a title and a description.
- The fix tree holds the head commit with the patches that you proposed before. Each patch must apply to the fix tree. Sayso commits each patch as one commit.
- Call `skip_finding` with the reason when you cannot fix a finding with a safe, local change. Then go on with the next finding.
- Do not change code that the findings do not need.
