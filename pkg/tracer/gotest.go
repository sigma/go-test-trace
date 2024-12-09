package tracer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sigma/go-test-trace/pkg/trace_testing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// TestCommandExecutor abstracts the execution of go test commands
type TestCommandExecutor interface {
	Execute(args []string, extraEnv map[string]string) (io.Reader, error)
}

// RealCommandExecutor executes actual go test commands
type RealCommandExecutor struct {
	skipperAddr net.Addr
	svcName     string
}

func (e *RealCommandExecutor) Execute(args []string, extraEnv map[string]string) (io.Reader, error) {
	cmd := exec.Command("go", append([]string{"test"}, append(args, "-json")...)...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("%s=%s", trace_testing.SkipperNetworkEnvVar, e.skipperAddr.Network()),
		fmt.Sprintf("%s=%s", trace_testing.SkipperAddressEnvVar, e.skipperAddr.String()),
		fmt.Sprintf("%s=%s", trace_testing.ServiceNameEnvVar, e.svcName),
	)
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s%s=%s", trace_testing.CarrierEnvVarPrefix, k, v))
	}

	// Create a pipe reader/writer pair
	pr, pw := io.Pipe()

	// Start the command with a pipe to capture output
	cmd.Stdout = pw

	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, fmt.Errorf("failed to start command: %v", err)
	}

	// Wait for command completion in a goroutine and close the writer when done
	go func() {
		err := cmd.Wait()
		if err != nil {
			// We expect test failures, so we just log
			log.Printf("Command finished with: %v", err)
		}
		pw.Close()
	}()

	return pr, nil
}

type spanData struct {
	span      oteltrace.Span
	startTime time.Time
	ctx       context.Context
}

type goTestOutput struct {
	Time   time.Time
	Action string
	Test   string
	Output string
}

type TestTracer struct {
	collectedSpans   map[string]*spanData
	collectedSpansMu sync.RWMutex
	skipper          Skipper
	processDelay     time.Duration
	executor         TestCommandExecutor
}

func NewTestTracer(skipper Skipper, svc string) *TestTracer {
	return &TestTracer{
		collectedSpans: make(map[string]*spanData, 1000),
		skipper:        skipper,
		processDelay:   500 * time.Millisecond, // default half second delay
		executor: &RealCommandExecutor{
			skipperAddr: skipper.Addr(),
			svcName:     svc,
		},
	}
}

// SetProcessDelay allows configuring the delay between receiving and processing test events
func (tt *TestTracer) SetProcessDelay(delay time.Duration) {
	tt.processDelay = delay
}

// SetCommandExecutor allows overriding the command executor (useful for testing)
func (tt *TestTracer) SetCommandExecutor(executor TestCommandExecutor) {
	tt.executor = executor
}

func (tt *TestTracer) Trace(ctx context.Context, args []string) error {
	t := otel.Tracer("test")
	globalCtx, globalSpan := t.Start(ctx, "tests")
	defer globalSpan.End()

	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	carrier := propagation.MapCarrier{}
	propagator.Inject(globalCtx, carrier)

	r, err := tt.executor.Execute(args, carrier)
	if err != nil {
		return fmt.Errorf("failed to execute test command: %v", err)
	}

	readDone := make(chan struct{})
	processingDone := make(chan struct{})
	eventCh := make(chan goTestOutput, 1000)

	go tt.readEvents(r, eventCh, readDone)
	go tt.processEvents(globalCtx, t, eventCh, processingDone)

	<-readDone
	<-processingDone
	return nil
}

// readEvents reads test output events from the pipe
func (tt *TestTracer) readEvents(r io.Reader, eventCh chan<- goTestOutput, done chan<- struct{}) {
	defer close(eventCh)
	defer close(done)

	decoder := json.NewDecoder(r)
	for decoder.More() {
		var data goTestOutput
		if err := decoder.Decode(&data); err != nil {
			if err == io.EOF {
				return
			}
			log.Printf("Failed to decode JSON: %v", err)
			continue
		}

		// Print output immediately
		fmt.Print(data.Output)
		// Send event for processing
		eventCh <- data
	}
}

// processEvents handles the processing of test events with delays
func (tt *TestTracer) processEvents(ctx context.Context, t oteltrace.Tracer, eventCh <-chan goTestOutput, done chan<- struct{}) {
	var wg sync.WaitGroup
	defer func() {
		wg.Wait() // Wait for all goroutines to finish before closing done
		close(done)
	}()

	type testState struct {
		startEvent goTestOutput
		endChan    chan goTestOutput
	}
	pendingTests := make(map[string]*testState)
	var pendingMu sync.RWMutex

	for data := range eventCh {
		data := data // capture for goroutine

		switch data.Action {
		case "run":
			if !tt.skipper.ShouldSkip(data.Test) {
				state := &testState{
					startEvent: data,
					endChan:    make(chan goTestOutput, 1),
				}
				pendingMu.Lock()
				pendingTests[data.Test] = state
				pendingMu.Unlock()

				wg.Add(1)
				go func(test string, state *testState) {
					defer wg.Done()

					if tt.processDelay > 0 {
						time.Sleep(tt.processDelay)
					}

					// Start the span
					tt.handleTestStart(ctx, t, state.startEvent)

					// Wait for end event
					if endEvent, ok := <-state.endChan; ok {
						if tt.processDelay > 0 {
							time.Sleep(tt.processDelay)
						}
						tt.handleTestEnd(endEvent)
					}
				}(data.Test, state)
			}
		case "pass", "fail", "skip":
			pendingMu.RLock()
			state, ok := pendingTests[data.Test]
			pendingMu.RUnlock()
			if ok {
				state.endChan <- data
				close(state.endChan)
				pendingMu.Lock()
				delete(pendingTests, data.Test)
				pendingMu.Unlock()
			}
		}
	}

	// Close any remaining channels
	pendingMu.Lock()
	for _, state := range pendingTests {
		close(state.endChan)
	}
	pendingMu.Unlock()
}

// handleTestStart processes the start of a test
func (tt *TestTracer) handleTestStart(ctx context.Context, t oteltrace.Tracer, data goTestOutput) {
	if tt.skipper.ShouldSkip(data.Test) {
		return
	}

	// recreate the hierarchy of (sub-)tests
	tName := data.Test
	idx := strings.LastIndex(tName, "/")
	if idx != -1 {
		// TODO: this horrible. We need to enforce the order of events instead.
		time.Sleep(500 * time.Millisecond) // wait for the parent test to be collected
	}

	tt.collectedSpansMu.Lock()
	defer tt.collectedSpansMu.Unlock()

	if idx != -1 {
		parentTest := tName[:idx]
		if parentTest != "" && tt.collectedSpans[parentTest] != nil {
			ctx = tt.collectedSpans[parentTest].ctx
			tName = tName[idx+1:]
		} else {
		}
	}

	ctx, span := t.Start(ctx, tName, oteltrace.WithTimestamp(data.Time))

	tt.collectedSpans[data.Test] = &spanData{
		span:      span,
		startTime: data.Time,
		ctx:       ctx,
	}
}

// handleTestEnd processes the end of a test
func (tt *TestTracer) handleTestEnd(data goTestOutput) {
	if data.Test == "" {
		return
	}

	tt.collectedSpansMu.RLock()
	spanData, ok := tt.collectedSpans[data.Test]
	tt.collectedSpansMu.RUnlock()

	if !ok {
		return // Test was skipped or not tracked
	}

	if data.Action == "fail" {
		spanData.span.SetStatus(codes.Error, "")
	}
	spanData.span.End(oteltrace.WithTimestamp(data.Time))
}
