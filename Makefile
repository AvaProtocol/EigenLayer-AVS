ROOT_DIR := $(shell pwd)

MAIN_PACKAGE_PATH ?= ./
BINARY_NAME ?= ap

# Version metadata stamped into the binary via -ldflags. Derived from git
# so /health and telemetry reflect the actual checkout instead of the
# stale default in version/version.go. Both fields fall back gracefully
# when git is missing or there are no tags (e.g. a CI shallow-clone).
#
# Override from the environment when needed: `make build SEMVER=4.0.0`.
SEMVER ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo dev)
REVISION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
VERSION_LDFLAGS := -X 'github.com/AvaProtocol/EigenLayer-AVS/version.semver=$(SEMVER)' -X 'github.com/AvaProtocol/EigenLayer-AVS/version.revision=$(REVISION)'

# ==================================================================================== #
# HELPERS
# ==================================================================================== #

## help: print this help message
.PHONY: help
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' Makefile | column -t -s ':' |  sed -e 's/^/ /'

.PHONY: confirm
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]

.PHONY: no-dirty
no-dirty:
	git diff --exit-code


## tidy: format code and tidy modfile
.PHONY: tidy
tidy:
	go fmt ./...
	go mod tidy -v

## audit: run quality control checks (excluding long-running integration tests)
.PHONY: audit
audit:
	go mod verify
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest -checks=all,-ST1000,-U1000 ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	golangci-lint run ./...
	go test -race -buildvcs -vet=off $(shell go list ./... | grep -v '/scripts$$')


## test: run all tests with coverage (excluding long-running integration tests)
.PHONY: test
test:
	go clean -testcache
	go clean -cache
	go mod tidy
	go build $(shell go list ./... | grep -v '/scripts$$')
	go test -v -race -buildvcs -coverprofile=/tmp/coverage.out $(shell go list ./... | grep -v '/scripts$$')
	go tool cover -html=/tmp/coverage.out

## test/integration: run long-running integration tests (usually failing, for debugging only)
.PHONY: test/integration
test/integration:
	@echo "⚠️  Running long-running integration tests that often fail..."
	@echo "⚠️  These are excluded from regular test runs and are for debugging purposes only"
	go clean -cache
	go mod tidy
	go build $(shell go list ./... | grep -v '/scripts$$')
	go test -v -race -buildvcs -tags=integration ./integration_test/ ./core/taskengine/...


## test/package: run tests for a specific package (usage: make test/package PKG=./core/taskengine)
.PHONY: test/package
test/package:
	go clean -testcache
	go clean -cache
	go mod tidy
	go build $(shell go list ./... | grep -v '/scripts$$')
	go test -v -race -buildvcs $(PKG)


## cicd-failed: print cleaned test failures from GitHub Actions logs (usage: make cicd-failed RUN_ID=123456789)
.PHONY: cicd-failed
cicd-failed:
ifndef RUN_ID
	$(error RUN_ID is not set. Usage: make cicd-failed RUN_ID=123456789)
endif
	@gh run view $(RUN_ID) --log | grep "FAIL:" | sed -E 's/^.*([ ]{4}--- FAIL:|--- FAIL:)/\1/'

## build-prod: build the application for production
.PHONY: build-prod
build-prod:
	go build -ldflags="$(VERSION_LDFLAGS)" -o=/tmp/bin/${BINARY_NAME} ${MAIN_PACKAGE_PATH}

.PHONY: run
run: build-prod
	/tmp/bin/${BINARY_NAME}


## push: push changes to the remote Git repository
.PHONY: push
push: tidy audit no-dirty
	git push

## production/deploy: deploy the application to production
.PHONY: production/deploy
production/deploy: confirm tidy audit no-dirty
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -X 'github.com/AvaProtocol/EigenLayer-AVS/version.semver=dev' -X 'github.com/AvaProtocol/EigenLayer-AVS/version.revision=dev'" -o=/tmp/bin/linux_amd64/${BINARY_NAME} ${MAIN_PACKAGE_PATH}
	upx -5 /tmp/bin/linux_amd64/${BINARY_NAME}

