package tracer

import (
	"net"
	"sync"
)

// MockSkipper implements Skipper for testing
type MockSkipper struct {
	skipTests map[string]bool
	addr      net.Addr
	mu        sync.RWMutex
}

func NewMockSkipper(testsToSkip []string) *MockSkipper {
	skip := make(map[string]bool)
	for _, test := range testsToSkip {
		skip[test] = true
	}
	return &MockSkipper{
		skipTests: skip,
		addr: &net.UnixAddr{
			Net:  "unix",
			Name: "/tmp/mock-skipper.sock",
		},
	}
}

func (m *MockSkipper) ShouldSkip(testName string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.skipTests[testName]
}

func (m *MockSkipper) Addr() net.Addr {
	return m.addr
}

func (m *MockSkipper) SkipTest(testName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.skipTests[testName] = true
}
