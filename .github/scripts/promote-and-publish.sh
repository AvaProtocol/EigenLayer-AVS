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

# Get the latest pre-release
echo -e "${BLUE}🔍 Finding the latest pre-release...${NC}"
LATEST_PRERELEASE=$(gh release list --repo "$REPO" --limit 50 --json tagName,isPrerelease,createdAt | \
    jq -r '.[] | select(.isPrerelease == true) | .tagName' | head -1)

if [ -z "$LATEST_PRERELEASE" ]; then
    echo -e "${RED}❌ No pre-release found. Make sure a pre-release exists.${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Found latest pre-release: ${LATEST_PRERELEASE}${NC}"

# Validate version format (should be like v1.13.2)
if [[ ! "$LATEST_PRERELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo -e "${YELLOW}⚠️  Warning: Version format doesn't match expected pattern (v1.13.2)${NC}"
    echo -e "${YELLOW}   Found: ${LATEST_PRERELEASE}${NC}"
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${BLUE}👋 Cancelled by user${NC}"
        exit 0
    fi
fi

# Confirm promotion
echo -e "${YELLOW}📋 About to promote pre-release to full release:${NC}"
echo -e "   Pre-release: ${LATEST_PRERELEASE}"
echo -e "   This will:"
echo -e "   • Convert pre-release to full release"
echo -e "   • Mark it as the latest release"
echo -e "   • Trigger dev Docker build (avaprotocol/avs-dev)"
echo -e "   • Trigger prod Docker build (avaprotocol/ap-avs) with 'latest' tag"
echo

read -p "Continue? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${BLUE}👋 Cancelled by user${NC}"
    exit 0
fi

# Promote pre-release to full release
echo -e "${BLUE}🔄 Promoting pre-release to full release...${NC}"
gh release edit "$LATEST_PRERELEASE" --repo "$REPO" --prerelease=false --latest

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ Successfully promoted ${LATEST_PRERELEASE} to full release${NC}"
else
    echo -e "${RED}❌ Failed to promote pre-release${NC}"
    exit 1
fi

# Wait a moment for GitHub to process the change
sleep 2

# Trigger dev Docker workflow
echo -e "${BLUE}🐳 Triggering dev Docker build workflow...${NC}"
gh workflow run "publish-dev-docker.yml" \
    --repo "$REPO" \
    --field git_tag="$LATEST_PRERELEASE" \
    --field branch_name="main" \
    --field fast_build=false

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ Dev Docker workflow triggered successfully${NC}"
    echo -e "   • Git tag: ${LATEST_PRERELEASE}"
    echo -e "   • Branch: main"
    echo -e "   • Image: avaprotocol/avs-dev:${LATEST_PRERELEASE}"
    echo -e "   • Image: avaprotocol/avs-dev:latest"
else
    echo -e "${RED}❌ Failed to trigger dev Docker workflow${NC}"
fi

# Wait a moment between workflow triggers
sleep 1

# Trigger production Docker workflow
echo -e "${BLUE}🏭 Triggering production Docker build workflow...${NC}"
gh workflow run "publish-prod-docker.yml" \
    --repo "$REPO" \
    --field git_tag="$LATEST_PRERELEASE" \
    --field branch_name="main" \
    --field tag_latest=true

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✅ Production Docker workflow triggered successfully${NC}"
    echo -e "   • Git tag: ${LATEST_PRERELEASE}"
    echo -e "   • Branch: main"
    echo -e "   • Image: avaprotocol/ap-avs:${LATEST_PRERELEASE}"
    echo -e "   • Image: avaprotocol/ap-avs:latest"
else
    echo -e "${RED}❌ Failed to trigger production Docker workflow${NC}"
fi

# Show workflow status links
echo -e "${BLUE}📊 Monitor workflow progress:${NC}"
echo -e "   Dev workflow:  https://github.com/${REPO}/actions/workflows/publish-dev-docker.yml"
echo -e "   Prod workflow: https://github.com/${REPO}/actions/workflows/publish-prod-docker.yml"

# Show final summary
echo
echo -e "${GREEN}🎉 Process completed successfully!${NC}"
echo -e "${BLUE}📋 Summary:${NC}"
echo -e "   • Released: ${LATEST_PRERELEASE} (now marked as latest)"
echo -e "   • Dev Docker: avaprotocol/avs-dev:${LATEST_PRERELEASE} & latest"
echo -e "   • Prod Docker: avaprotocol/ap-avs:${LATEST_PRERELEASE} & latest"
echo
echo -e "${YELLOW}💡 Next steps:${NC}"
echo -e "   • Monitor the workflow runs above"
echo -e "   • Verify Docker images are published correctly"
echo -e "   • Update deployment configurations if needed"

echo -e "${GREEN}✨ All done!${NC}"