## protoc-gen: generate protoc buf Go binding
.PHONY: protoc-gen
protoc-gen:
	@echo "Ensuring output directory ./protobuf exists..."
	@mkdir -p ./protobuf
	@echo "Deleting old generated files from ./protobuf/ (if any)..."
	@rm -f ./protobuf/*.pb.go
	@echo "Generating Go bindings (expected into ./protobuf/ as package avsproto)..."
	@protoc \
		--proto_path=protobuf \
		--go_out=./protobuf \
		--go_opt=paths=source_relative \
    	--go-grpc_out=./protobuf \
		--go-grpc_opt=paths=source_relative \
    	protobuf/avs.proto protobuf/node.proto protobuf/worker.proto
	@echo "Protobuf Go generation complete. Files should be in ./protobuf/ and declare package avsproto."

## rest-gen: regenerate REST handler types + Echo ServerInterface from api/openapi.yaml
##
## Two outputs are produced under aggregator/rest/generated/:
##   - types.gen.go    — Go type definitions for every schema in the spec
##   - server.gen.go   — Echo ServerInterface (one method per spec operationId)
##
## REST handlers in aggregator/rest/handlers/ implement the ServerInterface;
## the compiler enforces that every spec route has a matching handler and
## that request/response types line up. Regenerate after editing the spec.
.PHONY: rest-gen
rest-gen:
	@echo "Ensuring output directory ./aggregator/rest/generated exists..."
	@mkdir -p ./aggregator/rest/generated
	@echo "Deleting old generated files..."
	@rm -f ./aggregator/rest/generated/*.gen.go
	@echo "Generating Go types from api/openapi.yaml..."
	@go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
		-config api/oapi-codegen-types.yaml api/openapi.yaml
	@echo "Generating Echo server interface from api/openapi.yaml..."
	@go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
		-config api/oapi-codegen-server.yaml api/openapi.yaml
	@echo "REST generation complete. Generated files in ./aggregator/rest/generated/."

## up: bring up docker compose stack
up:
	docker compose up

## unstable-build: generate an unstable for internal test
unstable-build:
	docker build --platform=linux/amd64 --build-arg RELEASE_TAG=unstable -t avaprotocol/avs-dev:unstable -f dockerfiles/operator.Dockerfile .
	docker push avaprotocol/avs-dev:unstable


## dev/live: run the application with reloading on file changes
.PHONY: dev-live
dev-live:
	go run github.com/air-verse/air@v1.61.1 \
		--build.cmd "make build" --build.bin "./out/${BINARY_NAME}" --build.args_bin "aggregator" --build.delay "100" \
		--build.exclude_dir "certs,client-sdk,contracts,examples,out,docs,tmp, migrations" \
		--build.include_ext "go, tpl, tmpl, html, css, scss, js, ts, sql, jpeg, jpg, gif, png, bmp, svg, webp, ico" \
		--misc.clean_on_exit "true"

## build: build the application for local development
.PHONY: build
build:
	mkdir out || true
	go build \
		-o ./out/ap \
		-ldflags "$(VERSION_LDFLAGS)"

## aggregator: show usage for aggregator commands
.PHONY: aggregator aggregator-sepolia aggregator-ethereum aggregator-base aggregator-base-sepolia
aggregator:
	@echo "Usage: make aggregator-<chain>"
	@echo ""
	@echo "Available commands:"
	@echo "  make aggregator-sepolia        - Start aggregator with Sepolia config"
	@echo "  make aggregator-ethereum       - Start aggregator with Ethereum config"
	@echo "  make aggregator-base           - Start aggregator with Base config"
	@echo "  make aggregator-base-sepolia   - Start aggregator with Base Sepolia config"
aggregator-sepolia: build
	@echo "🚀 Starting aggregator with Sepolia configuration..."
	@echo "📝 Logs will be written to aggregator-sepolia.log"
	./out/ap aggregator --config=config/aggregator-sepolia.yaml 2>&1 | tee aggregator-sepolia.log

aggregator-ethereum: build
	@echo "🚀 Starting aggregator with Ethereum configuration..."
	@echo "📝 Logs will be written to aggregator-ethereum.log"
	./out/ap aggregator --config=config/aggregator-ethereum.yaml 2>&1 | tee aggregator-ethereum.log

aggregator-base: build
	@echo "🚀 Starting aggregator with Base configuration..."
	@echo "📝 Logs will be written to aggregator-base.log"
	./out/ap aggregator --config=config/aggregator-base.yaml 2>&1 | tee aggregator-base.log

aggregator-base-sepolia: build
	@echo "🚀 Starting aggregator with Base Sepolia configuration..."
	@echo "📝 Logs will be written to aggregator-base-sepolia.log"
	./out/ap aggregator --config=config/aggregator-base-sepolia.yaml 2>&1 | tee aggregator-base-sepolia.log
## operator: show usage for operator commands
.PHONY: operator operator-sepolia operator-ethereum operator-base operator-base-sepolia
operator:
	@echo "Usage: make operator-<chain>"
	@echo ""
	@echo "Available commands:"
	@echo "  make operator-sepolia          - Start operator with Sepolia config"
	@echo "  make operator-ethereum         - Start operator with Ethereum config"
	@echo "  make operator-base             - Start operator with Base config"
	@echo "  make operator-base-sepolia     - Start operator with Base Sepolia config"
	@echo "  make operator-default          - Start operator with default config"

operator-sepolia: build
	@echo "🔧 Starting operator with Sepolia configuration..."
	@echo "📝 Logs will be written to operator-sepolia.log"
	./out/ap operator --config=config/operator-sepolia.yaml 2>&1 | tee operator-sepolia.log

operator-ethereum: build
	@echo "🔧 Starting operator with Ethereum configuration..."
	@echo "📝 Logs will be written to operator-ethereum.log"
	./out/ap operator --config=config/operator-ethereum.yaml 2>&1 | tee operator-ethereum.log

operator-base: build
	@echo "🔧 Starting operator with Base configuration..."
	@echo "📝 Logs will be written to operator-base.log"
	./out/ap operator --config=config/operator-base.yaml 2>&1 | tee operator-base.log

operator-base-sepolia: build
	@echo "🔧 Starting operator with Base Sepolia configuration..."
	@echo "📝 Logs will be written to operator-base-sepolia.log"
	./out/ap operator --config=config/operator-base-sepolia.yaml 2>&1 | tee operator-base-sepolia.log

operator-default: build
	@echo "🔧 Starting operator with default configuration..."
	@echo "📝 Logs will be written to operator-default.log"
	./out/ap operator --config=config/operator.yaml 2>&1 | tee operator-default.log


## dev-gateway: run the dev gateway (REST 8080, gRPC 2206)
.PHONY: dev-gateway dev-worker-sepolia dev-worker-base-sepolia dev-operator-sepolia
dev-gateway: build
	@mkdir -p logs
	@echo "🚀 Starting gateway (dev) — REST :8080, gRPC :2206"
	@echo "📝 Logs: logs/gateway.log"
	./out/ap aggregator --config=config/gateway-dev.yaml 2>&1 | tee logs/gateway.log

## dev-worker-sepolia: run the sepolia chain worker (gRPC 50051)
dev-worker-sepolia: build
	@mkdir -p logs
	@echo "🛠  Starting worker:sepolia (dev) — gRPC :50051"
	@echo "📝 Logs: logs/worker-sepolia.log"
	./out/ap worker --config=config/worker-sepolia-dev.yaml 2>&1 | tee logs/worker-sepolia.log

## dev-worker-base-sepolia: run the base-sepolia chain worker (gRPC 50052)
dev-worker-base-sepolia: build
	@mkdir -p logs
	@echo "🛠  Starting worker:base-sepolia (dev) — gRPC :50052"
	@echo "📝 Logs: logs/worker-base-sepolia.log"
	./out/ap worker --config=config/worker-base-sepolia-dev.yaml 2>&1 | tee logs/worker-base-sepolia.log

## dev-operator-sepolia: run the sepolia operator pointed at the dev gateway
dev-operator-sepolia: build
	@mkdir -p logs
	@echo "🔧 Starting operator:sepolia (dev) — aggregator: 127.0.0.1:2206"
	@echo "📝 Logs: logs/operator-sepolia.log"
	./out/ap operator --config=config/operator-sepolia.yaml 2>&1 | tee logs/operator-sepolia.log

## dev-stack: run gateway + sepolia worker + base-sepolia worker + sepolia operator together (Ctrl-C stops all)
##
## All four processes run as background children of this make recipe.
## Logs stream to logs/<service>.log. SIGINT (Ctrl-C) propagates to the
## whole process group via `kill 0`, so the entire stack tears down
## cleanly on exit. Use `tail -f logs/*.log` in a second terminal to
## watch everything live.
.PHONY: dev-stack
dev-stack: build
	@mkdir -p logs
	@echo "🚀 Starting dev stack: gateway + 2 workers + operator"
	@echo "   gateway      → REST :8080, gRPC :2206  (logs/gateway.log)"
	@echo "   worker:sep   → gRPC :50051            (logs/worker-sepolia.log)"
	@echo "   worker:bsep  → gRPC :50052            (logs/worker-base-sepolia.log)"
	@echo "   operator:sep → 127.0.0.1:2206         (logs/operator-sepolia.log)"
	@echo ""
	@echo "   Tail with:  tail -f logs/*.log"
	@echo "   Stop with:  Ctrl-C  (kills the whole stack)"
	@echo ""
	@set -m; \
		trap 'echo; echo "🛑 Stopping dev stack..."; kill 0 2>/dev/null; exit 0' INT TERM; \
		./out/ap worker --config=config/worker-sepolia-dev.yaml      > logs/worker-sepolia.log      2>&1 & \
		./out/ap worker --config=config/worker-base-sepolia-dev.yaml > logs/worker-base-sepolia.log 2>&1 & \
		sleep 1; \
		./out/ap aggregator --config=config/gateway-dev.yaml         > logs/gateway.log             2>&1 & \
		sleep 3; \
		./out/ap operator   --config=config/operator-sepolia.yaml    > logs/operator-sepolia.log    2>&1 & \
		wait

## clean: cleanup storage data
clean:
	rm -rf /tmp/ap-avs /tmp/ap.sock

## hetzner-snapshot: pull fresh BadgerDB tarballs from each Hetzner aggregator
##                   into ./donors/<chain>/db/. Brief docker-stop per chain.
##                   Pass CHAIN=sepolia (etc.) to limit scope.
.PHONY: hetzner-snapshot
hetzner-snapshot:
	@scripts/dev/snapshot-hetzner-donors.sh $(CHAIN)

## migration-rehearse: run the Hetzner->gateway merge tool sequentially
##                     against each donor in ./donors/, default dry-run.
##                     Pass APPLY=1 to actually write to the scratch
##                     gateway DB at ./tmp/rehearsal-gateway-db.
##                     Pass CHAIN=sepolia (etc.) to limit scope.
.PHONY: migration-rehearse
migration-rehearse:
	@scripts/dev/run-merge-rehearsal.sh $(if $(APPLY),--apply) $(CHAIN)

## dev-stack-rehearsal: start the dev gateway against the post-merge
##                      scratch DB at ./tmp/rehearsal-gateway-db. Use
##                      after `make migration-rehearse APPLY=1` to
##                      validate read-path queries via the SDK.
.PHONY: dev-stack-rehearsal
dev-stack-rehearsal: build
	@mkdir -p logs
	@echo "🚀 Starting rehearsal gateway against ./tmp/rehearsal-gateway-db"
	@echo "   gateway      → REST :8080, gRPC :2206  (logs/gateway-rehearsal.log)"
	@echo "   worker:sep   → gRPC :50051            (logs/worker-sepolia.log)"
	@echo "   worker:bsep  → gRPC :50052            (logs/worker-base-sepolia.log)"
	@echo ""
	@echo "   Validate with the ava-sdk-js CLI:"
	@echo "     cd ../ava-sdk-js/examples"
	@echo "     yarn start listWorkflows           # expect post-merge workflow count"
	@echo "     yarn start getWorkflow <task_id>   # ground-truth single-task fetch"
	@echo ""
	@set -m; \
		trap 'echo; echo "🛑 Stopping rehearsal stack..."; kill 0 2>/dev/null; exit 0' INT TERM; \
		./out/ap worker --config=config/worker-sepolia-dev.yaml      > logs/worker-sepolia.log      2>&1 & \
		./out/ap worker --config=config/worker-base-sepolia-dev.yaml > logs/worker-base-sepolia.log 2>&1 & \
		sleep 1; \
		./out/ap aggregator --config=config/gateway-dev-rehearsal.yaml > logs/gateway-rehearsal.log 2>&1 & \
		wait
