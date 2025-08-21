#!/bin/bash

# Script to verify GitHub CLI setup for release automation
# Usage: ./check-gh-setup.sh

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🔍 Checking GitHub CLI setup for release automation...${NC}"
echo

# Check if gh CLI is installed
if command -v gh &> /dev/null; then
    GH_VERSION=$(gh --version | head -1)
    echo -e "${GREEN}✅ GitHub CLI is installed: ${GH_VERSION}${NC}"
else
    echo -e "${RED}❌ GitHub CLI (gh) is not installed${NC}"
    echo -e "${YELLOW}   Install from: https://cli.github.com/${NC}"
    exit 1
fi

# Check authentication
if gh auth status &> /dev/null; then
    echo -e "${GREEN}✅ GitHub CLI is authenticated${NC}"
    
    # Show current user
    GH_USER=$(gh api user --jq '.login' 2>/dev/null || echo "unknown")
    echo -e "${BLUE}   Authenticated as: ${GH_USER}${NC}"
else
    echo -e "${RED}❌ GitHub CLI is not authenticated${NC}"
    echo -e "${YELLOW}   Run: gh auth login${NC}"
    exit 1
fi

# Check repository access
if gh repo view &> /dev/null; then
    REPO_INFO=$(gh repo view --json owner,name --jq '.owner.login + "/" + .name')
    echo -e "${GREEN}✅ Repository access confirmed: ${REPO_INFO}${NC}"
    
    # Check permissions using viewerPermission
    VIEWER_PERMISSION=$(gh repo view --json viewerPermission --jq '.viewerPermission' 2>/dev/null || echo "unknown")
    case "$VIEWER_PERMISSION" in
        "ADMIN"|"MAINTAIN"|"WRITE")
            echo -e "${GREEN}✅ Write permissions confirmed (${VIEWER_PERMISSION})${NC}"
            ;;
        "READ"|"TRIAGE")
            echo -e "${YELLOW}⚠️  Limited permissions (${VIEWER_PERMISSION}) - may not be able to create releases or trigger workflows${NC}"
            ;;
        *)
            echo -e "${YELLOW}⚠️  Unknown permissions (${VIEWER_PERMISSION})${NC}"
            ;;
    esac
else
    echo -e "${RED}❌ Cannot access repository${NC}"
    echo -e "${YELLOW}   Make sure you're in a Git repository with GitHub remote${NC}"
    exit 1
fi

# Check for existing releases
echo -e "${BLUE}📋 Checking existing releases...${NC}"
RELEASE_COUNT=$(gh release list --limit 10 --json tagName | jq '. | length' 2>/dev/null || echo "0")
if [ "$RELEASE_COUNT" -gt 0 ]; then
    echo -e "${GREEN}✅ Found ${RELEASE_COUNT} existing releases${NC}"
    
    # Check for pre-releases
    PRERELEASE_COUNT=$(gh release list --limit 50 --json isPrerelease | jq '[.[] | select(.isPrerelease == true)] | length' 2>/dev/null || echo "0")
    if [ "$PRERELEASE_COUNT" -gt 0 ]; then
        echo -e "${GREEN}✅ Found ${PRERELEASE_COUNT} pre-releases available for promotion${NC}"
        
        # Show latest pre-release
        LATEST_PRERELEASE=$(gh release list --limit 50 --json tagName,isPrerelease,createdAt | jq -r '.[] | select(.isPrerelease == true) | .tagName' | head -1 2>/dev/null || echo "none")
        if [ "$LATEST_PRERELEASE" != "none" ]; then
            echo -e "${BLUE}   Latest pre-release: ${LATEST_PRERELEASE}${NC}"
        fi
    else
        echo -e "${YELLOW}⚠️  No pre-releases found - create one first or wait for PR merge${NC}"
    fi
else
    echo -e "${YELLOW}⚠️  No releases found - create one first or wait for PR merge${NC}"
fi

# Check workflow files
echo -e "${BLUE}🔍 Checking workflow files...${NC}"
if [ -f ".github/workflows/publish-dev-docker.yml" ]; then
    echo -e "${GREEN}✅ Dev Docker workflow found${NC}"
else
    echo -e "${RED}❌ Dev Docker workflow missing: .github/workflows/publish-dev-docker.yml${NC}"
fi

if [ -f ".github/workflows/publish-prod-docker.yml" ]; then
    echo -e "${GREEN}✅ Prod Docker workflow found${NC}"
else
    echo -e "${RED}❌ Prod Docker workflow missing: .github/workflows/publish-prod-docker.yml${NC}"
fi

if [ -f ".github/workflows/release-on-pr-close.yml" ]; then
    echo -e "${GREEN}✅ Release workflow found${NC}"
else
    echo -e "${RED}❌ Release workflow missing: .github/workflows/release-on-pr-close.yml${NC}"
fi

echo
echo -e "${GREEN}🎉 Setup check completed!${NC}"
echo -e "${BLUE}💡 You can now run: ./release.sh${NC}"
