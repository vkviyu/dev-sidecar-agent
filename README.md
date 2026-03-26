# Dev-Sidecar Agent

> Give your AI coding assistant runtime superpowers.

Dev-Sidecar Agent is an [MCP](https://modelcontextprotocol.io/) (Model Context Protocol) server that runs as a sidecar process on your dev machine. It provides AI-powered IDEs (Cursor, Windsurf, Claude Code, etc.) with deep runtime observability: HTTP traffic capture, process debugging, log correlation, and traffic rewriting -- all through a standard MCP tool interface.

```
┌──────────────┐                        ┌──────────────────────────────┐
│   AI IDE     │   MCP (Streamable HTTP) │   Dev-Sidecar Agent          │
│  (Cursor,    │ ◄═════════════════════► │                              │
│   Windsurf)  │                         │  18 MCP Tools                │
└──────────────┘                         │  ├── HTTP Traffic Capture    │
                                         │  ├── Delve Runtime Debugger  │
┌──────────────┐    HTTP Proxy :8888     │  ├── Process Log Capture     │
│  Your App /  │ ──────────────────────► │  ├── Map Remote (URL Rewrite)│
│  Browser     │                         │  └── Web Dashboard           │
└──────────────┘                         └──────────────────────────────┘
```

## Features

### MITM HTTP/HTTPS Proxy

Full man-in-the-middle proxy powered by [goproxy](https://github.com/elazarl/goproxy). Captures complete request/response pairs including headers, body, and timing breakdown. **Streaming-aware**: SSE (`text/event-stream`) and chunked responses are forwarded in real-time with zero additional latency -- data is captured as it flows through, not buffered.

Each request is assigned a unique Trace-ID (without modifying request headers -- zero intrusion to your backend) for cross-layer correlation with application logs.

Timing instrumentation via `net/http/httptrace`:
- DNS resolution
- TCP connect
- TLS handshake
- Time to first byte (TTFB)
- Body transfer

### Delve Runtime Debugger

Non-invasive Go process debugging through [Delve](https://github.com/go-delve/delve) RPC:
- List all goroutines with status and code location
- Get full stack traces with function args and local variables
- Evaluate arbitrary Go expressions at runtime
- Set dynamic tracepoints (non-blocking breakpoints that capture state and continue)
- Attach to / detach from running processes

### Map Remote (URL Rewrite)

Charles Proxy-style URL rewriting at runtime. Redirect production API calls to local dev servers, point third-party services to mocks -- all with regex support and persistent rule storage.

### Process Management & Log Capture

Launch and manage your target process as a subprocess. Stdout/stderr are captured with automatic Trace-ID extraction (via regex matching `trace_id=xxx`, `x-dev-trace-id:xxx`, etc.) for log-to-request correlation. Also supports reading arbitrary log files from disk with glob patterns and regex filtering.

### Web Dashboard

Built-in dark-themed web dashboard at `http://localhost:3000/` with:
- **Traffic** tab: HTTP request/response list with Reqable-style split-pane detail view (headers, body with format detection)
- **Logs** tab: Real-time subprocess stdout/stderr
- **Map Remote** tab: Rule CRUD + hit records
- **Marked** tab: Cross-tab bookmarking of important records

## Quick Start

### Prerequisites

- Go 1.21+

### Build

```bash
git clone https://github.com/user/dev-sidecar-agent.git
cd dev-sidecar-agent
go build -o dev-sidecar-agent .
```

### Run

```bash
# Basic: just the proxy + MCP server
./dev-sidecar-agent

# With a target process
./dev-sidecar-agent -config config.yaml

# With Delve debugging enabled
./dev-sidecar-agent -config config.yaml -debug
```

### Connect Your AI IDE

Add the MCP server to your IDE configuration:

**Cursor** (`~/.cursor/mcp.json`):
```json
{
  "mcpServers": {
    "dev-sidecar-agent": {
      "url": "http://localhost:3000/mcp"
    }
  }
}
```

**Claude Code:**
```bash
claude mcp add dev-sidecar-agent http://localhost:3000/mcp
```

### Configure Your App to Use the Proxy

Set the HTTP proxy environment variables before starting your application:

```bash
export HTTP_PROXY=http://localhost:8888
export HTTPS_PROXY=http://localhost:8888
```

Or let the agent manage your process directly via `config.yaml`:

```yaml
app:
  command: "./my-service --port=8080"
```

## Configuration

All settings have sensible defaults. Create a `config.yaml` to override:

```yaml
mcp:
  port: "3000"          # MCP + Web Dashboard port

proxy:
  port: "8888"          # MITM proxy port

app:
  command: ""            # Target process command (empty = don't launch)
  debug: false           # Enable Delve debugger

delve:
  listen_addr: "127.0.0.1:2345"  # Delve RPC address

storage:
  traffic_limit: 1000   # HTTP traffic ring buffer capacity
  log_limit: 10000      # Log ring buffer capacity
  marked_limit: 500     # Marked records capacity
```

## MCP Tools Reference

18 tools organized in 5 categories:

| Category | Tools | Description |
|----------|-------|-------------|
| **Traffic** | `get_http_traffic`, `get_map_remote_hits` | Query captured HTTP traffic and Map Remote hit records |
| **Map Remote** | `list_map_remote`, `add_map_remote`, `update_map_remote`, `toggle_map_remote`, `remove_map_remote` | Runtime URL rewrite rule management |
| **Logs** | `get_app_stdout`, `read_log_files` | Process output and disk log file reading |
| **Delve Debugger** | `attach_process`, `detach_process`, `list_goroutines`, `get_stack_trace`, `eval_variable`, `set_tracepoint`, `clear_tracepoint` | Non-invasive Go runtime debugging |
| **Marked Records** | `get_marked_records`, `add_marked_record`, `remove_marked_record` | Bookmark important records for AI analysis |

See [tools-capability.md](tools-capability.md) for detailed parameter documentation.

## Architecture

```
dev-sidecar-agent/
├── main.go              # MITM proxy, data models, TrafficStore, MapRemoteEngine
├── mcp.go               # MCP server setup, 18 tool handlers
├── web.go               # REST API + static file server for Web Dashboard
├── config.go            # YAML config loading with defaults
├── process.go           # Subprocess management, stdout/stderr capture
├── delve_tracer.go      # Delve RPC client, goroutine/stack/eval/tracepoint
├── config.yaml          # User configuration
├── map_remote.json      # Persistent Map Remote rules
└── web/
    ├── index.html       # Dashboard HTML
    ├── app.js           # Dashboard logic
    └── style.css        # Dark theme styles
```

### Key Design Decisions

- **Streaming proxy**: Response bodies are never buffered. A `recordingBody` wrapper streams data through to the client while capturing a copy, enabling real-time SSE/chunked proxy with zero latency overhead.
- **Zero-intrusion tracing**: Trace-IDs are generated internally without modifying HTTP request headers -- your backend sees identical traffic with or without the proxy.
- **Ring buffer storage**: All stores (traffic, logs, marked records) use fixed-capacity ring buffers with automatic eviction of oldest records.
- **httptrace instrumentation**: Precise timing breakdown (DNS/Connect/TLS/TTFB/Transfer) via `net/http/httptrace.ClientTrace` hooks injected into the proxy's Transport.

## HTTPS Certificate

On first run, the agent exports its CA certificate to `ca.crt` in the working directory. To capture HTTPS traffic, trust this certificate:

**macOS:**
```bash
sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain ca.crt
```

**Linux:**
```bash
sudo cp ca.crt /usr/local/share/ca-certificates/dev-sidecar-agent.crt
sudo update-ca-certificates
```

For Go applications using the proxy, you can also set:
```bash
export SSL_CERT_FILE=./ca.crt
```

## License

[MIT](LICENSE)
