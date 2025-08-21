# GitHub Scripts

This directory contains scripts for automating GitHub workflows and releases.

## Scripts

### `promote-and-publish.sh`

Automates the process of promoting a pre-release to a full release and triggering Docker builds.

**What it does:**
1. Finds the latest pre-release (e.g., `v1.13.2`)
2. Promotes it to a full release and marks it as "latest"
3. Triggers the dev Docker workflow (`publish-dev-docker.yml`)
4. Triggers the prod Docker workflow (`publish-prod-docker.yml`) with `latest` tag

**Usage:**
```bash
# From repository root
./.github/scripts/promote-and-publish.sh

# Or use the wrapper script
./release.sh
```

**Prerequisites:**
- GitHub CLI (`gh`) installed and authenticated
- Permissions to create releases and trigger workflows
- A pre-release must exist to promote

**Docker Images Created:**
- `avaprotocol/avs-dev:v1.13.2` and `avaprotocol/avs-dev:latest`
- `avaprotocol/ap-avs:v1.13.2` and `avaprotocol/ap-avs:latest`

### `check-gh-setup.sh`

Verifies that GitHub CLI is properly configured for release automation.

**What it checks:**
- GitHub CLI installation and authentication
- Repository access and permissions
- Existing releases and pre-releases
- Required workflow files

**Usage:**
```bash
./.github/scripts/check-gh-setup.sh
```

### `generate-test-config.sh`

Generates test configuration files for the GitHub Actions workflows.

## Workflow Integration

This script works with the following workflows:
- `release-on-pr-close.yml` - Creates pre-releases automatically
- `publish-dev-docker.yml` - Builds development Docker images
- `publish-prod-docker.yml` - Builds production Docker images

## Security Notes

- The script requires GitHub CLI authentication
- It will prompt for confirmation before making changes
- All operations are logged with colored output for clarity
