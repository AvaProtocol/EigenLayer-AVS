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

# Railway auto-update configuration.
#
# After the prod Docker workflow finishes, the script can flip each
# Railway service's pinned Source Image from the previous tag to the
# new ${DOCKER_RELEASE}, then trigger a redeploy. The Railway CLI does
# not expose a "set service image" verb, so we call the Railway
# GraphQL API directly (mutations: serviceInstanceUpdate +
# serviceInstanceRedeploy).
#
# Skipped if RAILWAY_API_TOKEN isn't set. Failures within a service
# warn-and-continue per the "Docker publish already succeeded, don't
# undo that progress" stance.
#
# Generate a token at: https://railway.com/account/tokens (Team token
# scoped to the AvaProtocol workspace works).
RAILWAY_GRAPHQL_URL="https://backboard.railway.app/graphql/v2"
RAILWAY_PROJECT_ID="${RAILWAY_PROJECT_ID:-9499ccd8-1610-4fda-bbcb-ef7d9e453f60}"
RAILWAY_ENV_ID="${RAILWAY_ENV_ID:-ba5658ef-bafd-4aa8-8dda-c1ee3ba4c14d}"
# Services that pull avaprotocol/ap-avs and need their tag bumped on
# every prod release. Order matters: gateway first, workers after, so
# operators reconnect cleanly. Override via env if you ever add/remove
# services without touching this script:
#   RAILWAY_SERVICES="gateway worker-sepolia" ./scripts/release.sh
RAILWAY_SERVICES="${RAILWAY_SERVICES:-gateway worker-ethereum worker-base worker-sepolia worker-base-sepolia worker-bnb-mainnet}"

# railway_graphql <query-json>
# Wraps the GraphQL POST with auth + JSON content-type. Echoes the
# raw response so callers can jq it themselves. Returns 0 if the
# HTTP call succeeded; the response may still contain {"errors":...}
# which the caller must check.
railway_graphql() {
    local payload="$1"
    curl -sS --max-time 30 -X POST "$RAILWAY_GRAPHQL_URL" \
        -H "Authorization: Bearer $RAILWAY_API_TOKEN" \
        -H "Content-Type: application/json" \
        --data "$payload"
}

