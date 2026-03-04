#!/bin/bash
# Railway project setup for gateway + worker architecture
#
# Prerequisites:
#   - Railway CLI installed: brew install railway
#   - Logged in: railway login
#   - Project created on Railway dashboard and linked: railway link
#
# This script creates the 3 services and configures them.
# Run once after creating the Railway project.
#
# Usage: ./scripts/railway-setup.sh

set -euo pipefail

echo "=== Railway Gateway + Worker Setup ==="
echo ""

# Verify we're linked to a project
if ! railway status &>/dev/null; then
    echo "Error: No Railway project linked."
    echo "  1. Create a project on https://railway.com/dashboard"
    echo "  2. Run: railway link"
    exit 1
fi

echo "Linked to Railway project. Creating services..."
echo ""

# --- Helper ---
create_service() {
    local name="$1"
    local start_cmd="$2"
    local health_path="$3"
    local health_port="$4"

    echo "Creating service: $name"
    echo "  Start command: $start_cmd"
    echo "  Health check: $health_path on port $health_port"

    # Create the service (Railway CLI)
    railway service create "$name" 2>/dev/null || echo "  (service may already exist)"

    echo "  Done."
    echo ""
}

# --- Create services ---
create_service "gateway" \
    "./ap aggregator --config=config/gateway-railway.yaml" \
    "/up" "8080"

create_service "worker-sepolia" \
    "./ap worker --config=config/worker-sepolia-railway.yaml" \
    "/health" "8080"

create_service "worker-base-sepolia" \
    "./ap worker --config=config/worker-base-sepolia-railway.yaml" \
    "/health" "8080"

echo "=== Services created ==="
echo ""
echo "Manual steps remaining (Railway dashboard):"
echo ""
echo "1. For each service, go to Settings → Deploy:"
echo "   - Set 'Custom Start Command' (see above)"
echo "   - Set 'Branch' to: feature/railway-migration-phase1-testnet"
echo ""
echo "2. For each service, go to Settings → Networking:"
echo "   - Workers: enable private networking (port 50051)"
echo "   - Gateway: enable private networking + public domain (ports 2206, 8080)"
echo ""
echo "3. For gateway, go to Settings → Volumes:"
echo "   - Add volume mounted at /data"
echo ""
echo "4. For each service, set Health Check:"
echo "   - Gateway:             path=/up      port=8080"
echo "   - worker-sepolia:      path=/health  port=8080"
echo "   - worker-base-sepolia: path=/health  port=8080"
echo ""
echo "5. Deploy workers first, then gateway."
echo ""
echo "Service DNS (private networking):"
echo "  worker-sepolia.railway.internal:50051"
echo "  worker-base-sepolia.railway.internal:50051"
