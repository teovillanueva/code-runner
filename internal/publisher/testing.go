package publisher

import "github.com/teovillanueva/code-runner/internal/config"

// Triggerer is the interface that wraps the Trigger method. It is the same as
// the internal triggerer interface, exported so that test packages can provide
// a fake implementation to NewForTest without needing access to the unexported
// interface.
//
// Implementing types must satisfy:
//
//	Trigger(channel, event string, data interface{}) error
type Triggerer interface {
	Trigger(channel, event string, data interface{}) error
}

// NewForTest creates a Publisher backed by the provided Triggerer. It is
// intended for use in tests that need to capture or assert on published events
// without a live soketi instance.
//
// Example usage:
//
//	ft := &fakeTriggerer{}
//	pub := publisher.NewForTest(ft)
func NewForTest(t Triggerer) *Publisher {
	p, _ := newWithTriggerer(config.Config{}, t)
	return p
}
