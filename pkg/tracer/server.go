package tracer

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// TestSkipper implements the Skipper interface and provides a way to dynamically
// add tests to skip
type TestSkipper struct {
	skipTests map[string]struct{}
	skipMutex sync.RWMutex
	listener  net.Listener
}

// NewTestSkipper creates a new TestSkipper with a Unix domain socket listener
func NewTestSkipper() (*TestSkipper, error) {
	// Generate a unique socket path in /tmp
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("go-test-trace-%d.sock", os.Getpid()))
	os.Remove(sockPath) // Clean up any existing socket

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %v", err)
	}

	ts := &TestSkipper{
		skipTests: make(map[string]struct{}),
		listener:  listener,
	}

	go ts.acceptConnections()

	return ts, nil
}

// Addr returns the address of the Unix domain socket
func (ts *TestSkipper) Addr() net.Addr {
	return ts.listener.Addr()
}

// SkipTest adds a test to the skip list
func (ts *TestSkipper) SkipTest(testName string) {
	ts.skipMutex.Lock()
	ts.skipTests[testName] = struct{}{}
	ts.skipMutex.Unlock()
}

// ShouldSkip implements the Skipper interface
func (ts *TestSkipper) ShouldSkip(testName string) bool {
	ts.skipMutex.RLock()
	defer ts.skipMutex.RUnlock()
	_, skip := ts.skipTests[testName]
	return skip
}

// Close stops the server and cleans up resources
func (ts *TestSkipper) Close() error {
	return ts.listener.Close()
}

func (ts *TestSkipper) acceptConnections() {
	for {
		conn, err := ts.listener.Accept()
		if err != nil {
			if !isClosedError(err) {
				log.Printf("Failed to accept connection: %v", err)
			}
			return
		}

		go ts.handleConnection(conn)
	}
}

func (ts *TestSkipper) handleConnection(conn net.Conn) {
	defer conn.Close()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		testName := strings.TrimSpace(scanner.Text())
		if testName != "" {
			ts.SkipTest(testName)
			log.Printf("Added test to skip list: %s", testName)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from connection: %v", err)
	}
}

// isClosedError returns true if the error indicates the listener was closed
func isClosedError(err error) bool {
	return strings.Contains(err.Error(), "use of closed network connection")
}
