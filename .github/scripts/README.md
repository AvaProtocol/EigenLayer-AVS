# GitHub Scripts

This directory holds scripts that GitHub Actions workflows call into.

## Release scripts have moved out of this repo

The previous `promote-and-publish.sh` (and its `scripts/release.sh`
wrapper at the repo root) live in a separate operator-side repo now.
That repo owns deployment topology (Railway project/env IDs, service
list, API tokens) which doesn't belong with application code.

The team that does releases knows where to run the new flow.

## Scripts that remain here

### `check-gh-setup.sh`

Verifies that GitHub CLI is properly configured for release automation.

**Checks:**
- GitHub CLI installation and authentication
- Repository access and permissions
- Existing releases and pre-releases
- Required workflow files

```bash
./.github/scripts/check-gh-setup.sh
```

### `generate-test-config.sh`

Generates test configuration files for the GitHub Actions workflows.

## Workflows that drive releases

- `release-on-pr-close.yml` — creates pre-release tags automatically
  on every PR merged to `main` or `staging`.
- `publish-dev-docker.yml` — builds development Docker images
  (`avaprotocol/avs-dev:vX.Y.Z`).
- `publish-prod-docker.yml` — builds production Docker images
  (`avaprotocol/ap-avs:vX.Y.Z`).

The two `publish-*` workflows accept `workflow_dispatch` inputs and are
invoked by the team's release tooling.
