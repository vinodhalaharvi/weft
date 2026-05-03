# cmd/discover-mcp — what's in there?

A standalone tool to inspect any MCP stdio server's tool surface. Used
as a discovery step before building a real weft integration against
that server.

## Usage

```bash
# Default: try `claude mcp serve`
go run ./cmd/discover-mcp

# Any other MCP server:
go run ./cmd/discover-mcp -- npx @modelcontextprotocol/server-filesystem /tmp
```

## What it prints

- Server name and version
- Protocol version negotiated
- Which capabilities the server has (tools, resources, prompts)
- Each tool's name, description, and input JSON schema

## Why it's standalone

This program is the chicken-and-egg solution: we need to know what
MCP servers actually expose before designing weft's stdio transport
integration. Running this tool against your real `claude mcp serve`
(or any other MCP server) gives us the ground truth.

It uses `mark3labs/mcp-go` directly. Once we know the tool surface,
weft will gain an `mcp.Stdio(...)` transport that wraps mcp-go,
and we'll write a typed example using one of the discovered tools.

## Note on Go version

This subdirectory pulls in `mark3labs/mcp-go`, which requires Go 1.23+.
That's why the module's `go.mod` declares `go 1.23`. The rest of weft
also compiles on Go 1.23+, no other dependencies have changed.
