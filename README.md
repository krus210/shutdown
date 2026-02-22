# shutdown

[![Go Reference](https://pkg.go.dev/badge/github.com/krus210/shutdown.svg)](https://pkg.go.dev/github.com/krus210/shutdown)
[![CI](https://github.com/krus210/shutdown/actions/workflows/ci.yml/badge.svg)](https://github.com/krus210/shutdown/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/krus210/shutdown)](https://goreportcard.com/report/github.com/krus210/shutdown)
[![Coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)](https://github.com/krus210/shutdown)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

Declarative graceful shutdown orchestrator for Go microservices.

Every Go microservice needs graceful shutdown. The standard approach is 50+ lines of boilerplate with `signal.NotifyContext`, `sync.WaitGroup`, and manual error handling scattered across `main()`. Existing libraries like [ory/graceful](https://github.com/ory/graceful) only work with HTTP servers. `shutdown` works with **any** component — HTTP, gRPC, database pools, message consumers, background workers, cron jobs.

## Features

- **Universal** — works with any component implementing a simple `Close(ctx) error` interface
- **Priority groups** — components with the same priority shut down in parallel; groups execute sequentially
- **Per-component timeouts** — override the global timeout for individual components
- **Thread-safe** — concurrent `Register` and `Shutdown` calls are safe
- **Idempotent** — calling `Shutdown` multiple times is safe; closers execute exactly once
- **Double-signal force exit** — first signal starts graceful shutdown, second forces `os.Exit(1)`
- **Structured logging** — built-in `log/slog` progress logging
- **Zero dependencies** — only Go standard library

## Installation

```
go get github.com/krus210/shutdown
```

Requires Go 1.21+ (for `log/slog`).

## Quick Start

```go
package main

import (
    "context"
    "log/slog"
    "net/http"
    "time"

    "github.com/krus210/shutdown"
)

func main() {
    httpServer := &http.Server{Addr: ":8080"}
    go httpServer.ListenAndServe()

    s := shutdown.New(shutdown.WithTimeout(15 * time.Second))

    s.RegisterFunc("http-server", httpServer.Shutdown)

    if err := s.Listen(context.Background()); err != nil {
        slog.Error("shutdown completed with errors", "error", err)
    }
}
```

## Priority Groups

Components with different priorities shut down sequentially (lowest first). Components with the same priority shut down in parallel.

```go
s := shutdown.New()

// Priority 0: stop accepting requests
s.RegisterFunc("http-server", httpServer.Shutdown, shutdown.WithPriority(0))
s.RegisterFunc("grpc-server", grpcServer.Shutdown, shutdown.WithPriority(0))

// Priority 1: drain in-flight work
s.RegisterFunc("worker", worker.Stop, shutdown.WithPriority(1))

// Priority 2: close infrastructure last
s.RegisterFunc("pg-pool", func(ctx context.Context) error {
    db.Close()
    return nil
}, shutdown.WithPriority(2))
```

Execution timeline:

```
Group 0:  |--- http ---|
          |--- grpc ---|
                        Group 1:  |--- worker ---|
                                                   Group 2:  |--- pg-pool ---|
```

If no priority is set, components shut down in registration order (each gets a unique auto-incremented priority).

## Timeouts

The global timeout (default: 30s, matching Kubernetes `terminationGracePeriodSeconds`) applies to the entire shutdown process. Per-component timeouts can make individual components fail faster, but never exceed the global timeout.

```go
s := shutdown.New(shutdown.WithTimeout(15 * time.Second))

// This component gets 5s max, even though global is 15s
s.RegisterFunc("cache", redisClient.Close,
    shutdown.WithComponentTimeout(5 * time.Second),
)

// This component inherits the global 15s timeout
s.RegisterFunc("database", func(ctx context.Context) error {
    db.Close()
    return nil
})
```

## Custom Logger

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
    Level: slog.LevelDebug,
}))

s := shutdown.New(shutdown.WithLogger(logger))
```

Example log output during shutdown:

```
level=INFO msg="shutdown started" timeout=15s
level=INFO msg="closing component" name=http-server priority=0
level=INFO msg="component closed" name=http-server elapsed=12ms
level=INFO msg="closing component" name=pg-pool priority=1
level=INFO msg="component closed" name=pg-pool elapsed=3ms
level=INFO msg="shutdown completed successfully" elapsed=18ms
```

## Error Callback

Handle individual component errors as they occur:

```go
s := shutdown.New(shutdown.WithOnError(func(name string, err error) {
    slog.Error("component shutdown failed", "name", name, "error", err)
    // Send to Sentry, increment metric, etc.
}))
```

All errors are also aggregated and returned from `Shutdown()` / `Listen()`.

## The Closer Interface

Any type implementing `Close(ctx context.Context) error` works directly:

```go
type Closer interface {
    Close(ctx context.Context) error
}
```

For types that don't match the interface, use `CloserFunc` or `RegisterFunc`:

```go
// pgxpool.Pool.Close() has no context and no error
s.RegisterFunc("pg-pool", func(ctx context.Context) error {
    pool.Close()
    return nil
})

// http.Server.Shutdown matches the Closer interface
s.Register("http", shutdown.CloserFunc(httpServer.Shutdown))
```

## Comparison

| Feature | shutdown | ory/graceful | Manual approach |
|---|---|---|---|
| Any component type | Yes | HTTP only | Yes |
| Priority ordering | Yes | No | Manual |
| Parallel within group | Yes | No | Manual |
| Per-component timeouts | Yes | No | Manual |
| Idempotent shutdown | Yes | No | Manual |
| Double-signal exit | Yes | No | Manual |
| Structured logging | Yes | No | Manual |
| Error aggregation | Yes | No | Manual |
| Lines of code needed | ~10 | ~5 | 50+ |

## API Reference

Full documentation is available on [pkg.go.dev](https://pkg.go.dev/github.com/krus210/shutdown).

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Add tests for new functionality
4. Ensure `go test -race ./...` passes
5. Submit a pull request

## License

[MIT](LICENSE)
