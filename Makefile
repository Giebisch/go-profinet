# HELP
# This will output the help for each task
# thanks to https://marmelab.com/blog/2016/02/29/auto-documented-makefile.html

.DEFAULT_GOAL := help

.PHONY: help all build examples fmt fmt-check lint lint-ci vet vuln test test-e2e coverage cover coverage-check bench fuzz check clean generate generate-check coverpkg-check l2-test interop interop-images test-interop interop-matrix cross

help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Parent otfabric/go.work does not list this module; isolate toolchain commands.
export GOWORK := off

# CI formats and lints with the Go version from go.mod, not with the toolchain installed here.
GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

BIN_DIR := bin
# Packages whose tests run by default (examples are run by make examples).
TEST_PKGS := $(shell go list ./... | grep -v '/examples')
# Packages whose coverage counts, including what the e2e suite exercises.
# The raw-socket backends (l2/afpacket, l2/bpf, l2/npcap) need privileges
# and a platform: make l2-test and the L2 workflow measure them.
COVER_PKGS := $(shell go list ./... | grep -v '/examples' | grep -v '/internal/cmd' | grep -v '/interop' | grep -v '/e2e' | grep -v '/l2/afpacket' | grep -v '/l2/bpf' | grep -v '/l2/npcap' | paste -sd, -)
# The minimum total statement coverage (make coverage-check, CI).
COVER_MIN := 90

all: build ## Default target: build the examples

