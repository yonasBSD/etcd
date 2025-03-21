// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testutil

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// TODO: Replace with https://github.com/uber-go/goleak.

/*
CheckLeakedGoroutine verifies tests do not leave any leaky
goroutines. It returns true when there are goroutines still
running(leaking) after all tests.

	import "go.etcd.io/etcd/client/pkg/v3/testutil"

	func TestMain(m *testing.M) {
		testutil.MustTestMainWithLeakDetection(m)
	}

	func TestSample(t *testing.T) {
		RegisterLeakDetection(t)
		...
	}
*/
var normalizedRegexp = regexp.MustCompile(`\(0[0-9a-fx, ]*\)`)

func CheckLeakedGoroutine() bool {
	gs := interestingGoroutines()
	if len(gs) == 0 {
		return false
	}

	stackCount := make(map[string]int)
	for _, g := range gs {
		// strip out pointer arguments in first function of stack dump
		normalized := string(normalizedRegexp.ReplaceAll([]byte(g), []byte("(...)")))
		stackCount[normalized]++
	}

	fmt.Fprint(os.Stderr, "Unexpected goroutines running after all test(s).\n")
	for stack, count := range stackCount {
		fmt.Fprintf(os.Stderr, "%d instances of:\n%s\n", count, stack)
	}
	return true
}

// CheckAfterTest returns an error if AfterTest would fail with an error.
// Waits for go-routines shutdown for 'd'.
func CheckAfterTest(d time.Duration) error {
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	var bad string
	// Presence of these goroutines causes immediate test failure.
	badSubstring := map[string]string{
		").writeLoop(": "a Transport",
		"created by net/http/httptest.(*Server).Start": "an httptest.Server",
		"timeoutHandler":        "a TimeoutHandler",
		"net.(*netFD).connect(": "a timing out dial",
		").noteClientGone(":     "a closenotifier sender",
		").readLoop(":           "a Transport",
		".grpc":                 "a gRPC resource",
		").sendCloseSubstream(": "a stream closing routine",
	}

	var stacks string
	begin := time.Now()
	for time.Since(begin) < d {
		bad = ""
		goroutines := interestingGoroutines()
		if len(goroutines) == 0 {
			return nil
		}
		stacks = strings.Join(goroutines, "\n\n")

		for substr, what := range badSubstring {
			if strings.Contains(stacks, substr) {
				bad = what
			}
		}
		// Undesired goroutines found, but goroutines might just still be
		// shutting down, so give it some time.
		runtime.Gosched()
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("appears to have leaked %s:\n%s", bad, stacks)
}

// RegisterLeakDetection is a convenient way to register before-and-after code to a test.
// If you execute RegisterLeakDetection, you don't need to explicitly register AfterTest.
func RegisterLeakDetection(t TB) {
	if err := CheckAfterTest(10 * time.Millisecond); err != nil {
		t.Skip("Found leaked goroutined BEFORE test", err)
		return
	}
	t.Cleanup(func() {
		afterTest(t)
	})
}

// afterTest is meant to run in a defer that executes after a test completes.
// It will detect common goroutine leaks, retrying in case there are goroutines
// not synchronously torn down, and fail the test if any goroutines are stuck.
func afterTest(t TB) {
	// If test-failed the leaked goroutines list is hidding the real
	// source of problem.
	if !t.Failed() {
		if err := CheckAfterTest(1 * time.Second); err != nil {
			t.Errorf("Test %v", err)
		}
	}
}

func interestingGoroutines() (gs []string) {
	buf := make([]byte, 2<<20)
	buf = buf[:runtime.Stack(buf, true)]
	for _, g := range strings.Split(string(buf), "\n\n") {
		sl := strings.SplitN(g, "\n", 2)
		if len(sl) != 2 {
			continue
		}
		stack := strings.TrimSpace(sl[1])
		if stack == "" {
			continue
		}

		shouldSkip := func() bool {
			uninterestingMsgs := [...]string{
				"sync.(*WaitGroup).Done",
				"os.(*file).close",
				"os.(*Process).Release",
				"created by os/signal.init",
				"runtime/panic.go",
				"created by testing.RunTests",
				"created by testing.runTests",
				"created by testing.(*T).Run",
				"testing.Main(",
				"runtime.goexit",
				"go.etcd.io/etcd/client/pkg/v3/testutil.interestingGoroutines",
				"go.etcd.io/etcd/client/pkg/v3/logutil.(*MergeLogger).outputLoop",
				"github.com/golang/glog.(*loggingT).flushDaemon",
				"created by runtime.gc",
				"created by text/template/parse.lex",
				"runtime.MHeap_Scavenger",
				"rcrypto/internal/boring.(*PublicKeyRSA).finalize",
				"net.(*netFD).Close(",
				"testing.(*T).Run",
				"crypto/tls.(*certCache).evict",
			}
			for _, msg := range uninterestingMsgs {
				if strings.Contains(stack, msg) {
					return true
				}
			}
			return false
		}()

		if shouldSkip {
			continue
		}

		gs = append(gs, stack)
	}
	sort.Strings(gs)
	return gs
}

func MustCheckLeakedGoroutine() {
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()

	CheckAfterTest(5 * time.Second)

	// Let the other goroutines finalize.
	runtime.Gosched()

	if CheckLeakedGoroutine() {
		os.Exit(1)
	}
}

// MustTestMainWithLeakDetection expands standard m.Run with leaked
// goroutines detection.
func MustTestMainWithLeakDetection(m *testing.M) {
	v := m.Run()
	if v == 0 {
		MustCheckLeakedGoroutine()
	}
	os.Exit(v)
}
