package envurl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fetcher() *Fetcher {
	return &Fetcher{Patience: time.Second, wait: func(int) time.Duration { return time.Millisecond }}
}

func serve(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL
}

// The server's environment is a plain JSON object, returned as NAME=value.
func TestTheObjectBecomesSortedVariables(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ZETA":"z","ALPHA":"a=b c"}`))
	})

	vars, err := fetcher().Fetch(context.Background(), url)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if want := []string{"ALPHA=a=b c", "ZETA=z"}; !slices.Equal(vars, want) {
		t.Fatalf("variables = %q, want %q", vars, want)
	}
}

// Whatever serves the URL may still be starting; a 5xx is worth waiting out.
func TestAServerErrorIsRetried(t *testing.T) {
	var calls atomic.Int32
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"A":"1"}`))
	})

	vars, err := fetcher().Fetch(context.Background(), url)
	if err != nil || len(vars) != 1 {
		t.Fatalf("fetch = %q, %v; want one variable after retries", vars, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

// Throttled is not refused: the answer is a moment away.
func TestAThrottleIsRetried(t *testing.T) {
	var calls atomic.Int32
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"A":"1"}`))
	})

	if vars, err := fetcher().Fetch(context.Background(), url); err != nil || len(vars) != 1 {
		t.Fatalf("fetch = %q, %v; want one variable after a retry", vars, err)
	}
}

// A proxy from the environment never sees the variables. (Go never proxies
// loopback, so a local server could not show it.)
func TestTheClientIgnoresProxiesFromTheEnvironment(t *testing.T) {
	transport, ok := (&Fetcher{}).client().Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("the client would honour HTTP_PROXY")
	}
}

// A refusal will not change by asking again.
func TestARefusalFailsAtOnce(t *testing.T) {
	var calls atomic.Int32
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	})

	if _, err := fetcher().Fetch(context.Background(), url); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the 403", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestRetriesStopWhenPatienceRunsOut(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	f := fetcher()
	f.Patience = 20 * time.Millisecond
	if _, err := f.Fetch(context.Background(), url); err == nil {
		t.Fatal("fetch succeeded against a server that never answers")
	}
}

func TestOnlyStringValuesAreAccepted(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"PORT":8080}`))
	})

	if _, err := fetcher().Fetch(context.Background(), url); err == nil {
		t.Fatal("a number was accepted as a variable value")
	}
}

// "A=B" as a name would set a different variable than the one named.
func TestANameThatCannotBeAVariableIsRefused(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"A=B":"x"}`))
	})

	if _, err := fetcher().Fetch(context.Background(), url); err == nil {
		t.Fatal("a name containing '=' was accepted")
	}
}

// The URL may carry a credential; errors must not repeat it.
func TestErrorsDoNotRepeatTheURL(t *testing.T) {
	f := fetcher()
	f.Patience = time.Millisecond
	_, err := f.Fetch(context.Background(), "http://127.0.0.1:1/env?token=s3cret")
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("err = %v, want an error without the URL", err)
	}
}

func TestOnlyHTTPURLsAreAccepted(t *testing.T) {
	if _, err := fetcher().Fetch(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("a file URL was accepted")
	}
}
