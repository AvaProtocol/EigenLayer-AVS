ROOT_DIR := $(shell pwd)

MAIN_PACKAGE_PATH ?= ./
BINARY_NAME ?= ap

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
	go test -race -buildvcs -vet=off ./...


## test: run all tests excluding long-running integration tests
.PHONY: test
test:
	go clean -cache
	go mod tidy
	go build ./...
	go test -v -race -buildvcs ./...

## test/cover: run all tests and display coverage excluding long-running integration tests
.PHONY: test/cover
test/cover:
	go clean -cache
	go mod tidy
	go build ./...
	go test -v -race -buildvcs -coverprofile=/tmp/coverage.out ./...
	go tool cover -html=/tmp/coverage.out

## test/quick: run tests without race detection for faster feedback during development
.PHONY: test/quick
test/quick:
	go test -v ./...

## test/integration: run long-running integration tests (usually failing, for debugging only)
.PHONY: test/integration
test/integration:
	@echo "⚠️  Running long-running integration tests that often fail..."
	@echo "⚠️  These are excluded from regular test runs and are for debugging purposes only"
	go clean -cache
	go mod tidy
	go build ./...
	go test -v -race -buildvcs -tags=integration ./integration_test/

## test/all: run all tests including integration tests (not recommended for CI)
.PHONY: test/all
test/all:
	@echo "⚠️  Running ALL tests including long-running integration tests..."
	go clean -cache
	go mod tidy
	go build ./...
	go test -v -race -buildvcs -tags=integration ./...

## test/package: run tests for a specific package (usage: make test/package PKG=./core/taskengine)
.PHONY: test/package
test/package:
	go clean -cache
	go mod tidy
	go build ./...
	go test -v -race -buildvcs $(PKG)

## test/clean: run tests with clean cache to ensure consistency
.PHONY: test/clean
test/clean:
	go clean -testcache
	go clean -cache
	go mod tidy
	go build ./...
	go test -v -race -buildvcs ./...

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
	go build -ldflags="-X 'github.com/AvaProtocol/EigenLayer-AVS/version.semver=dev' -X 'github.com/AvaProtocol/EigenLayer-AVS/version.revision=dev'" -o=/tmp/bin/${BINARY_NAME} ${MAIN_PACKAGE_PATH}

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
    	protobuf/avs.proto protobuf/node.proto
	@echo "Protobuf Go generation complete. Files should be in ./protobuf/ and declare package avsproto."

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
    	-ldflags "-X github.com/AvaProtocol/EigenLayer-AVS/version.revision=$(shell  git rev-parse HEAD)"

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
	./out/ap aggregator --config=config/aggregator-sepolia.yaml

aggregator-ethereum: build
	@echo "🚀 Starting aggregator with Ethereum configuration..."
	./out/ap aggregator --config=config/aggregator-ethereum.yaml

aggregator-base: build
	@echo "🚀 Starting aggregator with Base configuration..."
	./out/ap aggregator --config=config/aggregator-base.yaml

aggregator-base-sepolia: build
	@echo "🚀 Starting aggregator with Base Sepolia configuration..."
	./out/ap aggregator --config=config/aggregator-base-sepolia.yaml
## operator: show usage for operator commands
.PHONY: operator operator-sepolia operator-ethereum operator-base
operator:
	@echo "Usage: make operator-<chain>"
	@echo ""
	@echo "Available commands:"
	@echo "  make operator-sepolia          - Start operator with Sepolia config"
	@echo "  make operator-ethereum         - Start operator with Ethereum config"
	@echo "  make operator-base             - Start operator with Base config"
	@echo "  make operator-default          - Start operator with default config"

operator-sepolia: build
	@echo "🔧 Starting operator with Sepolia configuration..."
	./out/ap operator --config=config/operator-sepolia.yaml

operator-ethereum: build
	@echo "🔧 Starting operator with Ethereum configuration..."
	./out/ap operator --config=config/operator-ethereum.yaml

operator-base: build
	@echo "🔧 Starting operator with Base configuration..."
	./out/ap operator --config=config/operator-base.yaml

operator-default: build
	@echo "🔧 Starting operator with default configuration..."
	./out/ap operator --config=config/operator.yaml


## dev-clean: cleanup storage data
dev-clean:
	rm -rf /tmp/ap-avs /tmp/ap.sock
