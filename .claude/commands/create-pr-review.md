# Create PR & Review Workflow

Execute the following steps for the current branch's PR:

## Steps

1. **Commit any uncommitted changes**
   Run: `git status`
   If there are staged or unstaged changes, commit them following the repository's commit conventions (use `git add` for relevant files, then `git commit`). Do NOT proceed with uncommitted changes — the PR should reflect all current work.

2. **Ensure branch is pushed to remote**
   Run: `git push -u origin $(git branch --show-current)`
   This ensures the local branch and all commits are synced to the remote before any PR operations.

3. **Check if PR already exists**
   Run: `gh pr view --json number,url 2>/dev/null`

   - If a PR already exists, capture the PR number and **skip to step 5**.
   - If no PR exists, continue to step 4.

4. **Create PR on GitHub**
   Determine the base branch:
   Run: `git branch --show-current`

   - If the current branch is `staging`, the base branch is `main`
   - Otherwise, the base branch is `staging`

   Run: `gh pr create --fill --base <BASE_BRANCH>`
   Capture the PR number from the output.

5. **Capture baseline timestamp**
   Run: `sh scripts/get-pr-comments.sh <PR_NUMBER>`
   Record the `created_at` timestamp of the newest Copilot review comment from `pr-comments-<PR_NUMBER>.json` — this is the **baseline timestamp**. If there are no Copilot comments yet, the baseline is empty (any comment after the review will be new).
   Run: `jq -r '.copilot.review_comments[-1].created_at // empty' pr-comments-<PR_NUMBER>.json`

   Also capture the latest Copilot **review** `submitted_at` timestamp (used to wait for the new review):
   Run: `gh api repos/AvaProtocol/EigenLayer-AVS/pulls/<PR_NUMBER>/reviews --jq '[.[] | select(.user.login == "copilot-pull-request-reviewer[bot]")] | sort_by(.submitted_at) | last | .submitted_at'`

6. **Request Copilot Review**
   Run: `gh api repos/AvaProtocol/EigenLayer-AVS/pulls/<PR_NUMBER>/requested_reviewers -f 'reviewers[]=copilot-pull-request-reviewer[bot]' --method POST`

7. **Wait for Copilot review to complete**
   Pass the baseline review timestamp so the script waits for a **new** review, not the old one:
   Run: `sh scripts/wait-for-copilot-review.sh <PR_NUMBER> 600 <BASELINE_REVIEW_TIMESTAMP>`
   This polls every 30s (up to 10 min) until a new Copilot review with a terminal state appears.

8. **Wait for new comments to appear**
   After the review state changes, poll until a comment newer than the baseline appears:
   Run: `sh scripts/get-pr-comments.sh <PR_NUMBER>`
   Then check: `jq -r '.copilot.review_comments[-1].created_at // empty' pr-comments-<PR_NUMBER>.json`
   If the newest `created_at` is the same as (or older than) the baseline comment timestamp from step 5, wait 15s and re-run. Repeat up to 10 times. If no newer comment appears after all retries, proceed anyway (Copilot may have approved with no comments).

9. **Evaluate comments**
   Read `pr-comments-<PR_NUMBER>.json` and evaluate only comments with `created_at` **newer than** the baseline timestamp from step 5. For each new comment, decide:

   - If it's a clear bug, correctness issue, or style issue → **FIX** it directly
   - If it's subjective or you disagree → **SKIP** with reasoning

   Present a summary table of all new comments with FIX/SKIP status before making changes.

   After applying fixes: lint, commit, and push.
