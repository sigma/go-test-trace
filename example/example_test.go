// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package example

import (
	"testing"
	"time"

	"github.com/sigma/go-test-trace/pkg/trace_testing"
)

func TestStart(t *testing.T) {
	time.Sleep(500 * time.Millisecond)
}

func TestStartWithOptions(t *testing.T) {
	time.Sleep(1000 * time.Millisecond)
}

func TestFileParser(t *testing.T) {
	time.Sleep(300 * time.Millisecond)
	t.Fail()
}

func TestLoading(t *testing.T) {
	t.Parallel()
	time.Sleep(time.Second)
}

func TestLoading_abort(t *testing.T) {
	t.Parallel()
	time.Sleep(2500 * time.Millisecond)
}

func TestLoading_interrupt(t *testing.T) {
	t.Parallel()
	time.Sleep(80 * time.Millisecond)
}

func TestTracing(t *testing.T) {
	tt := trace_testing.WithTracing(t)
	tt.Parallel()

	tt.Run("test", func(t trace_testing.T) {
		time.Sleep(100 * time.Millisecond)

		t.Run("subtest", func(t trace_testing.T) {
			time.Sleep(100 * time.Millisecond)
		})
	})

	tt.Fail()
}
