package shutdown

import (
	"log/slog"
	"os"
	"syscall"
	"time"
)

const (
	// defaultTimeout is the default global shutdown timeout.
	// Matches the default Kubernetes terminationGracePeriodSeconds.
	defaultTimeout = 30 * time.Second
)

var defaultSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}

// managerConfig holds resolved configuration for the [Manager].
type managerConfig struct {
	timeout time.Duration
	signals []os.Signal
	logger  *slog.Logger
	onError func(name string, err error)
}

func defaultManagerConfig() managerConfig {
	return managerConfig{
		timeout: defaultTimeout,
		signals: defaultSignals,
		logger:  slog.Default(),
	}
}

// Option configures the [Manager].
type Option func(*managerConfig)

// WithTimeout sets the global shutdown timeout. The default is 30 seconds.
// This timeout is applied as a context deadline wrapping the parent context
// passed to [Manager.Shutdown] or [Manager.Listen].
func WithTimeout(d time.Duration) Option {
	return func(c *managerConfig) {
		c.timeout = d
	}
}

// WithSignals sets the OS signals that trigger graceful shutdown.
// The default signals are SIGINT and SIGTERM.
func WithSignals(signals ...os.Signal) Option {
	return func(c *managerConfig) {
		c.signals = signals
	}
}

// WithLogger sets a custom [*slog.Logger] for structured shutdown logging.
// The default is [slog.Default].
func WithLogger(logger *slog.Logger) Option {
	return func(c *managerConfig) {
		c.logger = logger
	}
}

// WithOnError sets a callback that is invoked when a component fails to shut down.
// The callback receives the component name and the error.
func WithOnError(fn func(name string, err error)) Option {
	return func(c *managerConfig) {
		c.onError = fn
	}
}

// componentConfig holds resolved configuration for a single component.
type componentConfig struct {
	timeout  time.Duration // 0 means inherit global timeout.
	priority int
	explicit bool // whether priority was explicitly set.
}

// ComponentOption configures a single registered component.
type ComponentOption func(*componentConfig)

// WithComponentTimeout overrides the global timeout for this component.
// If the component timeout is longer than the remaining global timeout,
// the global timeout takes precedence (context deadlines propagate).
func WithComponentTimeout(d time.Duration) ComponentOption {
	return func(c *componentConfig) {
		c.timeout = d
	}
}

// WithPriority sets the shutdown priority for a component.
// Lower numbers shut down first. Components with the same priority
// shut down in parallel. The default priority is assigned based on
// registration order (first registered = lowest priority = shuts down first).
func WithPriority(p int) ComponentOption {
	return func(c *componentConfig) {
		c.priority = p
		c.explicit = true
	}
}
