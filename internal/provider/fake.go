package provider

// FakeOpenAICompatibleProvider is a deterministic in-process provider used by
// unit/integration tests. It deliberately exposes only call accounting and a
// response function; no network or real credential is involved.
import (
	"context"
	"sync"

	"github.com/Oumainory/DDBOT-AI/internal/classifier"
	"github.com/Oumainory/DDBOT-AI/internal/domain"
)

type FakeOpenAICompatibleProvider struct {
	Mu           sync.Mutex
	Calls        int
	ClassifyFunc func(context.Context, domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error)
	TestResult   TestResult
	TestErr      error
}

func (f *FakeOpenAICompatibleProvider) Classify(ctx context.Context, event domain.NormalizedEvent) (classifier.Classification, classifier.Usage, error) {
	if f == nil {
		return classifier.Classification{}, classifier.Usage{}, ErrUnavailable
	}
	f.Mu.Lock()
	f.Calls++
	fn := f.ClassifyFunc
	f.Mu.Unlock()
	if fn == nil {
		return classifier.Classification{SchemaVersion: 1, Category: domain.CategoryUnknown, Importance: domain.ImportanceLow, Confidence: 0.99, Uncertain: true}, classifier.Usage{}, nil
	}
	return fn(ctx, event)
}

func (f *FakeOpenAICompatibleProvider) TestConnection(context.Context) (TestResult, error) {
	if f == nil {
		return TestResult{}, ErrUnavailable
	}
	return f.TestResult, f.TestErr
}

func (f *FakeOpenAICompatibleProvider) CallCount() int {
	if f == nil {
		return 0
	}
	f.Mu.Lock()
	defer f.Mu.Unlock()
	return f.Calls
}

// FakeProvider is kept as a concise alias for callers that do not need to
// encode the transport in a test name.
type FakeProvider = FakeOpenAICompatibleProvider
