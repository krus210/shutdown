package shutdown

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func newTestManager(opts ...Option) *Manager {
	defaults := []Option{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	return New(append(defaults, opts...)...)
}

func TestCloserFunc(t *testing.T) {
	called := false
	var cf CloserFunc = func(ctx context.Context) error {
		called = true
		return nil
	}

	// Verify CloserFunc implements Closer.
	var c Closer = cf
	err := c.Close(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("CloserFunc was not called")
	}
}

func TestRegistrationOrder(t *testing.T) {
	mgr := newTestManager()

	var order []string
	var mu sync.Mutex
	for _, name := range []string{"first", "second", "third"} {
		n := name
		mgr.RegisterFunc(n, func(ctx context.Context) error {
			mu.Lock()
			order = append(order, n)
			mu.Unlock()
			return nil
		})
	}

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Each component gets unique auto-priority, so they execute sequentially.
	expected := []string{"first", "second", "third"}
	if len(order) != len(expected) {
		t.Fatalf("got %d components, want %d", len(order), len(expected))
	}
	for i, name := range expected {
		if order[i] != name {
			t.Errorf("order[%d] = %q, want %q", i, order[i], name)
		}
	}
}

func TestExplicitPriority(t *testing.T) {
	mgr := newTestManager()

	var order []string
	var mu sync.Mutex

	record := func(name string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	// Register in reverse order but with explicit priorities.
	mgr.RegisterFunc("db", record("db"), WithPriority(2))
	mgr.RegisterFunc("worker", record("worker"), WithPriority(1))
	mgr.RegisterFunc("http", record("http"), WithPriority(0))

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"http", "worker", "db"}
	if len(order) != len(expected) {
		t.Fatalf("got %d components, want %d", len(order), len(expected))
	}
	for i, name := range expected {
		if order[i] != name {
			t.Errorf("order[%d] = %q, want %q", i, order[i], name)
		}
	}
}

func TestMixedPriority(t *testing.T) {
	mgr := newTestManager()

	var order []string
	var mu sync.Mutex

	record := func(name string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	// auto-0, explicit-0, auto-2
	mgr.RegisterFunc("auto-first", record("auto-first"))                        // auto priority 0
	mgr.RegisterFunc("explicit-zero", record("explicit-zero"), WithPriority(0)) // explicit 0
	mgr.RegisterFunc("auto-third", record("auto-third"))                        // auto priority 2

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Priority 0 group: "auto-first" and "explicit-zero" run in parallel.
	// Priority 2 group: "auto-third" runs after.
	if len(order) != 3 {
		t.Fatalf("got %d components, want 3", len(order))
	}
	// The last one must be "auto-third" since it has priority 2.
	if order[2] != "auto-third" {
		t.Errorf("last component = %q, want %q", order[2], "auto-third")
	}
	// First two are priority 0 — order between them is not guaranteed (parallel).
	prio0 := map[string]bool{order[0]: true, order[1]: true}
	if !prio0["auto-first"] || !prio0["explicit-zero"] {
		t.Errorf("priority 0 group = %v, want {auto-first, explicit-zero}", prio0)
	}
}

func TestParallelWithinGroup(t *testing.T) {
	mgr := newTestManager()

	const sleepDuration = 100 * time.Millisecond

	for i := 0; i < 3; i++ {
		mgr.RegisterFunc(fmt.Sprintf("comp-%d", i), func(ctx context.Context) error {
			time.Sleep(sleepDuration)
			return nil
		}, WithPriority(0))
	}

	start := time.Now()
	err := mgr.Shutdown(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// If parallel, total time ≈ 100ms. If sequential, ≈ 300ms.
	if elapsed > 250*time.Millisecond {
		t.Errorf("shutdown took %v, expected ~%v (parallel execution)", elapsed, sleepDuration)
	}
}

func TestSequentialGroups(t *testing.T) {
	mgr := newTestManager()

	var t0, t1 time.Time

	mgr.RegisterFunc("first", func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		t0 = time.Now()
		return nil
	}, WithPriority(0))

	mgr.RegisterFunc("second", func(ctx context.Context) error {
		t1 = time.Now()
		return nil
	}, WithPriority(1))

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !t1.After(t0) {
		t.Errorf("second group started at %v, before first group finished at %v", t1, t0)
	}
}

func TestComponentTimeout(t *testing.T) {
	mgr := newTestManager(WithTimeout(5 * time.Second))

	mgr.RegisterFunc("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, WithComponentTimeout(50*time.Millisecond))

	start := time.Now()
	err := mgr.Shutdown(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("shutdown took %v, expected ~50ms", elapsed)
	}
}

func TestGlobalTimeout(t *testing.T) {
	mgr := newTestManager(WithTimeout(100 * time.Millisecond))

	mgr.RegisterFunc("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	start := time.Now()
	err := mgr.Shutdown(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("shutdown took %v, expected ~100ms", elapsed)
	}
}

func TestGlobalTimeoutShorterThanComponent(t *testing.T) {
	mgr := newTestManager(WithTimeout(50 * time.Millisecond))

	mgr.RegisterFunc("slow", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, WithComponentTimeout(1*time.Second))

	start := time.Now()
	err := mgr.Shutdown(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Global timeout (50ms) should win over component timeout (1s).
	if elapsed > 500*time.Millisecond {
		t.Errorf("shutdown took %v, expected ~50ms", elapsed)
	}
}

func TestErrorAggregation(t *testing.T) {
	mgr := newTestManager()

	errA := errors.New("error-a")
	errB := errors.New("error-b")

	mgr.RegisterFunc("a", func(ctx context.Context) error { return errA }, WithPriority(0))
	mgr.RegisterFunc("b", func(ctx context.Context) error { return nil }, WithPriority(0))
	mgr.RegisterFunc("c", func(ctx context.Context) error { return errB }, WithPriority(0))

	err := mgr.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errA) {
		t.Errorf("expected error to contain errA, got: %v", err)
	}
	if !errors.Is(err, errB) {
		t.Errorf("expected error to contain errB, got: %v", err)
	}
}

func TestIdempotentShutdown(t *testing.T) {
	mgr := newTestManager()

	var callCount atomic.Int32

	mgr.RegisterFunc("comp", func(ctx context.Context) error {
		callCount.Add(1)
		return nil
	})

	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = mgr.Shutdown(context.Background())
		}(i)
	}
	wg.Wait()

	if callCount.Load() != 1 {
		t.Errorf("closer called %d times, want 1", callCount.Load())
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("errs[%d] = %v, want nil", i, err)
		}
	}
}

func TestConcurrentRegisterAndShutdown(t *testing.T) {
	mgr := newTestManager()

	var wg sync.WaitGroup

	// 10 goroutines registering.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mgr.RegisterFunc(fmt.Sprintf("comp-%d", i), func(ctx context.Context) error {
				return nil
			})
		}(i)
	}

	// 3 goroutines calling Shutdown.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = mgr.Shutdown(context.Background())
		}()
	}

	wg.Wait()
}

