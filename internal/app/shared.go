package app

import (
	"context"
	"sync"
)

type sharedResult[T any] struct {
	done     chan struct{}
	cancel   context.CancelFunc
	users    int
	finished bool
	value    T
	err      error
}

// sharedWork gives each caller its own cancellation while retaining work that
// another caller still needs. Epochs belong in keys, so refresh starts new work.
type sharedWork[T any] struct {
	mu   sync.Mutex
	jobs map[string]*sharedResult[T]
}

func (s *sharedWork[T]) do(ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	s.mu.Lock()
	if s.jobs == nil {
		s.jobs = make(map[string]*sharedResult[T])
	}
	j := s.jobs[key]
	if j == nil {
		work, cancel := context.WithCancel(context.WithoutCancel(ctx))
		j = &sharedResult[T]{done: make(chan struct{}), cancel: cancel}
		s.jobs[key] = j
		go func() {
			value, err := fn(work)
			s.mu.Lock()
			j.value, j.err, j.finished = value, err, true
			if s.jobs[key] == j {
				delete(s.jobs, key)
			}
			close(j.done)
			s.mu.Unlock()
			cancel()
		}()
	}
	j.users++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		j.users--
		if j.users == 0 && !j.finished {
			if s.jobs[key] == j {
				delete(s.jobs, key)
			}
			j.cancel()
		}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-j.done:
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		return j.value, j.err
	}
}
