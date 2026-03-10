# Create PR & Review Workflow

Execute the following steps for the current branch's PR:

## Steps

1. **Determine base branch**
   Run: `git branch --show-current`

   - If the current branch is `staging`, the base branch is `main`
   - Otherwise, the base branch is `staging`

2. **Create PR on GitHub**
   Run: `gh pr create --fill --base <BASE_BRANCH>`
   Capture the PR number from the output.

3. **Request Copilot Review**
   Run: `gh api repos/AvaProtocol/studio/pulls/<PR_NUMBER>/requested_reviewers -f 'reviewers[]=copilot-pull-request-reviewer[bot]' --method POST`

4. **Wait for Copilot review to complete**
   Run: `sh scripts/wait-for-copilot-review.sh <PR_NUMBER>`
   This polls every 30s (up to 10 min) until Copilot's review state is no longer PENDING.

5. **Download PR comments**
   Run: `sh scripts/get-pr-comments.sh <PR_NUMBER>`
   This fetches unresolved Copilot and Claude review comments and saves them to `pr-comments-<PR_NUMBER>.json`.

6. **Evaluate comments**
   Read `pr-comments-<PR_NUMBER>.json` and for each comment, decide:

   - If it's a clear bug or style issue → apply the fix directly
   - If it's subjective or you disagree → output a SKIP decision with reasoning

   Present a summary table of all comments with FIX/SKIP status before making changes.
