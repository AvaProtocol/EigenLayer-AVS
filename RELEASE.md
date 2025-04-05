# AP-AVS Release Process

## Overview

The release process follows a staged approach from development to production, with specific Docker image management for each stage.

## Branch and Docker Image Alignment

| Environment | Github Branch | Docker Image Repository      | Docker Tag Format          | Managed By                      | Trigger Condition                                 |
| ----------- | ------------- | ---------------------------- | -------------------------- | ------------------------------- | ------------------------------------------------- |
| Development | `dev`         | `avaprotocol/ap-avs-dev`     | `commit-hash`              | GA: `update-docker-dev`         | Manual trigger against dev branch                 |
| Staging     | `staging`     | `avaprotocol/ap-avs-staging` | `commit-hash`              | GA: `update-docker-staging`     | Automatic on PR merge to staging branch           |
| Production  | `main`        | `avaprotocol/ap-avs`         | `x.y.z` (semantic version) | GA: `release-and-update-docker` | Automatic on PR merge from staging to main        |
| Mainnet     | `mainnet`     | `avaprotocol/ap-avs`         | `mainnet`                  | GA: `update-docker-mainnet`     | Manual trigger to sync main to mainnet and deploy |

### Environment Details

- **Staging Branch**:

  - This branch serves as a caching and testing pool before changes are merged into main
  - The staging Docker image is updated after each PR merge from dev branches.
  - Changes in staging are considered pre-production and not yet live

- **Main Branch**:

  - Docker images are automatically built and published after merging into main
  - Creates a GitHub Release with the semantic versioning (x.y.z) and for image tags
  - Deploys to all environments (Ethereum, Base, etc.). Each environment uses the same version but different configurations

- **Mainnet Branch**:
  - Uses non-fast-forward merge to preserve mainnet-specific changes (different EigenSDK version)
  - After testing the Docker image on test environments, we run `update-docker-mainnet` to officially update the `avaprotocol/ap-avs@mainnet` tag
  - Operators automatically download and upgrade their images when the mainnet tag is updated
  - Requires manual trigger for deployment
  

### Important Notes

Due to the EigenSDK package difference on Ethereum mainnet, the implementation of the EigenSDK on `mainnet` branched out from `main`. For updating the `avaprotocol/ap-avs@mainnet` docker, the `update-docker-mainnet` GA perform the below actions.

1. Merge the working code from `main` to `mainnet`
2. Build Docker image from `mainnet`
3. Publish the docker to the `avaprotocol/ap-avs@mainnet` tag.

## Versioning and Releasing

The versioning process is automated and triggered when code is merged from `staging` to `main` through a pull request. The `release-on-pr` GitHub Action handles this process using `go-semantic-release`.

### Commit Message Format

For versioning to work correctly, commit messages must follow the conventional commit format:

- `feat:` for new features (minor version bump)
- `fix:` for bug fixes (patch version bump)
- `docs:` for documentation changes (no version bump)
- `BREAKING CHANGE:` or `feat!:` for breaking changes (major version bump)

### Release Process

When a PR from `staging` to `main` is merged, the following happens automatically:

1. Version Determination:

   - Analyzes commit messages since the last release
   - Determines the next version number based on commit types
   - Major version for breaking changes
   - Minor version for new features
   - Patch version for bug fixes

2. Release Creation:

   - Creates a GitHub Release with the new version
   - Builds and publishes Docker image to `avaprotocol/ap-avs` with version tag
   - Does NOT create Git tags automatically

3. Deployment:
   - Triggers deployments to all environments, currently `Sepolia`, `Base Sepolia`, `Ethereum` and `Base`.
   - Each environment uses the same version and docker image, but vary on `environment` and `directory`.

### Important Notes

- Docker images are tagged with the version number such as `x.y.z`
- The `latest` tag of Docker image is not automatically updated
- Git tags need to be created manually if needed