# railway_update_service <service-name> <full-image-ref>
# Updates the service's Source Image to the given ref (e.g.
# avaprotocol/ap-avs:v3.4.2) and triggers a redeploy. Returns 0 on
# success, 1 on any failure (looked-up service ID missing, GraphQL
# error response, network error). Always prints what it did so the
# operator can see progress live.
railway_update_service() {
    local service_name="$1"
    local image_ref="$2"

    # Resolve service ID by name. Querying project.services keeps the
    # script resilient to service recreations that would invalidate
    # a hardcoded ID.
    local lookup_payload
    lookup_payload=$(jq -nc \
        --arg pid "$RAILWAY_PROJECT_ID" \
        '{query: "query($pid: String!){project(id: $pid){services{edges{node{id name}}}}}", variables: {pid: $pid}}')
    local lookup_response
    lookup_response=$(railway_graphql "$lookup_payload") || {
        echo -e "${YELLOW}   ⚠️  ${service_name}: Railway GraphQL unreachable (network?). Skipping.${NC}"
        return 1
    }
    if echo "$lookup_response" | jq -e '.errors' >/dev/null 2>&1; then
        local err
        err=$(echo "$lookup_response" | jq -r '.errors[0].message // "unknown GraphQL error"')
        echo -e "${YELLOW}   ⚠️  ${service_name}: lookup failed: ${err}. Skipping.${NC}"
        return 1
    fi
    local service_id
    service_id=$(echo "$lookup_response" | \
        jq -r --arg name "$service_name" \
        '.data.project.services.edges[].node | select(.name==$name) | .id' | head -n1)
    if [ -z "$service_id" ] || [ "$service_id" = "null" ]; then
        echo -e "${YELLOW}   ⚠️  ${service_name}: service not found in project. Skipping.${NC}"
        return 1
    fi

    # Update source.image. We pass only the source field so other
    # service config (start command, region, replicas, healthcheck)
    # stays untouched.
    local update_payload
    update_payload=$(jq -nc \
        --arg eid "$RAILWAY_ENV_ID" \
        --arg sid "$service_id" \
        --arg img "$image_ref" \
        '{query: "mutation($eid: String!, $sid: String!, $img: String!){serviceInstanceUpdate(environmentId: $eid, serviceId: $sid, input: {source: {image: $img}})}", variables: {eid: $eid, sid: $sid, img: $img}}')
    local update_response
    update_response=$(railway_graphql "$update_payload") || {
        echo -e "${YELLOW}   ⚠️  ${service_name}: update call failed (network?). Skipping redeploy.${NC}"
        return 1
    }
    if echo "$update_response" | jq -e '.errors' >/dev/null 2>&1; then
        local err
        err=$(echo "$update_response" | jq -r '.errors[0].message // "unknown GraphQL error"')
        echo -e "${YELLOW}   ⚠️  ${service_name}: serviceInstanceUpdate failed: ${err}.${NC}"
        return 1
    fi

    # Trigger redeploy so the new image actually gets pulled.
    local redeploy_payload
    redeploy_payload=$(jq -nc \
        --arg eid "$RAILWAY_ENV_ID" \
        --arg sid "$service_id" \
        '{query: "mutation($eid: String!, $sid: String!){serviceInstanceRedeploy(environmentId: $eid, serviceId: $sid)}", variables: {eid: $eid, sid: $sid}}')
    local redeploy_response
    redeploy_response=$(railway_graphql "$redeploy_payload") || {
        echo -e "${YELLOW}   ⚠️  ${service_name}: image was updated but redeploy call failed. Trigger manually in dashboard.${NC}"
        return 1
    }
    if echo "$redeploy_response" | jq -e '.errors' >/dev/null 2>&1; then
        local err
        err=$(echo "$redeploy_response" | jq -r '.errors[0].message // "unknown GraphQL error"')
        echo -e "${YELLOW}   ⚠️  ${service_name}: redeploy mutation failed: ${err}. Trigger manually.${NC}"
        return 1
    fi

    echo -e "${GREEN}   ✅ ${service_name}: image → ${image_ref}, redeploy triggered${NC}"
    return 0
}

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

    # Print release notes for each pre-release
    echo
    for release in "${PRERELEASE_ARRAY[@]}"; do
        RELEASE_BODY=$(gh release view "$release" --repo "$REPO" --json body --jq '.body // empty')
        echo -e "${YELLOW}── ${release} ──${NC}"
        if [ -n "$RELEASE_BODY" ]; then
            echo "$RELEASE_BODY"
        else
            echo -e "   (no release notes)"
        fi
        echo
    done
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
    
    # Trigger production Docker workflow.
    # The prod workflow only accepts `git_tag` (REQUIRED — must be a published,
    # non-draft, non-prerelease GitHub release) and `tag_latest`. Skip dispatch
    # entirely when DOCKER_RELEASE is a pre-release (e.g. user chose to publish
    # without promoting the pre-release) — the workflow would reject it anyway,
    # but skipping avoids a misleading "✅ workflow triggered" line followed by
    # a CI failure.
    if [[ ! "$DOCKER_RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${YELLOW}   ⏭️  Skipping prod Docker build: ${DOCKER_RELEASE} looks like a pre-release.${NC}"
        echo -e "${YELLOW}      Prod images must come from a full release tag (vX.Y.Z). Promote the${NC}"
        echo -e "${YELLOW}      pre-release first via this script's promotion step, then re-run.${NC}"
        DOCKER_SUCCESS=false
    else
    echo -e "${BLUE}   🏭 Production Docker build...${NC}"
    gh workflow run "publish-prod-docker.yml" \
        --repo "$REPO" \
        --field git_tag="$DOCKER_RELEASE" \
        --field tag_latest="$TAG_LATEST"

    if [ $? -eq 0 ]; then
        LATEST_TAG_INFO=""
        if [ "$TAG_LATEST" = "true" ]; then
            LATEST_TAG_INFO=" (latest)"
        fi
        echo -e "${GREEN}   ✅ Production Docker workflow triggered for ${DOCKER_RELEASE}${LATEST_TAG_INFO}${NC}"
        echo -e "      • Image: avaprotocol/ap-avs:${DOCKER_RELEASE}${LATEST_TAG_INFO}"
        # Capture the run ID we just dispatched so we can wait on it
        # below (gh workflow run doesn't return the ID in stdout).
        # Brief sleep gives GitHub time to register the new run before
        # we list it, otherwise the "most recent" run is the previous
        # one and `gh run watch` returns immediately with success.
        sleep 3
        PROD_RUN_ID=$(gh run list --repo "$REPO" --workflow="publish-prod-docker.yml" --limit 1 --json databaseId --jq '.[0].databaseId')
    else
        echo -e "${RED}   ❌ Failed to trigger production Docker workflow for ${DOCKER_RELEASE}${NC}"
        DOCKER_SUCCESS=false
    fi
    fi  # close the prerelease-skip guard
else
    echo -e "${BLUE}⏭️  Skipping Docker publishing${NC}"
    DOCKER_SUCCESS=true
fi

if [ "$DOCKER_SUCCESS" != true ]; then
    echo -e "${YELLOW}⚠️  Some Docker workflows failed to trigger${NC}"
fi

# ---- Watch prod Docker build to completion + (optionally) update Railway ----
#
# Only the prod image is what Railway pulls, so we wait on that one
# specifically; the dev workflow can finish whenever. We also skip the
# Railway path for non-full-release tags (e.g. -rc.N) — back-fills and
# pre-releases shouldn't propagate to prod gateway/workers.
PROD_WORKFLOW_OK=false
if [ "$SKIP_DOCKER" = false ] \
   && [[ "$DOCKER_RELEASE" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
   && [ -n "${PROD_RUN_ID:-}" ]; then
    echo
    echo -e "${BLUE}⏳ Waiting for prod Docker workflow to finish (run #${PROD_RUN_ID})...${NC}"
    echo -e "   ${YELLOW}https://github.com/${REPO}/actions/runs/${PROD_RUN_ID}${NC}"
    if gh run watch "$PROD_RUN_ID" --repo "$REPO" --exit-status >/dev/null 2>&1; then
        echo -e "${GREEN}✅ Prod Docker workflow succeeded${NC}"
        PROD_WORKFLOW_OK=true
    else
        echo -e "${RED}❌ Prod Docker workflow failed — skipping Railway update.${NC}"
        echo -e "${RED}   Check the run for details before retrying.${NC}"
    fi
fi

if [ "$PROD_WORKFLOW_OK" = true ]; then
    if [ -n "${RAILWAY_API_TOKEN:-}" ]; then
        IMAGE_REF="avaprotocol/ap-avs:${DOCKER_RELEASE}"
        echo
        echo -e "${YELLOW}🚂 Update Railway services to ${IMAGE_REF}?${NC}"
        echo -e "   Services (in order): ${RAILWAY_SERVICES}"
        read -p "Proceed? (y/N): " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            # Sanity-check jq is installed (the GraphQL helpers depend
            # on it). curl is standard on macOS + Ubuntu; jq is not.
            if ! command -v jq &> /dev/null; then
                echo -e "${YELLOW}⚠️  jq not found in PATH. Install jq (brew install jq) and re-run, or update Railway manually.${NC}"
            else
                echo -e "${BLUE}Updating Railway services...${NC}"
                RAILWAY_FAILS=0
                for svc in $RAILWAY_SERVICES; do
                    railway_update_service "$svc" "$IMAGE_REF" || RAILWAY_FAILS=$((RAILWAY_FAILS+1))
                done
                if [ "$RAILWAY_FAILS" -gt 0 ]; then
                    echo -e "${YELLOW}⚠️  ${RAILWAY_FAILS} Railway service(s) failed to update — finish those by hand in the dashboard.${NC}"
                else
                    echo -e "${GREEN}✅ All Railway services pointed at ${IMAGE_REF}${NC}"
                fi
            fi
        else
            echo -e "${BLUE}⏭️  Skipping Railway update (do it from the dashboard if you change your mind)${NC}"
        fi
    else
        echo
        echo -e "${YELLOW}ℹ️  RAILWAY_API_TOKEN not set — skipping Railway auto-update.${NC}"
        echo -e "${YELLOW}   To enable next time:${NC}"
        echo -e "${YELLOW}     1. Create a Team token at https://railway.com/account/tokens${NC}"
        echo -e "${YELLOW}     2. Export it: export RAILWAY_API_TOKEN=<token>${NC}"
        echo -e "${YELLOW}     3. Re-run ./scripts/release.sh after the next merge.${NC}"
        echo -e "${YELLOW}   Or update services manually in the Railway dashboard.${NC}"
    fi
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

if [ "$PROD_WORKFLOW_OK" = true ] && [ -n "${RAILWAY_API_TOKEN:-}" ]; then
    echo -e "   • Railway services targeted: ${RAILWAY_SERVICES}"
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
    if [ "$PROD_WORKFLOW_OK" = true ] && [ -n "${RAILWAY_API_TOKEN:-}" ]; then
        echo -e "   • Watch Railway deploys finish (gateway first, then workers):"
        echo -e "     railway logs --service gateway"
        echo -e "   • Confirm Sentry events now tag release: ${DOCKER_RELEASE#v}@<sha>"
    else
        echo -e "   • Monitor the workflow runs:"
        echo -e "     - Dev workflow:  https://github.com/${REPO}/actions/workflows/publish-dev-docker.yml"
        echo -e "     - Prod workflow: https://github.com/${REPO}/actions/workflows/publish-prod-docker.yml"
        echo -e "   • Verify Docker images are published correctly"
        echo -e "   • Update Railway services manually in the dashboard"
    fi
    echo
fi

echo -e "${GREEN}✨ All done!${NC}"
