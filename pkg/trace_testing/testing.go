package trace_testing

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/honeycombio/otel-config-go/otelconfig"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	ServiceNameEnvVar = "GO_TEST_TRACE_SERVICE_NAME"

	// SkipperNetworkEnvVar is the environment variable used to pass the listener network
	SkipperNetworkEnvVar = "GO_TEST_TRACE_SKIPPER_NETWORK"

	// SkipperAddressEnvVar is the environment variable used to pass the listener address
	SkipperAddressEnvVar = "GO_TEST_TRACE_SKIPPER_ADDRESS"

	CarrierEnvVarPrefix = "GO_TEST_TRACE_CARRIER_"
)

type skipper interface {
	Skip(testName string) error
}

var (
	defaultSkipper     skipper
	defaultServiceName string
	defaultCarrier     propagation.MapCarrier = propagation.MapCarrier{}
)

func init() {
	defaultServiceName = os.Getenv(ServiceNameEnvVar)

	// Initialize the test skipper client
	skipperNetwork := os.Getenv(SkipperNetworkEnvVar)
	skipperAddr := os.Getenv(SkipperAddressEnvVar)
	if skipperNetwork == "" || skipperAddr == "" {
		defaultSkipper = &noopSkipper{}
		return
	}

	defaultSkipper = &skipperClient{
		network: skipperNetwork,
		addr:    skipperAddr,
	}

	// Reconstruct the carrier from the environment variables
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, CarrierEnvVarPrefix) {
			parts := strings.SplitN(env, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimPrefix(parts[0], CarrierEnvVarPrefix)
				value := parts[1]
				defaultCarrier.Set(key, value)
			}
		}
	}
}

type noopSkipper struct{}

func (s *noopSkipper) Skip(testName string) error {
	return nil
}

type skipperClient struct {
	network string
	addr    string

	conn net.Conn
}

func (s *skipperClient) Skip(testName string) error {
	if s.conn == nil {
		conn, err := net.Dial(s.network, s.addr)
		if err != nil {
			return fmt.Errorf("failed to connect to skipper: %v", err)
		}
		s.conn = conn
	}

	if _, err := fmt.Fprintf(s.conn, "%s\n", testName); err != nil {
		return fmt.Errorf("failed to send skip command: %v", err)
	}
	return nil

}

type BasicT interface {
	testing.TB
	Deadline() (deadline time.Time, ok bool)
	Parallel()
	Run(name string, f func(t *testing.T)) bool
}

type contexter interface {
	BasicT
	Context() context.Context
}

type T interface {
	testing.TB
	Deadline() (deadline time.Time, ok bool)
	Parallel()
	Context() context.Context
	WithContext(ctx context.Context) T
	Run(name string, f func(t T)) bool
}

type tWrapper struct {
	BasicT
	ctx            context.Context
	disableTracing bool
}

func (t *tWrapper) Context() context.Context {
	return t.ctx
}

func (t *tWrapper) WithContext(ctx context.Context) T {
	return &tWrapper{BasicT: t.BasicT, ctx: ctx}
}

func (t *tWrapper) Deadline() (deadline time.Time, ok bool) {
	return t.BasicT.Deadline()
}

func (t *tWrapper) Parallel() {
	t.BasicT.Parallel()
}

func (t *tWrapper) Run(name string, f func(t T)) bool {
	return t.BasicT.Run(name, func(tt *testing.T) {
		wt := wrapT(tt)

		ctx := t.Context()
		if !t.disableTracing {
			tracer := otel.Tracer(wt.Name())
			var span trace.Span
			ctx, span = tracer.Start(ctx, name)
			defer span.End()
		}

		if err := defaultSkipper.Skip(wt.Name()); err != nil {
			log.Printf("error skipping test %s: %e", wt.Name(), err)
		}
		f(wt.WithContext(ctx))
	})
}

var _ T = (*tWrapper)(nil)

func wrapT(t BasicT) *tWrapper {
	var res *tWrapper
	if tt, ok := t.(contexter); ok { // should activate with Go 1.24
		res = &tWrapper{BasicT: tt, ctx: tt.Context()}
	} else {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		res = &tWrapper{BasicT: t, ctx: ctx}
	}

	return res
}

func WithTracing(t BasicT) T {
	res := wrapT(t)

	otelShutdown, err := otelconfig.ConfigureOpenTelemetry(
		otelconfig.WithServiceName(defaultServiceName),
	)
	if err != nil {
		// OpenTelemetry is not initialized, make our best to run the test
		log.Printf("error setting up OTel SDK - %e", err)
		return &tWrapper{BasicT: t, ctx: context.Background(), disableTracing: true}
	}
	res.Cleanup(otelShutdown)

	propagator := propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
	ctx := propagator.Extract(res.Context(), defaultCarrier)

	tName := res.Name()
	tracer := otel.Tracer(tName)
	ctx, span := tracer.Start(ctx, tName)

	if err := defaultSkipper.Skip(tName); err != nil {
		log.Printf("error skipping test %s: %e", tName, err)
	}

	res.Cleanup(func() {
		span.End()
	})
	return res.WithContext(ctx)
}