func TestOnErrorCallback(t *testing.T) {
	var calledName string
	var calledErr error
	var mu sync.Mutex

	mgr := newTestManager(WithOnError(func(name string, err error) {
		mu.Lock()
		calledName = name
		calledErr = err
		mu.Unlock()
	}))

	expectedErr := errors.New("component failure")
	mgr.RegisterFunc("failing", func(ctx context.Context) error {
		return expectedErr
	})

	_ = mgr.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	if calledName != "failing" {
		t.Errorf("onError name = %q, want %q", calledName, "failing")
	}
	if !errors.Is(calledErr, expectedErr) {
		t.Errorf("onError err = %v, want %v", calledErr, expectedErr)
	}
}

func TestListenContextCancellation(t *testing.T) {
	mgr := newTestManager()

	var closed atomic.Bool
	mgr.RegisterFunc("comp", func(ctx context.Context) error {
		closed.Store(true)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.Listen(ctx)
	}()

	// Give Listen time to set up signal handling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return after context cancellation")
	}

	if !closed.Load() {
		t.Error("component was not closed")
	}
}

func TestListenSignal(t *testing.T) {
	mgr := newTestManager(WithSignals(syscall.SIGUSR1))

	var closed atomic.Bool
	mgr.RegisterFunc("comp", func(ctx context.Context) error {
		closed.Store(true)
		return nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.Listen(context.Background())
	}()

	// Give Listen time to set up signal handling.
	time.Sleep(50 * time.Millisecond)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("failed to send signal: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return after signal")
	}

	if !closed.Load() {
		t.Error("component was not closed")
	}
}

