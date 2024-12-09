package tracer

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestTestSkipper(t *testing.T) {
	tests := []struct {
		name      string
		skipTests []string
		checkTest string
		want      bool
	}{
		{
			name:      "no tests skipped",
			skipTests: nil,
			checkTest: "TestFoo",
			want:      false,
		},
		{
			name:      "test is skipped",
			skipTests: []string{"TestFoo"},
			checkTest: "TestFoo",
			want:      true,
		},
		{
			name:      "test is not in skip list",
			skipTests: []string{"TestBar"},
			checkTest: "TestFoo",
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a new skipper for each test case
			skipper, err := NewTestSkipper()
			if err != nil {
				t.Fatalf("Failed to create test skipper: %v", err)
			}
			defer skipper.Close()

			for _, test := range tt.skipTests {
				skipper.SkipTest(test)
			}

			if got := skipper.ShouldSkip(tt.checkTest); got != tt.want {
				t.Errorf("ShouldSkip(%q) = %v, want %v", tt.checkTest, got, tt.want)
			}
		})
	}
}

func TestTestSkipper_Connection(t *testing.T) {
	skipper, err := NewTestSkipper()
	if err != nil {
		t.Fatalf("Failed to create test skipper: %v", err)
	}
	defer skipper.Close()

	// Connect to the socket
	addr := skipper.Addr()
	conn, err := net.Dial(addr.Network(), addr.String())
	if err != nil {
		t.Fatalf("Failed to connect to socket: %v", err)
	}
	defer conn.Close()

	// Send a test name
	testName := "TestToSkip"
	_, err = conn.Write([]byte(testName + "\n"))
	if err != nil {
		t.Fatalf("Failed to write to socket: %v", err)
	}

	// Give it a moment to process
	time.Sleep(100 * time.Millisecond)

	// Verify the test was added to skip list
	if !skipper.ShouldSkip(testName) {
		t.Errorf("Test %q should be skipped", testName)
	}
}

func TestTestSkipper_Concurrent(t *testing.T) {
	skipper, err := NewTestSkipper()
	if err != nil {
		t.Fatalf("Failed to create test skipper: %v", err)
	}
	defer skipper.Close()

	const numTests = 2
	done := make(chan struct{})
	errs := make(chan error, 10) // Channel for collecting errors

	// Start multiple goroutines to add tests
	for i := 0; i < 10; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < numTests; j++ {
				testName := fmt.Sprintf("Test%d_%d", n, j)
				addr := skipper.Addr()
				conn, err := net.Dial(addr.Network(), addr.String())
				if err != nil {
					errs <- fmt.Errorf("Failed to connect to socket: %v", err)
					return
				}
				_, err = conn.Write([]byte(testName + "\n"))
				conn.Close()
				if err != nil {
					errs <- fmt.Errorf("Failed to write to socket: %v", err)
					return
				}
			}
		}(i)
	}

	// Wait for all goroutines to finish
	for i := 0; i < 10; i++ {
		<-done
	}
	close(errs)

	// Check for any errors
	for err := range errs {
		t.Error(err)
	}

	// Give it a moment to process all messages
	time.Sleep(100 * time.Millisecond)

	// Verify all tests were added
	for i := 0; i < 10; i++ {
		for j := 0; j < numTests; j++ {
			testName := fmt.Sprintf("Test%d_%d", i, j)
			if !skipper.ShouldSkip(testName) {
				t.Errorf("Test %q should be skipped", testName)
			}
		}
	}
}
