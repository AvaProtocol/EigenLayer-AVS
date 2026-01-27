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

# Get the latest release (could be pre-release or full release)
echo -e "${BLUE}🔍 Finding latest release for Docker publishing...${NC}"
LATEST_RELEASE_INFO=$(gh release list --repo "$REPO" --limit 1 --json tagName,isPrerelease | \
    jq -r '.[] | "\(.tagName)|\(.isPrerelease)"')

if [ -z "$LATEST_RELEASE_INFO" ]; then
    echo -e "${RED}❌ No releases found. Make sure a release exists.${NC}"
    exit 1
fi

LATEST_RELEASE=$(echo "$LATEST_RELEASE_INFO" | cut -d'|' -f1)
LATEST_IS_PRERELEASE=$(echo "$LATEST_RELEASE_INFO" | cut -d'|' -f2)

if [ "$LATEST_IS_PRERELEASE" = "true" ]; then
    echo -e "${GREEN}✅ Found latest release: ${LATEST_RELEASE} (pre-release)${NC}"
else
    echo -e "${GREEN}✅ Found latest release: ${LATEST_RELEASE}${NC}"
fi

# Get the latest full release (non-pre-release) for Docker publishing
LATEST_FULL_RELEASE=$(gh release list --repo "$REPO" --limit 50 --json tagName,isPrerelease | \
    jq -r '.[] | select(.isPrerelease == false) | .tagName' | head -n 1)

# Check if we should promote pre-releases too
PRERELEASES=$(gh release list --repo "$REPO" --limit 50 --json tagName,isPrerelease,createdAt | \
    jq -r '.[] | select(.isPrerelease == true) | .tagName' | sort -V)

