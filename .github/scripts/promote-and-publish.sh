#!/bin/bash

# Script to promote the latest pre-release to full release and trigger Docker builds
# Usage: ./promote-and-publish.sh

set -e  # Exit on any error

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Starting release promotion and Docker publishing process...${NC}"

# Check if gh CLI is installed and authenticated
if ! command -v gh &> /dev/null; then
    echo -e "${RED}❌ GitHub CLI (gh) is not installed. Please install it first.${NC}"
    echo "Visit: https://cli.github.com/"
    exit 1
fi

# Check if authenticated
if ! gh auth status &> /dev/null; then
    echo -e "${RED}❌ Not authenticated with GitHub CLI. Please run 'gh auth login' first.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ GitHub CLI is installed and authenticated${NC}"

# Get the repository info
REPO=$(gh repo view --json owner,name --jq '.owner.login + "/" + .name')
echo -e "${BLUE}📦 Working with repository: ${REPO}${NC}"

# Get all pre-releases that need to be promoted
echo -e "${BLUE}🔍 Finding all pre-releases to promote...${NC}"
PRERELEASES=$(gh release list --repo "$REPO" --limit 50 --json tagName,isPrerelease,createdAt | \
    jq -r '.[] | select(.isPrerelease == true) | .tagName' | sort -V)

if [ -z "$PRERELEASES" ]; then
    echo -e "${RED}❌ No pre-releases found. Make sure pre-releases exist.${NC}"
    exit 1
fi

