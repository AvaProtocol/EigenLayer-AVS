# AP-AVS Release Process

## Overview

The release process follows a staged approach from development to production, with specific Docker image management for each stage, automated and manually triggered via GitHub Actions.

## Branch and Docker Image Alignment

| Environment             | Github Branch   | Docker Image Repository      | Docker Tag Format                     | Managed By                                          | Trigger Condition                                        |
| ----------------------- | --------------- | ---------------------------- | ------------------------------------- | --------------------------------------------------- | -------------------------------------------------------- |
| Development             | `dev`           | N/A                          | N/A                                   | N/A                                                 | N/A                                                      |
| Staging                 | `staging`       | `avaprotocol/avs-dev`        | `<branch>-<sha>`, `latest`            | `publish-dev-docker.yml` (Manual)                   | Manual dispatch                                          |
| **Production (Pre)**    | `staging` (Merged PR) | N/A (GitHub Pre-release)   | `vX.Y.Z-rc.N`                         | `release-on-pr-close.yml`                           | Automatic on PR merge to `staging`                       |
| **Production (Release)**| `main`          | `avaprotocol/ap-avs`         | `vX.Y.Z`, `latest`                    | `publish-prod-docker.yml` (Manual)                  | Manual dispatch                                          |

### Environment Details

- **Staging Branch (`staging`)**:
  - Serves as a testing and integration pool before changes are promoted.
  - The `avaprotocol/avs-dev` Docker image can be updated by manually running the `publish-dev-docker.yml` workflow, typically targeting the `staging` branch. This produces tags like `staging-<sha>` and `latest`.
  - When a Pull Request is merged into `staging`, the `release-on-pr-close.yml` workflow is triggered. This workflow creates a GitHub Pre-release with a semantic version (e.g., `v0.5.0-rc.1`). No Docker image is built by this workflow.
  - Changes in `staging` are considered pre-production.

- **Main Branch (`main`)**:
  - This branch reflects production-ready code.
  - Production Docker images (`avaprotocol/ap-avs`) are built and published by manually running the `publish-prod-docker.yml` workflow, typically targeting a specific release tag (e.g., `vX.Y.Z`) on the `main` branch. This produces tags like `vX.Y.Z` and `latest`.
  - Deployments to production environments are typically done manually after a stable version is released and its Docker image is published.

- **Mainnet Branch (`mainnet`)**:
  - Uses non-fast-forward merge to preserve mainnet-specific changes (e.g., different EigenSDK version).
  - After testing the stable Docker image (from `main` branch releases, published via `publish-prod-docker.yml`) on test environments, a separate process (potentially a manual trigger of a dedicated workflow like `update-docker-mainnet`, not detailed here) is used to update the `avaprotocol/ap-avs:mainnet` tag.
  - Operators automatically download and upgrade their images when the `mainnet` tag is updated.
  - Requires manual intervention for promoting a version to the `mainnet` Docker tag and for deployment.

### Important Notes (EigenSDK on Mainnet)

Due to the EigenSDK package difference on Ethereum mainnet, the implementation of the EigenSDK on the `mainnet` branch has diverged from `main`. For updating the `avaprotocol/ap-avs:mainnet` Docker image, a dedicated process typically performs the following:

1. Merge the working code from `main` (or a specific release tag) to `mainnet`.
2. Build a Docker image from the `mainnet` branch.
3. Publish this Docker image to `avaprotocol/ap-avs:mainnet`.

## Versioning and Releasing

The versioning process for GitHub pre-releases is automated using `go-semantic-release` within the `release-on-pr-close.yml` workflow when PRs are merged to `staging`.

### Commit Message Format

For versioning to work correctly, commit messages **must** follow the Conventional Commits format:

- `feat:` for new features (results in a minor version bump for pre-releases).
- `fix:` for bug fixes (results in a patch version bump for pre-releases).
- `docs:` for documentation changes (typically no version bump by `go-semantic-release` unless configured otherwise).
- `BREAKING CHANGE:` in the commit body, or `type!:` (e.g., `feat!:`) for breaking changes (results in a major version bump for pre-releases).

## Release Process

### 1. Dry-run GitHub Pre-release locally (Optional)
To see what the new pre-release version would be if a PR were merged to `staging`, you can run `go-semantic-release` locally on your feature branch before creating the PR.
1. Install `go-semantic-release`, by running `go install github.com/go-semantic-release/semantic-release@latest`
2. Configure Github Developer Personal Token in `.env` file. The `.env` file will look like,
  ```
  GITHUB_TOKEN=your_github_token_here
  ```
  This is because `go-semantic-release` will pull existing tags.

  After editing the `.env`, run the below commands to make sure the GITHUB_TOKEN value is read in command-line
  ```
  source .env
  echo $GITHUB_TOKEN
  ```
