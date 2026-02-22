package shutdown

import "context"

// Closer is the interface that any managed component must implement.
// Close should gracefully shut down the component, respecting the provided context
// for cancellation and deadlines.
//
// Many Go types already satisfy this interface or can be easily adapted.
// For example, *http.Server has a Shutdown(ctx) method that matches the signature.
type Closer interface {
	Close(ctx context.Context) error
}

// CloserFunc is an adapter that allows ordinary functions to be used as [Closer].
// It follows the same pattern as [net/http.HandlerFunc].
type CloserFunc func(ctx context.Context) error

// Close calls f(ctx).
func (f CloserFunc) Close(ctx context.Context) error {
	return f(ctx)
}
