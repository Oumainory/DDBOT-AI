package provider

import (
	"context"
	"sync/atomic"

	"github.com/cnxysoft/DDBOT-WSa/internal/classifier"
	"github.com/cnxysoft/DDBOT-WSa/internal/domain"
)

// Swappable is a small runtime client holder. Provider configuration updates
// replace the client atomically; in-flight requests keep using the client they
// already acquired and no global state is introduced.
type Swappable struct{ current atomic.Value }

func NewSwappable(initial Provider) *Swappable {
	s := &Swappable{}
	s.current.Store(providerValue{Provider: initial})
	return s
}

func (s *Swappable) Set(value Provider) {
	if s == nil {
		return
	}
	s.current.Store(providerValue{Provider: value})
}

func (s *Swappable) Get() Provider {
	if s == nil {
		return nil
	}
	return s.current.Load().(providerValue).Provider
}

func (s *Swappable) Classify(ctx context.Context, event domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
	if value := s.Get(); value != nil {
		return value.Classify(ctx, event)
	}
	return classifier.Classification{}, classifier.Usage{}, ErrUnavailable
}

func (s *Swappable) TestConnection(ctx context.Context) (TestResult, error) {
	if value := s.Get(); value != nil {
		return value.TestConnection(ctx)
	}
	return TestResult{}, ErrUnavailable
}

type providerValue struct{ Provider Provider }
