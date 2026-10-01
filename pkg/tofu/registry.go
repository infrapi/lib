package tofu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	infraerrors "github.com/infrapi/lib/pkg/errors"
)

// Registry keeps a set of named, running providers, so a caller can drive
// resources across several provider plugins at once and close every one of
// them together. It is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]*Provider
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]*Provider{}}
}

// Start launches exe as name's provider plugin, registers it under name, and
// returns it. It is an error for name to already be registered.
func (r *Registry) Start(ctx context.Context, name, exe string, args ...string) (*Provider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.providers[name]; exists {
		return nil, infraerrors.New(http.StatusConflict, "tofu.Registry.Start", "provider %q is already registered", name)
	}

	p, err := Start(ctx, name, exe, args...)
	if err != nil {
		return nil, err
	}
	r.providers[name] = p
	return p, nil
}

// Provider returns the provider registered under name, and true, or false
// if no provider by that name is registered.
func (r *Registry) Provider(name string) (*Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.providers[name]
	return p, ok
}

// Names returns the names of every registered provider, in no particular
// order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

// Close closes every registered provider, continuing past individual
// failures, and returns a joined error describing every one that failed. It
// leaves the registry empty, so it is safe to call more than once.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var errs []error
	for name, p := range r.providers {
		if err := p.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close provider %q: %w", name, err))
		}
	}
	r.providers = map[string]*Provider{}
	return errors.Join(errs...)
}
