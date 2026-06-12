# GitHub Scripts

This directory contains scripts that GitHub Actions workflows call into.

## Release flow lives in `avs-infra`

The previous `promote-and-publish.sh` (and its `scripts/release.sh`
wrapper at the repo root) moved to the private
[`avs-infra`](https://github.com/AvaProtocol/avs-infra) repo:

- `avs-infra/scripts/release.sh`
- `avs-infra/scripts/promote-and-publish.sh`

That's where the Railway-flip step lives now — project ID, environment
ID, service list, and `RAILWAY_API_TOKEN` are deployment topology that
doesn't belong with the application code. The release flow there:

1. Sources `avs-infra/.env` (tokens).
2. Talks to this repo via `gh` (override with `TARGET_REPO`): finds
   the latest pre-release, prompts to promote, dispatches
   `publish-prod-docker.yml` + `publish-dev-docker.yml`.
3. Watches the prod Docker build to completion.
4. Flips Railway services to the new image via the Railway GraphQL API.

See `avs-infra/RAILWAY_OPERATIONS.md` → "Releasing a new version
end-to-end" for the full runbook.

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

`avs-infra/scripts/release.sh` is what dispatches the two `publish-*`
workflows after promoting a pre-release.
