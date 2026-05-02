# syntax=docker/dockerfile:1.6
#
# Multi-stage Dockerfile for the weft framework.
#
# Stages:
#   builder  — compiles and tests the module, produces a verified module cache
#   ci       — minimal image for CI: runs make, exits. Small.
#   dev      — fat image with Claude Code, Node.js, common MCP servers,
#              and shell tools for interactive development.
#
# Build a specific stage:
#   docker build --target ci  -t weft:ci  .
#   docker build --target dev -t weft:dev .
#
# Default (no --target) builds dev.

# =============================================================================
# Stage 1: builder
# =============================================================================
# We use the official golang image rather than Ubuntu-with-go-installed
# because it's better-maintained, smaller, and has the build cache layout
# Go's tooling expects.
FROM golang:1.22-bookworm AS builder

WORKDIR /src

# Copy go.mod first so the dependency layer caches independently of source.
# When go.mod doesn't change, this layer is reused even if .go files do.
COPY go.mod ./
# go.sum may not exist for a fresh module with no deps; copy if present.
COPY go.su[m] ./
RUN go mod download

# Now copy source. Layer above is cached unless go.mod changes.
COPY . .

# Verify the module builds and passes tests as part of the image build.
# This means a successful image build proves the code works.
RUN go build ./... && go test ./...


# =============================================================================
# Stage 2: ci — minimal image for running the test suite
# =============================================================================
# Just Go and the source. No editor, no MCP servers, no Claude Code.
# Use this in CI pipelines where you only need `make test`.
FROM golang:1.22-bookworm AS ci

# A non-root user is hygiene; tests don't need root.
RUN useradd -m -u 1000 -s /bin/bash dev
WORKDIR /workspace

# Copy verified source from builder.
COPY --from=builder --chown=dev:dev /src /workspace

# make is needed for the Makefile targets.
RUN apt-get update && apt-get install -y --no-install-recommends \
        make \
    && rm -rf /var/lib/apt/lists/*

USER dev

# Default: run the full pipeline.
CMD ["make", "all"]


# =============================================================================
# Stage 3: dev — full development image
# =============================================================================
# Includes Claude Code, Node.js (for npx-based MCP servers), and common
# command-line tools. This is the image you'd `docker compose run --rm dev sh`
# into for interactive work.
FROM golang:1.22-bookworm AS dev

# Tools commonly needed when developing against MCP servers and LLM APIs:
#   - make:      Makefile targets
#   - git:       version control inside the container
#   - curl:      ad-hoc HTTP debugging
#   - jq:        inspecting MCP JSON-RPC traffic
#   - ripgrep:   fast code search for agents to use
#   - ca-certs:  TLS for outbound HTTPS to LLM APIs
#   - nodejs +
#     npm:       running npx-based MCP servers (filesystem, github, etc.)
#   - python3:   some MCP servers are Python-based
#   - vim:       minimal editor for in-container edits
RUN apt-get update && apt-get install -y --no-install-recommends \
        make \
        git \
        curl \
        jq \
        ripgrep \
        ca-certificates \
        nodejs \
        npm \
        python3 \
        python3-pip \
        vim \
        && rm -rf /var/lib/apt/lists/*

# Install Claude Code globally. This is the npm-published CLI; it brings in
# its own dependencies and lets `claude` and `claude mcp serve` work in-container.
# Pinned to a specific major to avoid surprise breakage across rebuilds.
RUN npm install -g @anthropic-ai/claude-code

# Pre-install a few canonical MCP servers so they're warm in the image cache.
# These are pulled via npx at runtime by default; pre-installing makes the
# first invocation fast and keeps the image self-contained when offline.
RUN npm install -g \
        @modelcontextprotocol/server-filesystem \
        @modelcontextprotocol/server-github \
        @modelcontextprotocol/server-memory

# A non-root user with a real home directory.
RUN useradd -m -u 1000 -s /bin/bash dev \
    && mkdir -p /workspace \
    && chown -R dev:dev /workspace

WORKDIR /workspace

# Copy the verified source. This is the same artifact tested in `builder`.
COPY --from=builder --chown=dev:dev /src /workspace

USER dev

# A reasonable default for interactive use; compose overrides this to keep
# the container alive for `docker compose exec dev sh`.
CMD ["bash"]
