// Package shutdown provides a declarative graceful shutdown orchestrator
// for Go microservices.
//
// It works with any component that implements the [Closer] interface —
// HTTP servers, gRPC servers, database pools, message consumers, background workers,
// and more.
//
// Components are organized into priority groups. Groups execute sequentially
// (lowest priority number first), while components within the same group
// shut down in parallel.
package shutdown

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"sync"
	"time"
)

// component represents a single registered shutdown handler.
type component struct {
	name   string
	closer Closer
	config componentConfig
}

// Manager orchestrates graceful shutdown of all registered components.
//
// Components are shut down in priority order: lower priority numbers shut down first.
// Components with the same priority shut down in parallel.
// If no priority is set, components shut down in registration order.
//
// Manager is safe for concurrent use.
type Manager struct {
	cfg        managerConfig
	mu         sync.Mutex
	components []component
	nextPrio   int
	once       sync.Once
	done       chan struct{}
	err        error
}

// New creates a new shutdown [Manager] with the given options.
func New(opts ...Option) *Manager {
	cfg := defaultManagerConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Manager{
		cfg:  cfg,
		done: make(chan struct{}),
	}
}

// Register adds a component to the shutdown manager.
// Components are stopped in reverse registration order by default,
// or by explicit priority (lower number = stops first).
//
// Register returns the Manager to allow method chaining.
//
// Register panics if closer is nil.
func (m *Manager) Register(name string, closer Closer, opts ...ComponentOption) *Manager {
	if closer == nil {
		panic("shutdown: closer must not be nil")
	}

	var cc componentConfig
	for _, opt := range opts {
		opt(&cc)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !cc.explicit {
		cc.priority = m.nextPrio
	}
	m.nextPrio++

	m.components = append(m.components, component{
		name:   name,
		closer: closer,
		config: cc,
	})

	return m
}

// RegisterFunc is a convenience method that registers a function as a [Closer].
// It wraps fn in a [CloserFunc] and delegates to [Manager.Register].
func (m *Manager) RegisterFunc(name string, fn func(ctx context.Context) error, opts ...ComponentOption) *Manager {
	return m.Register(name, CloserFunc(fn), opts...)
}

// Listen blocks until an OS signal is received (SIGINT and SIGTERM by default),
// then initiates graceful shutdown. A second signal forces immediate exit via [os.Exit](1).
//
// Listen also triggers shutdown if the provided context is cancelled.
//
// Returns an aggregated error if any component failed to shut down.
func (m *Manager) Listen(ctx context.Context) error {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, m.cfg.signals...)
	defer signal.Stop(sigChan)

	m.cfg.logger.Info("shutdown listener started",
		slog.Any("signals", m.cfg.signals),
	)

	select {
	case sig := <-sigChan:
		m.cfg.logger.Info("received signal, initiating shutdown",
			slog.String("signal", sig.String()),
		)

		// Second signal forces immediate exit.
		go func() {
			<-sigChan
			m.cfg.logger.Error("received second signal, forcing exit")
			os.Exit(1)
		}()

		return m.Shutdown(ctx)

	case <-ctx.Done():
		m.cfg.logger.Info("context cancelled, initiating shutdown")
		return m.Shutdown(ctx)
	}
}

// Shutdown initiates graceful shutdown of all registered components.
// Components are stopped according to their priority: lower priority numbers first.
// Components with the same priority are stopped in parallel.
//
// Shutdown is idempotent — calling it multiple times is safe.
// Subsequent calls block until the first invocation completes and return the same error.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.once.Do(func() {
		m.err = m.doShutdown(ctx)
		close(m.done)
	})

	<-m.done
	return m.err
}

func (m *Manager) doShutdown(ctx context.Context) error {
	start := time.Now()

	m.cfg.logger.Info("shutdown started",
		slog.Duration("timeout", m.cfg.timeout),
	)

	globalCtx, globalCancel := context.WithTimeout(ctx, m.cfg.timeout)
	defer globalCancel()

	// Snapshot components under lock.
	m.mu.Lock()
	components := make([]component, len(m.components))
	copy(components, m.components)
	m.mu.Unlock()

	groups := groupByPriority(components)

	var allErrors []error

	for _, group := range groups {
		groupErrors := m.shutdownGroup(globalCtx, group)
		allErrors = append(allErrors, groupErrors...)

		if globalCtx.Err() != nil {
			m.cfg.logger.Error("global timeout exceeded, aborting remaining groups")
			break
		}
	}

	result := joinErrors(allErrors...)

	if result != nil {
		m.cfg.logger.Error("shutdown completed with errors",
			slog.Duration("elapsed", time.Since(start)),
			slog.String("error", result.Error()),
		)
	} else {
		m.cfg.logger.Info("shutdown completed successfully",
			slog.Duration("elapsed", time.Since(start)),
		)
	}

	return result
}

func (m *Manager) shutdownGroup(globalCtx context.Context, group []component) []error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for _, comp := range group {
		wg.Add(1)
		go func(c component) {
			defer wg.Done()

			compCtx := globalCtx
			if c.config.timeout > 0 {
				var cancel context.CancelFunc
				compCtx, cancel = context.WithTimeout(globalCtx, c.config.timeout)
				defer cancel()
			}

			start := time.Now()
			m.cfg.logger.Info("closing component",
				slog.String("name", c.name),
				slog.Int("priority", c.config.priority),
			)

			err := c.closer.Close(compCtx)
			elapsed := time.Since(start)

			if err != nil {
				m.cfg.logger.Error("component close failed",
					slog.String("name", c.name),
					slog.Duration("elapsed", elapsed),
					slog.String("error", err.Error()),
				)
				if m.cfg.onError != nil {
					m.cfg.onError(c.name, err)
				}
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			} else {
				m.cfg.logger.Info("component closed",
					slog.String("name", c.name),
					slog.Duration("elapsed", elapsed),
				)
			}
		}(comp)
	}

	wg.Wait()
	return errs
}

// groupByPriority sorts components by priority (stable) and groups them.
// Returns a slice of groups ordered by ascending priority.
func groupByPriority(components []component) [][]component {
	if len(components) == 0 {
		return nil
	}

	sort.SliceStable(components, func(i, j int) bool {
		return components[i].config.priority < components[j].config.priority
	})

	var groups [][]component
	currentPrio := components[0].config.priority
	currentGroup := []component{components[0]}

	for i := 1; i < len(components); i++ {
		if components[i].config.priority == currentPrio {
			currentGroup = append(currentGroup, components[i])
		} else {
			groups = append(groups, currentGroup)
			currentPrio = components[i].config.priority
			currentGroup = []component{components[i]}
		}
	}
	groups = append(groups, currentGroup)

	return groups
}