if [ -n "$PRERELEASES" ]; then
    PRERELEASE_ARRAY=($PRERELEASES)
    PRERELEASE_COUNT=${#PRERELEASE_ARRAY[@]}
    echo -e "${YELLOW}📋 Found ${PRERELEASE_COUNT} pre-release(s) that could be promoted:${NC}"
    for release in "${PRERELEASE_ARRAY[@]}"; do
        echo -e "   • ${release}"
    done
else
    PRERELEASE_ARRAY=()
    PRERELEASE_COUNT=0
    echo -e "${BLUE}ℹ️  No pre-releases found to promote${NC}"
fi

# Determine which release to use for Docker publishing
# Use the latest release overall (which could be a pre-release if it's newer)
# This ensures we always publish the most recent version
if [ "$LATEST_IS_PRERELEASE" = "true" ]; then
    # Latest release is a pre-release, use it for Docker publishing
    DOCKER_RELEASE="$LATEST_RELEASE"
elif [ -n "$LATEST_FULL_RELEASE" ]; then
    # Latest release is a full release, use it
    DOCKER_RELEASE="$LATEST_FULL_RELEASE"
else
    # Fallback to latest release if no full release exists
    DOCKER_RELEASE="$LATEST_RELEASE"
fi

# Validate version formats for pre-releases if any exist
if [ $PRERELEASE_COUNT -gt 0 ]; then
    echo -e "${BLUE}🔍 Validating pre-release version formats...${NC}"
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
fi

# Validate Docker release version format
echo -e "${BLUE}🔍 Validating Docker release version format...${NC}"
if [[ ! "$DOCKER_RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo -e "${YELLOW}⚠️  Warning: Docker release version format doesn't match expected pattern (v1.13.2)${NC}"
    echo -e "${YELLOW}   Found: ${DOCKER_RELEASE}${NC}"
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${BLUE}👋 Cancelled by user${NC}"
        exit 0
    fi
fi

# Show what will be done
echo
echo -e "${YELLOW}📋 Summary of actions:${NC}"

if [ $PRERELEASE_COUNT -gt 0 ]; then
    LATEST_PRERELEASE="${PRERELEASE_ARRAY[$((${#PRERELEASE_ARRAY[@]} - 1))]}"
    echo -e "${BLUE}Pre-release promotion:${NC}"
    echo -e "   • Will promote ${PRERELEASE_COUNT} pre-release(s) to full release(s):"
    for release in "${PRERELEASE_ARRAY[@]}"; do
        echo -e "     - ${release}"
    done
    echo -e "   • Latest pre-release (${LATEST_PRERELEASE}) will be marked as 'latest'"
else
    echo -e "${BLUE}No pre-releases to promote${NC}"
fi

echo -e "${BLUE}Docker publishing:${NC}"
echo -e "   • Will trigger Docker builds for: ${DOCKER_RELEASE}"
echo -e "   • Dev image: avaprotocol/avs-dev:${DOCKER_RELEASE}"
echo -e "   • Prod image: avaprotocol/ap-avs:${DOCKER_RELEASE}"
if [[ "$DOCKER_RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo -e "   • Both will be tagged as 'latest'"
else
    echo -e "   • ⚠️  Note: Pre-release detected, 'latest' tag will NOT be applied"
fi
echo

# Confirm pre-release promotion if any exist
if [ $PRERELEASE_COUNT -gt 0 ]; then
    read -p "Promote pre-releases to full releases? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo -e "${BLUE}👋 Skipping pre-release promotion${NC}"
        SKIP_PROMOTION=true
    else
        SKIP_PROMOTION=false
    fi
else
    SKIP_PROMOTION=true
fi

# Confirm Docker publishing
echo
read -p "Trigger Docker image publishing for ${DOCKER_RELEASE}? (y/N): " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo -e "${BLUE}👋 Skipping Docker publishing${NC}"
    SKIP_DOCKER=true
else
    SKIP_DOCKER=false
fi

# Exit if user declined both actions
if [ "$SKIP_PROMOTION" = true ] && [ "$SKIP_DOCKER" = true ]; then
    echo -e "${BLUE}👋 No actions selected, exiting${NC}"
    exit 0
fi

# Promote pre-releases if requested and any exist
PROMOTION_SUCCESS=true
if [ "$SKIP_PROMOTION" = false ] && [ $PRERELEASE_COUNT -gt 0 ]; then
    echo -e "${BLUE}🔄 Promoting pre-releases to full releases...${NC}"
    
    for i in "${!PRERELEASE_ARRAY[@]}"; do
        release="${PRERELEASE_ARRAY[$i]}"
        echo -e "${BLUE}   Processing ${release}...${NC}"
        
        # Only mark the latest version as "latest"
        if [ "$release" = "${PRERELEASE_ARRAY[$((${#PRERELEASE_ARRAY[@]} - 1))]}" ]; then
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
    # Update Docker release to use the promoted pre-release
    if [ $PRERELEASE_COUNT -gt 0 ]; then
        LATEST_PRERELEASE="${PRERELEASE_ARRAY[$((${#PRERELEASE_ARRAY[@]} - 1))]}"
        DOCKER_RELEASE="$LATEST_PRERELEASE"
        echo -e "${BLUE}📦 Updated Docker release target to promoted release: ${DOCKER_RELEASE}${NC}"
    fi
else
    echo -e "${BLUE}⏭️  Skipping pre-release promotion${NC}"
fi

# Trigger Docker workflows for the latest release
if [ "$SKIP_DOCKER" = false ]; then
    echo -e "${BLUE}🐳 Triggering Docker build workflows for ${DOCKER_RELEASE}...${NC}"
    
    DOCKER_SUCCESS=true
    
    # Determine if we should tag as 'latest' (only for production tags, not pre-releases)
    TAG_LATEST="false"
    if [[ "$DOCKER_RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        TAG_LATEST="true"
    fi
    
    # Trigger dev Docker workflow
    echo -e "${BLUE}   🐳 Dev Docker build...${NC}"
    gh workflow run "publish-dev-docker.yml" \
        --repo "$REPO" \
        --field git_tag="$DOCKER_RELEASE" \
        --field branch_name="main" \
        --field fast_build=false
    
    if [ $? -eq 0 ]; then
        LATEST_TAG_INFO=""
        if [ "$TAG_LATEST" = "true" ]; then
            LATEST_TAG_INFO=" (latest)"
        fi
        echo -e "${GREEN}   ✅ Dev Docker workflow triggered for ${DOCKER_RELEASE}${LATEST_TAG_INFO}${NC}"
        echo -e "      • Image: avaprotocol/avs-dev:${DOCKER_RELEASE}${LATEST_TAG_INFO}"
    else
        echo -e "${RED}   ❌ Failed to trigger dev Docker workflow for ${DOCKER_RELEASE}${NC}"
        DOCKER_SUCCESS=false
    fi
    
    # Wait between workflows to avoid rate limiting
    sleep 2
    
    # Trigger production Docker workflow
    echo -e "${BLUE}   🏭 Production Docker build...${NC}"
    gh workflow run "publish-prod-docker.yml" \
        --repo "$REPO" \
        --field git_tag="$DOCKER_RELEASE" \
        --field branch_name="main" \
        --field tag_latest="$TAG_LATEST"
    
    if [ $? -eq 0 ]; then
        LATEST_TAG_INFO=""
        if [ "$TAG_LATEST" = "true" ]; then
            LATEST_TAG_INFO=" (latest)"
        fi
        echo -e "${GREEN}   ✅ Production Docker workflow triggered for ${DOCKER_RELEASE}${LATEST_TAG_INFO}${NC}"
        echo -e "      • Image: avaprotocol/ap-avs:${DOCKER_RELEASE}${LATEST_TAG_INFO}"
    else
        echo -e "${RED}   ❌ Failed to trigger production Docker workflow for ${DOCKER_RELEASE}${NC}"
        DOCKER_SUCCESS=false
    fi
else
    echo -e "${BLUE}⏭️  Skipping Docker publishing${NC}"
    DOCKER_SUCCESS=true
fi

if [ "$DOCKER_SUCCESS" != true ]; then
    echo -e "${YELLOW}⚠️  Some Docker workflows failed to trigger${NC}"
fi

# Show final summary
echo
echo -e "${GREEN}🎉 Process completed successfully!${NC}"
echo -e "${BLUE}📋 Summary:${NC}"

if [ "$SKIP_PROMOTION" = false ] && [ $PRERELEASE_COUNT -gt 0 ]; then
    echo -e "   • Promoted ${PRERELEASE_COUNT} pre-release(s) to full release(s):"
    for release in "${PRERELEASE_ARRAY[@]}"; do
        echo -e "     - ${release}"
    done
fi

if [ "$SKIP_DOCKER" = false ]; then
    LATEST_TAG_INFO=""
    if [[ "$DOCKER_RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        LATEST_TAG_INFO=" (latest)"
    fi
    echo -e "   • Docker workflows triggered for: ${DOCKER_RELEASE}"
    echo -e "   • Dev Docker image: avaprotocol/avs-dev:${DOCKER_RELEASE}${LATEST_TAG_INFO}"
    echo -e "   • Prod Docker image: avaprotocol/ap-avs:${DOCKER_RELEASE}${LATEST_TAG_INFO}"
fi

if [ "$SKIP_PROMOTION" = true ] && [ "$SKIP_DOCKER" = true ]; then
    echo -e "   • No actions performed (both skipped by user)"
elif [ "$SKIP_PROMOTION" = true ]; then
    echo -e "   • Pre-release promotion: Skipped"
elif [ "$SKIP_DOCKER" = true ]; then
    echo -e "   • Docker publishing: Skipped"
fi

echo
if [ "$SKIP_DOCKER" = false ]; then
    echo -e "${YELLOW}💡 Next steps:${NC}"
    echo -e "   • Monitor the workflow runs:"
    echo -e "     - Dev workflow:  https://github.com/${REPO}/actions/workflows/publish-dev-docker.yml"
    echo -e "     - Prod workflow: https://github.com/${REPO}/actions/workflows/publish-prod-docker.yml"
    echo -e "   • Verify Docker images are published correctly"
    echo -e "   • Update deployment configurations if needed"
    echo
fi

echo -e "${GREEN}✨ All done!${NC}"
