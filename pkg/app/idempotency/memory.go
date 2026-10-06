package idempotency

import (
	"context"
	"sync"
)

// Memory is a Store for tests. It remembers nothing across restarts, which is
// exactly why production does not use it: a retried activity usually arrives
// after something went wrong, a restart included.
type Memory struct {
	mu   sync.Mutex
	keys map[string]*entry
}

type entry struct {
	hash string
	done bool
	rec  Record
}

// NewMemory builds one.
func NewMemory() *Memory { return &Memory{keys: map[string]*entry{}} }

// Begin claims the key, or says why it cannot be.
func (m *Memory) Begin(_ context.Context, scope, key, hash string) (Outcome, *Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.keys[scope+"\x00"+key]
	switch {
	case !ok:
		m.keys[scope+"\x00"+key] = &entry{hash: hash}
		return Proceed, nil, nil
	case e.hash != hash:
		return Mismatch, nil, nil
	case e.done:
		rec := e.rec
		return Replay, &rec, nil
	default:
		return Busy, nil, nil
	}
}

// Finish remembers the response.
func (m *Memory) Finish(_ context.Context, scope, key string, rec Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.keys[scope+"\x00"+key]; ok {
		e.done, e.rec = true, rec
	}
	return nil
}

// Abandon forgets the key.
func (m *Memory) Abandon(_ context.Context, scope, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.keys, scope+"\x00"+key)
	return nil
}
