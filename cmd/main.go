package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/honeycombio/otel-config-go/otelconfig"
	"github.com/sigma/go-test-trace/pkg/tracer"
)

var (
	help         bool
	processDelay time.Duration = 500 * time.Millisecond
	serviceName  string        = "go-test-trace"
)

func main() {
	skipper, err := tracer.NewTestSkipper()
	if err != nil {
		log.Fatalf("Failed to create test skipper: %v", err)
	}
	defer skipper.Close()

	fset := flag.NewFlagSet("", flag.ContinueOnError)
	fset.BoolVar(&help, "help", false, "")
	fset.StringVar(&serviceName, "service", serviceName, "Service name for tracing")
	fset.Usage = func() {} // don't error instead pass remaining arguments to go test
	fset.Parse(os.Args[1:])

	if help {
		fmt.Println(usageText)
		os.Exit(0)
	}

	// Set up OpenTelemetry
	otelShutdown, err := otelconfig.ConfigureOpenTelemetry(
		otelconfig.WithServiceName(serviceName),
	)
	if err != nil {
		log.Fatalf("error setting up OTel SDK - %e", err)
	}
	defer otelShutdown()

	// Create and run the test tracer
	testTracer := tracer.NewTestTracer(skipper, serviceName)
	testTracer.SetProcessDelay(processDelay)
	if err := testTracer.Trace(context.Background(), fset.Args()); err != nil {
		log.Fatal(err)
	}
}

const usageText = `Usage:
go-test-trace [flags...] [go test flags...]

Flags:
-help        Print this text.
-socket      Unix socket path for receiving skip commands
-service     Service name for tracing
Run "go help test" for go test flags.`
