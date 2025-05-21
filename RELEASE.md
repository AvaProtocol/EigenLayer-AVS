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

## Release Process

### 1. Dry-run release locally
In order to see what the new version would be, run the below steps on the `staging` branch locally.
1. Install `semantic-release`, by running `go install github.com/go-semantic-release/semantic-release@latest`
2. Configure Github Developer Personal Token in .env file. The .env file will look like,
  ```
  GITHUB_TOKEN=
  ```
  This is because the `semantic-release` will pull the existing tags.

  After editing the .env, run the below commands to make sure the GITHUB_TOKEN value is read in command-line
  ```
  source .env
  echo $GITHUB_TOKEN
  ```
3. Run the below command to dry run,
  ```
  semantic-release --dry --no-ci --token $GITHUB_TOKEN --provider github --provider-opt slug=AvaProtocol/EigenLayer-AVS --update version/version.go
  ```
  You should see new version, e.g. 1.6.0, to be determined in the output.
  ```
  [go-semantic-release]: found version: 1.5.1
  [go-semantic-release]: getting commits...
  [go-semantic-release]: analyzing commits...
  [go-semantic-release]: commit-analyzer plugin: default@1.15.0
  [go-semantic-release]: calculating new version...
  [go-semantic-release]: new version: 1.6.0
  ```

### 2. Merge PR: staging to main and public Dev Docker
Once a PR is merged, the `.github/workflows/release-on-pr-close.yml` action will perform the below operations:
1. `semantic-release` will run on the main branch to determine the new version.
2. It will use `hooks: goreleaser` to create a Pre-release.

#### Local Testing Before Publishing Docker
Before running the "Publish Dev Docker image" GitHub Action, you can test the Docker build locally using:
```bash
# Get the current commit SHA
export COMMIT_SHA=$(git rev-parse --short HEAD)

# Build the Docker image locally
docker build \
  --build-arg RELEASE_TAG=v1.6.0 \
  --build-arg COMMIT_SHA=$COMMIT_SHA \
  --progress=plain \
  -f dockerfiles/operator.Dockerfile \
  -t avaprotocol/avs-dev:test \
  .

# Test the built image
docker run --rm avaprotocol/avs-dev:test version
```
This will help catch any build issues before triggering the GitHub Action. If the local build succeeds, proceed with step 3.

3. Manually run the `Publish Dev Docker image` Github Action, to build and publish docker with **main** branch, and the new version number. For example, if v1.6.0 is selected, a `@avaprotocol/avs-dev@v1.6.0` Docker image will be created.
4. Update `sepolia`, `base-sepolia`, etc. with the new `avs-dev` docker, and test them end-to-end.

### 3. Publish Release and official Docker
1. Deploy the official `@avaprotocol/ap-avs` Docker.
2. Upgrade the aggregators of `ethereum`, `base`, etc.
3. Mark the Pre-release as an official release.

### Deployment

- Deployments to various environments (e.g., `Sepolia`, `Base Sepolia`, `Ethereum`, `Base`) are handled by the **manual** GitHub Action workflow: `deploy-avs.yml`.
- To deploy, a user manually triggers this workflow, selecting the target environment and the version (implicitly by deploying the code from a specific branch, usually `main` for production environments after a release, or `staging` for staging environments).
- Each environment uses the same Docker image version (for a given release) but varies in runtime configuration.


### Important Notes

- Docker images are tagged as described above: pre-releases for PRs to `main`, and stable versions + `latest` upon merge to `main`.
- Git tags (both for pre-releases and full releases) are created **automatically** by `go-semantic-release` as part of the respective workflows.