# Convert to array for easier processing
PRERELEASE_ARRAY=($PRERELEASES)
PRERELEASE_COUNT=${#PRERELEASE_ARRAY[@]}

echo -e "${GREEN}✅ Found ${PRERELEASE_COUNT} pre-release(s) to promote:${NC}"
for release in "${PRERELEASE_ARRAY[@]}"; do
    echo -e "   • ${release}"
done

# Validate version formats
echo -e "${BLUE}🔍 Validating version formats...${NC}"
for release in "${PRERELEASE_ARRAY[@]}"; do
    if [[ ! "$release" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${YELLOW}⚠️  Warning: Version format doesn't match expected pattern (v1.13.2)${NC}"
        echo -e "${YELLOW}   Found: ${release}${NC}"
        read -p "Continue anyway? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            echo -e "${BLUE}👋 Cancelled by user${NC}"
            exit 0
        fi
        break
    fi
done

# Get the latest (highest version) pre-release for Docker latest tags
LATEST_PRERELEASE="${PRERELEASE_ARRAY[${#PRERELEASE_ARRAY[@]}-1]}"

# Confirm promotion
echo -e "${YELLOW}📋 About to promote ${PRERELEASE_COUNT} pre-release(s) to full release(s):${NC}"
for release in "${PRERELEASE_ARRAY[@]}"; do
    echo -e "   • ${release}"
done
echo -e "   Latest version (${LATEST_PRERELEASE}) will be tagged as 'latest'"
echo -e "   This will:"
echo -e "   • Convert all pre-releases to full releases"
echo -e "   • Mark latest version as the latest release"
echo -e "   • Trigger Docker builds for all versions"
echo -e "   • Tag latest version Docker images as 'latest'"
echo

read -p "Continue? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${BLUE}👋 Cancelled by user${NC}"
    exit 0
fi

# Promote all pre-releases to full releases
echo -e "${BLUE}🔄 Promoting pre-releases to full releases...${NC}"

PROMOTION_SUCCESS=true
for i in "${!PRERELEASE_ARRAY[@]}"; do
    release="${PRERELEASE_ARRAY[$i]}"
    echo -e "${BLUE}   Processing ${release}...${NC}"
    
    # Only mark the latest version as "latest"
    if [ "$release" = "$LATEST_PRERELEASE" ]; then
        gh release edit "$release" --repo "$REPO" --prerelease=false --latest
    else
        gh release edit "$release" --repo "$REPO" --prerelease=false
    fi
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}   ✅ Successfully promoted ${release} to full release${NC}"
    else
        echo -e "${RED}   ❌ Failed to promote ${release}${NC}"
        PROMOTION_SUCCESS=false
    fi
done

if [ "$PROMOTION_SUCCESS" != true ]; then
    echo -e "${RED}❌ Some promotions failed${NC}"
    exit 1
fi

# Wait a moment for GitHub to process the changes
sleep 2

# Trigger Docker workflows for all promoted releases
echo -e "${BLUE}🐳 Triggering Docker build workflows for all versions...${NC}"

DOCKER_SUCCESS=true
for release in "${PRERELEASE_ARRAY[@]}"; do
    echo -e "${BLUE}   Building Docker images for ${release}...${NC}"
    
    # Determine if this version should be tagged as 'latest'
    TAG_LATEST=false
    if [ "$release" = "$LATEST_PRERELEASE" ]; then
        TAG_LATEST=true
    fi
    
    # Trigger dev Docker workflow
    echo -e "${BLUE}     🐳 Dev Docker build...${NC}"
    gh workflow run "publish-dev-docker.yml" \
        --repo "$REPO" \
        --field git_tag="$release" \
        --field branch_name="main" \
        --field fast_build=false
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}     ✅ Dev Docker workflow triggered for ${release}${NC}"
        if [ "$TAG_LATEST" = true ]; then
            echo -e "        • Image: avaprotocol/avs-dev:${release} (latest)"
        else
            echo -e "        • Image: avaprotocol/avs-dev:${release}"
        fi
    else
        echo -e "${RED}     ❌ Failed to trigger dev Docker workflow for ${release}${NC}"
        DOCKER_SUCCESS=false
    fi
    
    # Wait between workflows to avoid rate limiting
    sleep 1
    
    # Trigger production Docker workflow
    echo -e "${BLUE}     🏭 Production Docker build...${NC}"
    gh workflow run "publish-prod-docker.yml" \
        --repo "$REPO" \
        --field git_tag="$release" \
        --field branch_name="main" \
        --field tag_latest="$TAG_LATEST"
    
    if [ $? -eq 0 ]; then
        echo -e "${GREEN}     ✅ Production Docker workflow triggered for ${release}${NC}"
        if [ "$TAG_LATEST" = true ]; then
            echo -e "        • Image: avaprotocol/ap-avs:${release} (latest)"
        else
            echo -e "        • Image: avaprotocol/ap-avs:${release}"
        fi
    else
        echo -e "${RED}     ❌ Failed to trigger production Docker workflow for ${release}${NC}"
        DOCKER_SUCCESS=false
    fi
    
    # Wait between versions to avoid overwhelming the system
    if [ "$release" != "$LATEST_PRERELEASE" ]; then
        sleep 2
    fi
done

if [ "$DOCKER_SUCCESS" != true ]; then
    echo -e "${YELLOW}⚠️  Some Docker workflows failed to trigger${NC}"
fi

# Show workflow status links
echo -e "${BLUE}📊 Monitor workflow progress:${NC}"
echo -e "   Dev workflow:  https://github.com/${REPO}/actions/workflows/publish-dev-docker.yml"
echo -e "   Prod workflow: https://github.com/${REPO}/actions/workflows/publish-prod-docker.yml"

# Show final summary
echo
echo -e "${GREEN}🎉 Process completed successfully!${NC}"
echo -e "${BLUE}📋 Summary:${NC}"
echo -e "   • Promoted ${PRERELEASE_COUNT} pre-release(s) to full release(s):"
for release in "${PRERELEASE_ARRAY[@]}"; do
    if [ "$release" = "$LATEST_PRERELEASE" ]; then
        echo -e "     - ${release} (marked as latest)"
    else
        echo -e "     - ${release}"
    fi
done
echo -e "   • Dev Docker images: avaprotocol/avs-dev (all versions)"
echo -e "   • Prod Docker images: avaprotocol/ap-avs (all versions)"
echo -e "   • Latest tags point to: ${LATEST_PRERELEASE}"
echo
echo -e "${YELLOW}💡 Next steps:${NC}"
echo -e "   • Monitor the workflow runs above"
echo -e "   • Verify Docker images are published correctly"
echo -e "   • Update deployment configurations if needed"

echo -e "${GREEN}✨ All done!${NC}"
