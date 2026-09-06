package tunnel

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

const (
	// connsDefault is how many connections to hold when the service
	// suggests none. One: the service tracks a single address per
	// tunnel, and a second connection could land elsewhere. Every MCP
	// server process is a stream on this one connection.
	connsDefault = 1

	// connsMax caps the suggested count.
	connsMax = 8

	backoffBase = time.Second
	backoffCap  = 30 * time.Second
)

// Manager keeps a tunnel's connections open.
type Manager struct {
	Dialer *Dialer
	Spawn  Spawner
	Conns  int // desired; 0 = follow the service's suggestion

	mu     sync.Mutex
	advice int
}

// Run holds connections open until ctx is cancelled or a failure arrives
// that redialling cannot fix.
func (m *Manager) Run(ctx context.Context) error {
	target := m.Conns
	if target <= 0 {
		target = connsDefault
	}

	var wg sync.WaitGroup
	fatal := make(chan error, 1)

	for i := 0; i < target; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			if err := m.hold(ctx, slot); err != nil {
				select {
				case fatal <- err:
				default: // one report is enough
				}
			}
		}(i)
	}

	// A revoked token or an outdated binary fails every connection the
	// same way, so the first fatal error stops them all.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case err := <-fatal:
		return err
	case <-done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
}

// hold keeps one connection filled, redialling with backoff.
func (m *Manager) hold(ctx context.Context, slot int) error {
	var attempt int

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		conn, info, err := m.Dialer.Dial()
		if err != nil {
			// Not logged here: every connection fails the same way at the
			// same moment, and the caller reports it once.
			if IsFatal(err) {
				return err
			}

			wait := backoff(attempt)
			var retry *Retryable
			if errors.As(err, &retry) && retry.After > 0 {
				wait = retry.After
			}
			attempt++

			slog.Warn("connect failed; retrying",
				slog.String("error", err.Error()),
				slog.Duration("in", wait))
			if !sleepCtx(ctx, wait) {
				return ctx.Err()
			}
			continue
		}

		attempt = 0
		m.noteAdvice(info.Conns)

		tc, err := NewConn(conn, info, m.Spawn)
		if err != nil {
			_ = conn.Close()
			slog.Warn("could not start the session", slog.String("error", err.Error()))
			if !sleepCtx(ctx, backoff(0)) {
				return ctx.Err()
			}
			continue
		}

		if err := tc.Serve(ctx); err != nil {
			return err // only cancellation reaches here
		}
		slog.Debug("connection ended; redialling", slog.Int("slot", slot))
	}
}

// noteAdvice records the suggested connection count. It takes effect on
// the next start rather than tearing down healthy connections now.
func (m *Manager) noteAdvice(n int) {
	if n <= 0 {
		return
	}
	if n > connsMax {
		n = connsMax
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.advice != n {
		slog.Debug("suggested connections", slog.Int("connections", n))
		m.advice = n
	}
}

// backoff is exponential with full jitter, so that many clients
// reconnecting after an outage do not arrive together.
func backoff(attempt int) time.Duration {
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