3. Run the below command to dry run (replace `main` with `staging` if simulating merge to staging):
  ```
  semantic-release --dry --no-ci --token $GITHUB_TOKEN --provider github --provider-opt slug=AvaProtocol/EigenLayer-AVS --update version/version.go --allow-current-branch
  ```
  You should see the new proposed pre-release version in the output.

### 2. Merge PR to `staging`: GitHub Pre-release Creation
Once a PR is merged into `staging`, the `.github/workflows/release-on-pr-close.yml` action will automatically:
1. Run `go-semantic-release` to determine the new pre-release version (e.g., `vX.Y.Z-rc.N`).
2. Create a GitHub Pre-release with this version and a changelog generated from commit messages.

### 3. Publish `avs-dev` Docker Image for Staging/Testing (Manual)
After a GitHub pre-release is created (or at any point for testing `staging`):
1. Manually run the `Publish Dev Docker image` GitHub Action (`.github/workflows/publish-dev-docker.yml`).
   - Input `branch_name`: `staging` (or a specific commit hash on staging).
   - This will build and publish `avaprotocol/avs-dev` tagged as `staging-<sha>` and `latest`.
2. Update `sepolia`, `base-sepolia`, etc., with the new `avs-dev` Docker image and test them end-to-end.

#### Local Testing Before Publishing Docker
Before running the "Publish Dev Docker image" or "Publish Prod Docker image" GitHub Actions, you can test the Docker build locally:
```bash
# Get the current commit SHA
export COMMIT_SHA=$(git rev-parse --short HEAD)
# For a production release tag like v1.6.0
export RELEASE_TAG_FOR_BUILD=v1.6.0
# For a staging build (branch-sha)
# export RELEASE_TAG_FOR_BUILD=staging-$COMMIT_SHA 

# Build the Docker image locally (adjust Dockerfile and image name as needed)
docker build \\
  --build-arg RELEASE_TAG=$RELEASE_TAG_FOR_BUILD \\
  --build-arg COMMIT_SHA=$COMMIT_SHA \\
  --progress=plain \\
  -f dockerfiles/operator.Dockerfile \\
  -t avaprotocol/avs-dev:test \\
  .

# Test the built image
docker run --rm avaprotocol/avs-dev:test version
```
This will help catch any build issues. If the local build succeeds, proceed with the manual GitHub Action.

### 4. Publish Official Production Docker Image and Finalize Release (Manual)
After thorough testing of a pre-release and its corresponding `avs-dev` image:
1. Decide on the final stable version number (e.g., `v1.6.0`), usually based on the latest GitHub pre-release.
2. Create and push a git tag for this stable version on the `main` branch (e.g., `git tag v1.6.0 && git push origin v1.6.0`).
3. Manually run the `Publish Prod Docker image` GitHub Action (`.github/workflows/publish-prod-docker.yml`).
   - Input `git_tag`: The stable version tag (e.g., `v1.6.0`).
   - This will build and publish `avaprotocol/ap-avs` tagged as `vX.Y.Z` and `latest`.
4. Deploy the official `avaprotocol/ap-avs` Docker image to production environments (e.g., `ethereum`, `base`).
5. Go to GitHub Releases, find the pre-release that corresponds to this version, edit it, uncheck "This is a pre-release", update the tag to the stable tag if necessary, and publish it as the official release.

### Deployment

- Deployments to various environments (e.g., `Sepolia`, `Base Sepolia`, `Ethereum`, `Base`) are handled by the **manual** GitHub Action workflow: `deploy-avs.yml`.
- To deploy, a user manually triggers this workflow, selecting the target environment and the version (implicitly by deploying the code from a specific branch/tag).
- Each environment uses the same Docker image version (for a given release) but varies in runtime configuration.


### Important Notes

- Docker images are tagged as described: `staging-<sha>` and `latest` for `avs-dev` (manual); `vX.Y.Z` and `latest` for `ap-avs` (manual, from a git tag).
- GitHub Pre-release tags (`vX.Y.Z-rc.N`) are created automatically by `go-semantic-release` when PRs merge to `staging`. Stable git tags (`vX.Y.Z`) for production are created manually.
