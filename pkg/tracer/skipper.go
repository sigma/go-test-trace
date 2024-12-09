package tracer

import "net"

// Skipper determines whether a test should be skipped for tracing
type Skipper interface {
	ShouldSkip(testName string) bool
	Addr() net.Addr // Returns the address where skip commands can be sent
}
