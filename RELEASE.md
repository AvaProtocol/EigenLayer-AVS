# AP-AVS Release Process

## Overview

The release process follows a staged approach from development to production, with specific Docker image management for each stage, automated via GitHub Actions.

## Branch and Docker Image Alignment

| Environment             | Github Branch   | Docker Image Repository      | Docker Tag Format                     | Managed By                                          | Trigger Condition                                        |
| ----------------------- | --------------- | ---------------------------- | ------------------------------------- | --------------------------------------------------- | -------------------------------------------------------- |
| Development             | `dev`           | N/A                          | N/A                                   | N/A                                                 | N/A                                                      |
| Staging                 | `staging`       | `avaprotocol/avs-dev`        | `pr-XXX` (PR number)                  | `publish-dev-docker-on-staging.yml`                 | Automatic on PR merge to `staging`                       |
| **Production (PR)**     | `main` (PR)     | `avaprotocol/ap-avs`         | `vX.Y.Z-rc.N` (pre-release)         | `prerelease-and-update-docker-on-main.yml`          | Automatic on PR opened/synchronized to `main`            |
| **Production (Release)**| `main` (Merged) | `avaprotocol/ap-avs`         | `vX.Y.Z` (stable), `latest`, etc.     | `release-on-pr-close.yml`                           | Automatic on PR merged to `main`                         |

### Environment Details

- **Staging Branch (`staging`)**:
  - Serves as a testing and integration pool before changes are promoted.
  - The `avaprotocol/avs-dev` Docker image is updated with a `pr-XXX` tag (where XXX is the PR number) after each pull request from a `dev` branch is merged into `staging`. This is handled by the `publish-dev-docker-on-staging.yml` workflow.
  - Changes in `staging` are considered pre-production.

- **Main Branch (`main`)**:
  - This branch reflects production-ready code. The release process for `main` is two-phased:
    1.  **Pre-release on Pull Request**: When a Pull Request is opened or updated targeting `main`, the `prerelease-and-update-docker-on-main.yml` workflow automatically builds a pre-release Docker image tagged like `vX.Y.Z-rc.N` (e.g., `v0.5.0-rc.1`) and creates a corresponding GitHub pre-release. This allows for testing the exact build that could become a full release.
    2.  **Full Release on Merge**: Upon merging a Pull Request into `main`, the `release-on-pr-close.yml` workflow is triggered. This workflow creates a full GitHub Release with a stable semantic version (e.g., `v0.5.0`), and builds and publishes the `avaprotocol/ap-avs` Docker image tagged with this stable version, `latest`, and other semantic version tags (e.g., `v0.5`).
  - Deployments to production environments are typically done manually after a stable version is released.

- **Mainnet Branch (`mainnet`)**:
  - Uses non-fast-forward merge to preserve mainnet-specific changes (e.g., different EigenSDK version).
  - After testing the stable Docker image (from `main` branch releases) on test environments, a separate process (potentially a manual trigger of a dedicated workflow like `update-docker-mainnet`, not detailed in the provided workflow files) is used to update the `avaprotocol/ap-avs:mainnet` tag.
  - Operators automatically download and upgrade their images when the `mainnet` tag is updated.
  - Requires manual intervention for promoting a version to the `mainnet` Docker tag and for deployment.

### Important Notes (EigenSDK on Mainnet)

Due to the EigenSDK package difference on Ethereum mainnet, the implementation of the EigenSDK on the `mainnet` branch has diverged from `main`. For updating the `avaprotocol/ap-avs:mainnet` Docker image, a dedicated process (e.g., the `update-docker-mainnet` workflow) typically performs the following:

1. Merge the working code from `main` (or a specific release tag) to `mainnet`.
2. Build a Docker image from the `mainnet` branch.
3. Publish this Docker image to `avaprotocol/ap-avs:mainnet`.

## Versioning and Releasing

The versioning process is automated using `go-semantic-release` within GitHub Actions.

### Commit Message Format

For versioning to work correctly, commit messages **must** follow the Conventional Commits format:

- `feat:` for new features (results in a minor version bump for stable releases, or contributes to pre-release versioning).
- `fix:` for bug fixes (results in a patch version bump for stable releases, or contributes to pre-release versioning).
- `docs:` for documentation changes (typically no version bump by `go-semantic-release` unless configured otherwise).
- `BREAKING CHANGE:` in the commit body, or `type!:` (e.g., `feat!:`) for breaking changes (results in a major version bump for stable releases).

### Release Process for `main` Branch

The release process targeting the `main` branch involves two key automated workflows:

**A. Pre-release on Pull Request to `main` (via `prerelease-and-update-docker-on-main.yml`)**

This workflow triggers when a Pull Request is opened or synchronized against the `main` branch:

1.  **Pre-release Version Determination**:
    - `go-semantic-release` analyzes commit messages in the PR.
    - Determines the next pre-release version number (e.g., `v0.5.0-rc.1`, where `rc.1` might increment on subsequent pushes to the PR).

2.  **Pre-release Creation**:
    - Creates a GitHub Pre-Release with the new pre-release version.
    - Creates a Git tag for the pre-release version (e.g., `v0.5.0-rc.1`).

3.  **Docker Image Build and Push**:
    - Builds a Docker image from the PR's code.
    - Publishes the image to `avaprotocol/ap-avs` tagged only with the specific pre-release version (e.g., `avaprotocol/ap-avs:v0.5.0-rc.1`). The `latest` tag is **not** updated at this stage.

**B. Full Release on Pull Request Merge to `main` (via `release-on-pr-close.yml`)**

This workflow triggers when a Pull Request is closed and merged into the `main` branch:

1.  **Stable Version Determination**:
    - `go-semantic-release` analyzes commit messages on the `main` branch since the last stable release.
    - Determines the next stable semantic version number (e.g., `v0.5.0`).

2.  **Full Release Creation**:
    - Creates a GitHub Full Release with the new stable version and a generated changelog.
    - Creates a Git tag for the stable version (e.g., `v0.5.0`).

3.  **Docker Image Build and Push**:
    - Builds a Docker image from the merged code on `main`.
    - Publishes the image to `avaprotocol/ap-avs` with multiple tags:
        - The stable version (e.g., `avaprotocol/ap-avs:v0.5.0`).
        - `latest` (i.e., `avaprotocol/ap-avs:latest`).
        - Other semantic tags like major.minor (e.g., `v0.5`) and major (e.g., `v0`).

### Deployment

- Deployments to various environments (e.g., `Sepolia`, `Base Sepolia`, `Ethereum`, `Base`) are handled by the **manual** GitHub Action workflow: `deploy-avs.yml`.
- To deploy, a user manually triggers this workflow, selecting the target environment and the version (implicitly by deploying the code from a specific branch, usually `main` for production environments after a release, or `staging` for staging environments).
- Each environment uses the same Docker image version (for a given release) but varies in runtime configuration.

### Important Notes

- Docker images are tagged as described above: pre-releases for PRs to `main`, and stable versions + `latest` upon merge to `main`.
- Git tags (both for pre-releases and full releases) are created **automatically** by `go-semantic-release` as part of the respective workflows.