func TestDoubleSignalForceExit(t *testing.T) {
	if os.Getenv("TEST_DOUBLE_SIGNAL") == "1" {
		mgr := New(WithSignals(syscall.SIGUSR1))
		mgr.RegisterFunc("slow", func(ctx context.Context) error {
			time.Sleep(10 * time.Second)
			return nil
		})
		_ = mgr.Listen(context.Background())
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestDoubleSignalForceExit$")
	cmd.Env = append(os.Environ(), "TEST_DOUBLE_SIGNAL=1")
	err := cmd.Start()
	if err != nil {
		t.Fatalf("failed to start subprocess: %v", err)
	}

	// Wait for subprocess to be ready.
	time.Sleep(200 * time.Millisecond)

	// First signal: triggers graceful shutdown.
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("failed to send first signal: %v", err)
	}

	// Wait briefly then send second signal: forces exit.
	time.Sleep(200 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatalf("failed to send second signal: %v", err)
	}

	err = cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got: %v", err)
	}
	if exitErr.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", exitErr.ExitCode())
	}
}

func TestNoComponents(t *testing.T) {
	mgr := newTestManager()

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNilCloserPanics(t *testing.T) {
	mgr := newTestManager()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic, got none")
		}
		msg, ok := r.(string)
		if !ok || msg != "shutdown: closer must not be nil" {
			t.Errorf("unexpected panic message: %v", r)
		}
	}()

	mgr.Register("nil-closer", nil)
}

func TestShutdownWithCancelledContext(t *testing.T) {
	mgr := newTestManager(WithTimeout(5 * time.Second))

	mgr.RegisterFunc("comp", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := mgr.Shutdown(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGroupByPriority(t *testing.T) {
	components := []component{
		{name: "c", config: componentConfig{priority: 2}},
		{name: "a", config: componentConfig{priority: 0}},
		{name: "b", config: componentConfig{priority: 1}},
		{name: "d", config: componentConfig{priority: 0}},
	}

	groups := groupByPriority(components)

	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}

	// Group 0: priority 0.
	if len(groups[0]) != 2 {
		t.Fatalf("group 0 has %d components, want 2", len(groups[0]))
	}
	if groups[0][0].name != "a" || groups[0][1].name != "d" {
		t.Errorf("group 0 = [%s, %s], want [a, d]", groups[0][0].name, groups[0][1].name)
	}

	// Group 1: priority 1.
	if len(groups[1]) != 1 || groups[1][0].name != "b" {
		t.Errorf("group 1 = %v, want [b]", groups[1])
	}

	// Group 2: priority 2.
	if len(groups[2]) != 1 || groups[2][0].name != "c" {
		t.Errorf("group 2 = %v, want [c]", groups[2])
	}
}

func TestGroupByPriorityEmpty(t *testing.T) {
	groups := groupByPriority(nil)
	if groups != nil {
		t.Errorf("expected nil, got %v", groups)
	}
}

func TestMethodChaining(t *testing.T) {
	mgr := newTestManager()

	// Verify Register returns the manager for chaining.
	result := mgr.
		RegisterFunc("a", func(ctx context.Context) error { return nil }).
		RegisterFunc("b", func(ctx context.Context) error { return nil })

	if result != mgr {
		t.Error("method chaining returned different manager")
	}

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWithLogger(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	mgr := New(WithLogger(logger), WithTimeout(5*time.Second))
	mgr.RegisterFunc("comp", func(ctx context.Context) error {
		return nil
	})

	err := mgr.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	output := buf.String()
	for _, expected := range []string{"shutdown started", "closing component", "component closed", "shutdown completed"} {
		if !containsSubstring(output, expected) {
			t.Errorf("log output missing %q", expected)
		}
	}
}

// syncBuffer is a thread-safe bytes.Buffer for capturing log output.
type syncBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (b *syncBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
