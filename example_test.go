package shutdown_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/krus210/shutdown"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ExampleManager_basic() {
	mgr := shutdown.New(
		shutdown.WithTimeout(5*time.Second),
		shutdown.WithLogger(discardLogger()),
	)

	mgr.RegisterFunc("http-server", func(ctx context.Context) error {
		fmt.Println("http server stopped")
		return nil
	})

	mgr.RegisterFunc("database", func(ctx context.Context) error {
		fmt.Println("database connection closed")
		return nil
	})

	_ = mgr.Shutdown(context.Background())
	// Output:
	// http server stopped
	// database connection closed
}

func ExampleManager_priority() {
	mgr := shutdown.New(
		shutdown.WithTimeout(5*time.Second),
		shutdown.WithLogger(discardLogger()),
	)

	mgr.RegisterFunc("http-server", func(ctx context.Context) error {
		fmt.Println("1: http server stopped")
		return nil
	}, shutdown.WithPriority(0))

	mgr.RegisterFunc("worker", func(ctx context.Context) error {
		fmt.Println("2: worker stopped")
		return nil
	}, shutdown.WithPriority(1))

	mgr.RegisterFunc("database", func(ctx context.Context) error {
		fmt.Println("3: database closed")
		return nil
	}, shutdown.WithPriority(2))

	_ = mgr.Shutdown(context.Background())
	// Output:
	// 1: http server stopped
	// 2: worker stopped
	// 3: database closed
}

// DBConn demonstrates implementing the Closer interface.
type DBConn struct{}

// Close implements shutdown.Closer.
func (d *DBConn) Close(_ context.Context) error {
	fmt.Println("db connection closed")
	return nil
}

func ExampleManager_closerInterface() {
	mgr := shutdown.New(
		shutdown.WithTimeout(5*time.Second),
		shutdown.WithLogger(discardLogger()),
	)

	mgr.Register("database", &DBConn{})

	_ = mgr.Shutdown(context.Background())
	// Output:
	// db connection closed
}
