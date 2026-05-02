# weft — categorical composition primitives for Go
#
# Standard targets:
#   make            - build and test (default)
#   make build      - compile all packages
#   make test       - run all tests
#   make test-v     - run tests with verbose output
#   make test-race  - run tests with race detector
#   make cover      - generate coverage report
#   make bench      - run benchmarks
#   make lint       - run go vet and gofmt check
#   make fmt        - format all Go files
#   make tidy       - tidy go.mod
#   make clean      - remove build artifacts
#   make laws       - run only the categorical law tests
#   make example    - run only the end-to-end example tests

.PHONY: all build test test-v test-race cover bench lint fmt tidy clean laws example help

# Default target: verify everything works.
all: build test

# Compile every package without producing binaries.
build:
	@echo ">> building"
	go build ./...

# Run the full test suite.
test:
	@echo ">> testing"
	go test ./...

# Run tests with verbose per-test output.
test-v:
	@echo ">> testing (verbose)"
	go test -v ./...

# Run tests with the race detector enabled.
# This catches concurrent access bugs in Par, Traverse, etc.
test-race:
	@echo ">> testing with -race"
	go test -race ./...

# Generate coverage report.
# Open coverage.html in a browser to see uncovered lines.
cover:
	@echo ">> generating coverage"
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "open coverage.html"

# Run benchmarks if any are defined.
bench:
	@echo ">> running benchmarks"
	go test -bench=. -benchmem ./...

# Lint: go vet plus a gofmt check that fails if anything needs formatting.
lint:
	@echo ">> linting"
	go vet ./...
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "gofmt needs to run on:"; \
		gofmt -l .; \
		exit 1; \
	fi

# Format all Go files in place.
fmt:
	@echo ">> formatting"
	gofmt -w .

# Tidy go.mod.
tidy:
	@echo ">> tidying"
	go mod tidy

# Remove generated artifacts.
clean:
	@echo ">> cleaning"
	rm -f coverage.out coverage.html

# Run only the categorical law tests — the spec.
laws:
	@echo ">> running law tests"
	go test -v -run "Law|PreservesIdentity|PreservesComposition|EquivalentTo" ./weft/

# Run only the end-to-end example.
example:
	@echo ">> running end-to-end example"
	go test -v -run "EndToEnd" ./weft/

# --- Docker targets -----------------------------------------------------------

.PHONY: docker-ci docker-dev docker-shell docker-test docker-down

# Build and run the lean CI image. Exits with the test result.
docker-ci:
	@echo ">> docker: building and running ci image"
	docker compose run --rm ci

# Build the dev image and start it in the background.
docker-dev:
	@echo ">> docker: starting dev container"
	docker compose up -d --build dev

# Open a shell in the running dev container. Run `make docker-dev` first.
docker-shell:
	docker compose exec dev bash

# Run the test suite inside the dev container (assumes it's running).
docker-test:
	docker compose exec dev make test

# Stop and remove all containers.
docker-down:
	docker compose down

# Print available targets.
help:
	@echo "weft — available targets:"
	@echo ""
	@echo "Native Go targets:"
	@echo "  make            build and test (default)"
	@echo "  make build      compile all packages"
	@echo "  make test       run all tests"
	@echo "  make test-v     run tests with verbose output"
	@echo "  make test-race  run tests with race detector"
	@echo "  make cover      generate HTML coverage report"
	@echo "  make bench      run benchmarks"
	@echo "  make lint       go vet + gofmt check"
	@echo "  make fmt        format all Go files"
	@echo "  make tidy       tidy go.mod"
	@echo "  make clean      remove generated files"
	@echo "  make laws       run only categorical law tests"
	@echo "  make example    run only end-to-end example"
	@echo ""
	@echo "Docker targets:"
	@echo "  make docker-ci      build + test in lean CI image"
	@echo "  make docker-dev     build + start dev container in background"
	@echo "  make docker-shell   open a shell in running dev container"
	@echo "  make docker-test    run tests inside running dev container"
	@echo "  make docker-down    stop and remove all containers"
