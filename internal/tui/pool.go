package tui

import (
	"errors"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazyset/internal/session"
)

type terminal interface {
	Send(tea.Msg) error
	Write([]byte) error
	Resize(int, int) error
	Snapshot() session.Frame
	Stop() error
	Done() <-chan struct{}
}

// pool owns sessions even when a start completes after the UI has exited.
type pool struct {
	mu         sync.Mutex
	wg         sync.WaitGroup
	closed     bool
	items      map[string]terminal
	generation map[string]uint64
}

func newPool() *pool { return &pool{items: make(map[string]terminal), generation: map[string]uint64{}} }

func (p *pool) reserve(key string) uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation[key]++
	return p.generation[key]
}
func (p *pool) cancel(key string) (uint64, terminal) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation[key]++
	return p.generation[key], p.items[key]
}

// A closed session releases its retained frame and pointer, while generations
// remain monotonic so a late start can never reappear as a reopened session.
func (p *pool) forget(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation[key]++
	delete(p.items, key)
}

func (p *pool) start(key string, generation uint64, spec session.Spec) (terminal, error) {
	p.mu.Lock()
	if p.closed || p.generation[key] != generation {
		p.mu.Unlock()
		return nil, errors.New("workbench is closing")
	}
	p.wg.Add(1)
	p.mu.Unlock()
	defer p.wg.Done()
	s, err := session.Start(spec)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed || p.generation[key] != generation {
		p.mu.Unlock()
		_ = s.Stop()
		return nil, errors.New("workbench is closing")
	}
	old := p.items[key]
	p.items[key] = s
	p.mu.Unlock()
	if old != nil {
		_ = old.Stop()
	}
	return s, nil
}

func (p *pool) close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.wg.Wait()
	p.mu.Lock()
	items := p.items
	p.items = make(map[string]terminal)
	p.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range items {
		wg.Add(1)
		go func(t terminal) { defer wg.Done(); _ = t.Stop() }(s)
	}
	wg.Wait()
}

func ended(t terminal) bool {
	select {
	case <-t.Done():
		return true
	default:
		return false
	}
}
