package tracer

import (
	"context"
	"io"
	"log"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// MockCommandExecutor implements TestCommandExecutor for testing
type MockCommandExecutor struct {
	output string
}

func NewMockCommandExecutor(output string) *MockCommandExecutor {
	return &MockCommandExecutor{output: output}
}

func (e *MockCommandExecutor) Execute(args []string, extraEnv map[string]string) (io.Reader, error) {
	pr, pw := io.Pipe()

	go func() {
		defer pw.Close()
		// Write each line separately to simulate streaming
		for _, line := range strings.Split(e.output, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, err := pw.Write([]byte(line + "\n")); err != nil {
				log.Printf("Error writing to pipe: %v", err)
				return
			}
		}
	}()

	return pr, nil
}

func TestTracer_Trace(t *testing.T) {
	tests := []struct {
		name           string
		skipTests      []string
		testOutput     string
		wantSpanCount  int
		wantFailedSpan bool
	}{
		{
			name:      "single passing test",
			skipTests: nil,
			testOutput: `
{"Time":"2024-03-20T10:00:00Z","Action":"run","Test":"TestFoo"}
{"Time":"2024-03-20T10:00:01Z","Action":"pass","Test":"TestFoo"}
`,
			wantSpanCount:  1,
			wantFailedSpan: false,
		},
		{
			name:      "single failing test",
			skipTests: nil,
			testOutput: `
{"Time":"2024-03-20T10:00:00Z","Action":"run","Test":"TestFoo"}
{"Time":"2024-03-20T10:00:01Z","Action":"fail","Test":"TestFoo"}
`,
			wantSpanCount:  1,
			wantFailedSpan: true,
		},
		{
			name:      "skipped test",
			skipTests: []string{"TestFoo"},
			testOutput: `
{"Time":"2024-03-20T10:00:00Z","Action":"run","Test":"TestFoo"}
{"Time":"2024-03-20T10:00:01Z","Action":"pass","Test":"TestFoo"}
`,
			wantSpanCount:  0,
			wantFailedSpan: false,
		},
		{
			name:      "multiple tests with one skipped",
			skipTests: []string{"TestBar"},
			testOutput: `
{"Time":"2024-03-20T10:00:00Z","Action":"run","Test":"TestFoo"}
{"Time":"2024-03-20T10:00:01Z","Action":"pass","Test":"TestFoo"}
{"Time":"2024-03-20T10:00:02Z","Action":"run","Test":"TestBar"}
{"Time":"2024-03-20T10:00:03Z","Action":"pass","Test":"TestBar"}
`,
			wantSpanCount:  1,
			wantFailedSpan: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spanRecorder := tracetest.NewSpanRecorder()
			tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))

			originalTracerProvider := otel.GetTracerProvider()
			otel.SetTracerProvider(tracerProvider)
			defer otel.SetTracerProvider(originalTracerProvider)

			// Create tracer with mock skipper and executor
			tracer := NewTestTracer(NewMockSkipper(tt.skipTests), "test")
			tracer.SetCommandExecutor(NewMockCommandExecutor(tt.testOutput))
			tracer.SetProcessDelay(0) // No delay needed for tests

			err := tracer.Trace(context.Background(), []string{})
			if err != nil {
				t.Fatalf("Trace() error = %v", err)
			}

			// Wait for spans to be recorded with exponential backoff
			var spans []sdktrace.ReadOnlySpan
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				spans = spanRecorder.Ended()
				if len(spans)-1 == tt.wantSpanCount { // -1 for the global span
					break
				}
				time.Sleep(10 * time.Millisecond)
			}

			// Check results
			if got := len(spans) - 1; got != tt.wantSpanCount {
				t.Errorf("got %d spans, want %d", got, tt.wantSpanCount)
				for _, span := range spans {
					t.Logf("span: %s", span.Name())
				}
			}

			if tt.wantSpanCount > 0 {
				for _, span := range spans {
					if strings.HasPrefix(span.Name(), "Test") {
						if tt.wantFailedSpan && span.Status().Code != codes.Error {
							t.Error("expected failed span")
						} else if !tt.wantFailedSpan && span.Status().Code != codes.Unset {
							t.Error("expected successful span")
						}
					}
				}
			}
		})
	}
}

func TestTracer_ProcessDelay(t *testing.T) {
	testOutput := `
{"Time":"2024-03-20T10:00:00Z","Action":"run","Test":"TestFoo"}
{"Time":"2024-03-20T10:00:01Z","Action":"pass","Test":"TestFoo"}
`

	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))

	originalTracerProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tracerProvider)
	defer otel.SetTracerProvider(originalTracerProvider)

	tracer := NewTestTracer(NewMockSkipper(nil), "test")
	tracer.SetCommandExecutor(NewMockCommandExecutor(testOutput))
	delay := 100 * time.Millisecond
	tracer.SetProcessDelay(delay)

	start := time.Now()
	err := tracer.Trace(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}

	elapsed := time.Since(start)
	if elapsed < delay {
		t.Errorf("Processing happened too quickly, expected at least %v delay, got %v", delay, elapsed)
	}
}