build: ## Build the examples into ./bin
	@mkdir -p $(BIN_DIR)
	@for dir in examples/*/; do \
		name="$$(basename "$$dir")"; \
		go build -o "$(BIN_DIR)/example-$$name" "./$$dir"; \
	done

# The self-contained examples: each starts both sides on a virtual network and exits by itself.
RUN_EXAMPLES := quickstart discovery alarms gsdml records topology chaos gsdmlimport

examples: ## Run the self-contained examples (each must exit successfully)
	@for e in $(RUN_EXAMPLES); do \
		echo "Running example $$e"; \
		go run ./examples/$$e >/dev/null || { echo "example $$e failed"; exit 1; }; \
	done

fmt: ## Format Go code with the gofmt of the Go version in go.mod (what CI uses)
	@$(GOFMT_CI) -w .

fmt-check: ## Fail if the go.mod-version gofmt or the local gofmt would change any file
	@ci="$$($(GOFMT_CI) -l .)"; loc="$$(gofmt -l .)"; \
	if [ -n "$$ci$$loc" ]; then \
		[ -z "$$ci" ] || { echo "Not formatted for gofmt go$(GO_MOD_VERSION) (CI):"; echo "$$ci"; }; \
		[ -z "$$loc" ] || { echo "Not formatted for the local gofmt:"; echo "$$loc"; }; \
		exit 1; \
	fi

lint: ## Run staticcheck
	@staticcheck ./...

lint-ci: ## Run golangci-lint (see .golangci.yml)
	@golangci-lint run ./...

vet: ## Run go vet on every supported OS (the raw-socket backends are per OS)
	@for os in linux darwin windows freebsd; do GOOS=$$os go vet ./... || exit 1; done

vuln: ## Run govulncheck
	@govulncheck ./...

test: ## Run the tests with the race detector
	@go test -count=1 -race $(TEST_PKGS)

test-e2e: ## Run the end-to-end suite on the virtual network (race detector, three chaos seeds)
	@go test -count=1 -race -v ./e2e/...

coverage: ## Run tests with coverage across the library packages (writes coverage.out)
	@go test -count=1 -race -covermode=atomic -coverpkg=$(COVER_PKGS) -coverprofile=coverage.out $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -1

coverage-check: coverage ## Fail when total coverage is below COVER_MIN
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub(/%/,"",$$3); print $$3}'); \
	echo "total coverage $$total% (minimum $(COVER_MIN)%)"; \
	awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { exit (t+0 < m+0) }' || { echo "coverage below $(COVER_MIN)%"; exit 1; }

cover: coverage ## Open coverage report in browser
	@go tool cover -html=coverage.out

bench: ## Run the codec and cyclic benchmarks
	@go test -count=1 -run='^$$' -bench=. -benchmem ./ethernet ./dcp ./lldp ./rpc ./pnio ./rt ./alarm ./gsdml ./vnet ./l2

# Duration each fuzz target runs. Override with FUZZTIME=... (e.g. FUZZTIME=5m).
FUZZTIME ?= 30s
fuzz: ## Run every fuzz target for FUZZTIME each (default 30s)
	@for pkg in $$(go list ./...); do \
		for target in $$(go test -list '^Fuzz' $$pkg 2>/dev/null | grep '^Fuzz'); do \
			echo "Fuzzing $$pkg $$target ($(FUZZTIME))"; \
			go test -run='^$$' -fuzz="^$$target\$$" -fuzztime=$(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

generate: ## Generate the GSDML types and SCHEMA.md from gsdml/schema/catalog.json
	@go generate ./gsdml

generate-check: ## Fail when the generated files differ from what the catalog generates
	@go run ./internal/cmd/gengsdml -check

coverpkg-check: ## Fail when CI's coverpkg (.github/workflows/ci.yml) is not COVER_PKGS
	@grep -qF -- "$(COVER_PKGS)" .github/workflows/ci.yml || { echo "coverpkg in .github/workflows/ci.yml must be: $(COVER_PKGS)"; exit 1; }

l2-test: ## Run the real Layer 2 tests on a veth pair in a privileged container (needs Docker)
	@scripts/l2-test.sh

# Reference implementations for 'make interop': otfabric/profinet-interop.
# Its images are built locally (p-net and profinet-py are GPL and are not
# published): make interop-images clones profinet-interop at
# INTEROP_REF into PROFINET_INTEROP_DIR, unless that directory exists,
# and builds every adapter image as profinet-interop-<adapter>:dev.
INTEROP_REPO          ?= https://github.com/otfabric/profinet-interop.git
INTEROP_REF           ?= v0.1.0
PROFINET_INTEROP_DIR  ?= ../profinet-interop
PROFINET_INTEROP_RESULTS ?= interop-results

interop-images: ## Build the profinet-interop reference images (needs Docker)
	@if [ ! -d "$(PROFINET_INTEROP_DIR)" ]; then \
		git clone --quiet "$(INTEROP_REPO)" "$(PROFINET_INTEROP_DIR)" && \
		git -C "$(PROFINET_INTEROP_DIR)" checkout --quiet "$(INTEROP_REF)"; \
	fi
	@$(MAKE) -C "$(PROFINET_INTEROP_DIR)" images

test-interop: ## Run the interop suite against the profinet-interop images (needs Docker; make interop-images first)
	@mkdir -p "$(PROFINET_INTEROP_RESULTS)"
	PROFINET_INTEROP_RESULTS="$(abspath $(PROFINET_INTEROP_RESULTS))" \
		go test -tags=interop -count=1 -timeout=40m -v ./interop/...

interop: test-interop ## Alias for test-interop

interop-matrix: ## Regenerate profinet-interop's matrix from the results of make test-interop
	@$(MAKE) -C "$(PROFINET_INTEROP_DIR)" matrix RESULTS="$(abspath $(PROFINET_INTEROP_RESULTS))"

cross: ## Build every package for the supported platforms
	@for p in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64; do \
		echo "Building $$p"; GOOS=$${p%/*} GOARCH=$${p#*/} go build ./... || exit 1; \
	done

check: generate-check coverpkg-check fmt fmt-check lint lint-ci vet vuln test coverage-check examples ## Run everything CI runs, except the Docker-based suites

clean: ## Remove generated binaries and coverage artifacts
	@rm -rf $(BIN_DIR) coverage.out coverage.html
