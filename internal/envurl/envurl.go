// Package envurl loads extra environment variables for the MCP server from a
// URL: a JSON object of names to string values, read once at start.
package envurl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/meandr-inc/meandr-cli/internal/version"
)

const (
	defaultPatience = time.Minute
	attemptTimeout  = 10 * time.Second
	backoffBase     = 500 * time.Millisecond
	backoffCap      = 8 * time.Second
	maxBody         = 1 << 20
)

// Fetcher reads the variables. The zero value is ready to use.
type Fetcher struct {
	// Client defaults to one with a per-attempt timeout.
	Client *http.Client
	// Patience bounds the retries; zero means a minute.
	Patience time.Duration

	wait func(attempt int) time.Duration
}

// Fetch returns the variables as NAME=value, sorted by name. An unreachable
// URL, a 5xx or a 429 is retried until Patience runs out; any other answer
// fails at once. Neither the URL nor a value is ever logged: either may be a
// secret.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) ([]string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("envurl: the URL must be http:// or https://")
	}

	deadline := time.Now().Add(f.patience())
	for attempt := 0; ; attempt++ {
		vars, retry, err := f.once(ctx, u.String())
		if err == nil {
			return vars, nil
		}
		if !retry || time.Now().After(deadline) {
			return nil, err
		}

		wait := f.backoff(attempt)
		slog.Warn("environment not loaded; retrying", slog.String("error", err.Error()), slog.Duration("in", wait))
		if !sleepCtx(ctx, wait) {
			return nil, ctx.Err()
		}
	}
}

func (f *Fetcher) once(ctx context.Context, rawURL string) ([]string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, errors.New("envurl: bad request")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", version.UserAgent())

	resp, err := f.client().Do(req)
	if err != nil {
		// url.Error repeats the URL, which may carry a secret.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, true, fmt.Errorf("envurl: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusInternalServerError || resp.StatusCode == http.StatusTooManyRequests {
		return nil, true, fmt.Errorf("envurl: the URL answered %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("envurl: the URL answered %d", resp.StatusCode)
	}

	var values map[string]string
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&values); err != nil {
		return nil, false, errors.New("envurl: the body is not a JSON object of strings")
	}

	vars := make([]string, 0, len(values))
	for name, value := range values {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, false, fmt.Errorf("envurl: %q is not a usable variable", name)
		}
		vars = append(vars, name+"="+value)
	}
	sort.Strings(vars)
	return vars, false, nil
}

// Never through a proxy: HTTP_PROXY baked into an image would get the
// variables, or break the start.
func (f *Fetcher) client() *http.Client {
	if f.Client != nil {
		return f.Client
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return &http.Client{Timeout: attemptTimeout, Transport: transport}
}

func (f *Fetcher) patience() time.Duration {
	if f.Patience > 0 {
		return f.Patience
	}
	return defaultPatience
}

// backoff is exponential with full jitter.
func (f *Fetcher) backoff(attempt int) time.Duration {
	if f.wait != nil {
		return f.wait(attempt)
	}
	d := backoffBase << attempt
	if d > backoffCap || d <= 0 {
		d = backoffCap
	}
	return time.Duration(rand.Int63n(int64(d)))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
