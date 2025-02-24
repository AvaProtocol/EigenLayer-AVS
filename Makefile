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

## audit: run quality control checks
.PHONY: audit
audit:
	go mod verify
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest -checks=all,-ST1000,-U1000 ./...
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	go test -race -buildvcs -vet=off ./...


## test: run all tests
.PHONY: test
test:
	go test -v -race -buildvcs ./...

## test/cover: run all tests and display coverage
.PHONY: test/cover
test/cover:
	go test -v -race -buildvcs -coverprofile=/tmp/coverage.out ./...
	go tool cover -html=/tmp/coverage.out

## build: build the application
.PHONY: build
build:
	go build -o=/tmp/bin/${BINARY_NAME} ${MAIN_PACKAGE_PATH}

.PHONY: run
run: build
	/tmp/bin/${BINARY_NAME}


## push: push changes to the remote Git repository
.PHONY: push
push: tidy audit no-dirty
	git push

## production/deploy: deploy the application to production
.PHONY: production/deploy
production/deploy: confirm tidy audit no-dirty
	GOOS=linux GOARCH=amd64 go build -ldflags='-s' -o=/tmp/bin/linux_amd64/${BINARY_NAME} ${MAIN_PACKAGE_PATH}
	upx -5 /tmp/bin/linux_amd64/${BINARY_NAME}

## protoc-gen: generate protoc buf Go binding
.PHONY: protoc-gen
protoc-gen:
	protoc \
		--go_out=. \
		--go_opt=paths=source_relative \
    	--go-grpc_out=. \
		--go-grpc_opt=paths=source_relative \
    	protobuf/avs.proto

	protoc \
		--go_out=. \
		--go_opt=paths=source_relative \
    	--go-grpc_out=. \
		--go-grpc_opt=paths=source_relative \
    	protobuf/node.proto

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
		--build.cmd "make dev-build" --build.bin "./out/${BINARY_NAME}" --build.args_bin "aggregator" --build.delay "100" \
		--build.exclude_dir "certs,client-sdk,contracts,examples,out,docs,tmp" \
		--build.include_ext "go, tpl, tmpl, html, css, scss, js, ts, sql, jpeg, jpg, gif, png, bmp, svg, webp, ico" \
		--misc.clean_on_exit "true"

## dev-build: build a dev version for local development
dev-build:
	mkdir out || true
	go build \
		-o ./out/ap \
    	-ldflags "-X github.com/AvaProtocol/ap-avs/version.revision=$(shell  git rev-parse HEAD)"

## dev-agg: run aggregator locally with dev build
dev-agg:
	./out/ap aggregator
## dev-agg: run operator locally with dev build
dev-op:
	./out/ap operator --config=config/operator.yaml

## dev-clean: cleanup storage data
dev-clean:
	rm -rf /tmp/ap-avs /tmp/ap.sock
