# Copyright © 2025-2026 Apple Inc. and the container-builder-shim project authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#   https://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Usage: run `make help` to view available targets.

BINARY_NAME ?= container-builder-shim
BUILD_DIR   ?= bin
PKG         := ./...

GO          ?= go
HAWKEYE     ?= $(shell command -v hawkeye 2>/dev/null || printf '%s' .local/bin/hawkeye)
GIT_TAG     := $(shell git describe --tags --always --dirty)
GO_LDFLAGS  := -s -w -X main.VERSION=$(GIT_TAG)
GOFLAGS    ?= -ldflags="$(GO_LDFLAGS)"
IMAGE_TAG  ?= $(BINARY_NAME):$(GIT_TAG)
SOURCE_REPOSITORY ?= https://github.com/stephenlclarke/container-builder-shim

GOLANGCI_LINT_VERSION ?= v1.64.8
GOLANGCI_LINT ?= $(GO) run github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

-include Protobuf.Makefile

.DEFAULT_GOAL := build

$(BUILD_DIR):
	@mkdir -p $@

.PHONY: help
help:
	@printf "\033[1mAvailable targets:\033[0m\n"
	@awk 'BEGIN{FS=":.*##"} /^[a-zA-Z_-]+:.*##/{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST) | sort

.PHONY: build
build: $(BUILD_DIR)
	$(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) .

.PHONY: build-linux
build-linux: $(BUILD_DIR)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(GOFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-linux .

.PHONY: fmt
fmt:	go-fmt update-licenses

.PHONY: go-fmt
go-fmt:
	$(GO) fmt $(PKG)

.PHONY: vet
vet:
	$(GO) vet $(PKG)

.PHONY: lint
lint:
	$(GOLANGCI_LINT) run

.PHONY: tidy
tidy:
	$(GO) mod tidy

.PHONY: deps
deps: tidy
	$(GO) mod download

TEST_FLAGS ?= -v

.PHONY: test
test:
	$(GO) test $(TEST_FLAGS) $(PKG)

.PHONY: test-race
test-race:
	$(GO) test -race $(TEST_FLAGS) $(PKG)

.PHONY: coverage
coverage:
	$(GO) test -coverprofile=coverage.out $(PKG)
	$(GO) tool cover -func=coverage.out

.PHONY: docs
docs: ## Build the static DocC developer guide.
	./scripts/make-docs.sh _site api/container-builder-shim

.PHONY: serve-docs
serve-docs: docs ## Serve the DocC developer guide locally.
	python3 -m http.server 8000 --directory _site

.PHONY: protos
protos: proto-all

.PHONY: generate
generate: protos
	$(GO) generate $(PKG)

.PHONY: update-licenses
update-licenses:
	@echo Updating license headers...
	@HAWKEYE="$(HAWKEYE)" ./scripts/ensure-hawkeye-exists.sh
	@"$(HAWKEYE)" format --fail-if-unknown --fail-if-updated false

.PHONY: check-licenses
check-licenses:
	@echo Checking license headers existence in source files...
	@HAWKEYE="$(HAWKEYE)" ./scripts/ensure-hawkeye-exists.sh
	@"$(HAWKEYE)" check --fail-if-unknown

.PHONY: image
image: build-linux
	container build \
		--build-arg GIT_TAG=$(GIT_TAG) \
		--build-arg SOURCE_REPOSITORY=$(SOURCE_REPOSITORY) \
		-t $(IMAGE_TAG) .

.PHONY: release
release: fmt vet lint test image

.PHONY: clean
clean:
	$(GO) clean
	rm -rf $(BUILD_DIR) _site coverage.out
