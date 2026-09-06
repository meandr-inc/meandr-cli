// Package tunnel opens and holds outbound connections to the service.
//
// Every connection is outbound; nothing here listens. The socket is
// established with an HTTP Upgrade, so failures arrive as status codes.
package tunnel

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/meandr-inc/meandr-cli/internal/version"
)

const (
	// Protocol is the Upgrade token.
	Protocol = "meandr-tunnel/1"

	// dialTimeout bounds TCP connect, TLS, and the Upgrade exchange.
	dialTimeout = 30 * time.Second

	defaultPort = "443"
)

// Info is what the service reports when the connection is accepted.
type Info struct {
	Node   string // node holding this connection
	Region string // its region
	Conns  int    // how many connections to keep open
}

// Fatal is a condition that redialling cannot fix.
type Fatal struct {
	Status int
	Reason string
}

func (e *Fatal) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Reason, e.Status) }

// Retryable is a temporary condition, with an optional backoff hint.
type Retryable struct {
	Status int
	Reason string
	After  time.Duration // 0 means the caller decides
}

func (e *Retryable) Error() string { return fmt.Sprintf("%s (HTTP %d)", e.Reason, e.Status) }

// IsFatal reports whether dialling should stop until the process restarts.
func IsFatal(err error) bool {
	var f *Fatal
	return errors.As(err, &f)
}

// Dialer opens connections to the service.
type Dialer struct {
	Endpoint string // host or host:port
	TunnelID string // tunnel to connect, as shown in the dashboard
	Token    string // bearer credential; never logged
	Instance string // identifies this process across its connections

	// RootCAs are trusted in place of the system roots. Nil, the normal
	// case, uses the system roots. There is no way to skip verification.
	RootCAs *x509.CertPool
}

// Dial opens one connection and returns the socket and the service's
// response. On error nothing is left open.
func (d *Dialer) Dial() (net.Conn, Info, error) {
	if d.Instance == "" {
		d.Instance = newInstanceID()
	}

	addr := d.Endpoint
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, defaultPort)
	}
	host, _, _ := net.SplitHostPort(addr)

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: dialTimeout},
		"tcp", addr,
		&tls.Config{
			ServerName: host,
			RootCAs:    d.RootCAs,
			MinVersion: tls.VersionTLS12,
			// Upgrade is an HTTP/1.1 mechanism; h2 has no equivalent.
			NextProtos: []string{"http/1.1"},
		})
	if err != nil {
		return nil, Info{}, fmt.Errorf("dial %s: %w", addr, err)
	}

	info, err := d.upgrade(conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, Info{}, err
	}
	return conn, info, nil
}

// upgrade performs the HTTP Upgrade exchange.
func (d *Dialer) upgrade(conn net.Conn, host string) (Info, error) {
	req, err := http.NewRequest(http.MethodPost, "/", nil)
	if err != nil {
		return Info{}, fmt.Errorf("build upgrade request: %w", err)
	}
	req.Host = host
	req.Header.Set("Upgrade", Protocol)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Authorization", "Bearer "+d.Token)
	req.Header.Set("Meandr-Tunnel-Id", d.TunnelID)
	req.Header.Set("Meandr-Tunnel-Instance", d.Instance)
	req.Header.Set("Meandr-Tunnel-Client",
		fmt.Sprintf("%s (%s/%s)", version.UserAgent(), runtime.GOOS, runtime.GOARCH))

	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	if err := req.Write(conn); err != nil {
		return Info{}, fmt.Errorf("write upgrade request: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return Info{}, fmt.Errorf("read upgrade response: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The connection is long-lived from here; a handshake deadline would
	// later close it.
	_ = conn.SetDeadline(time.Time{})

	return statusToInfo(resp)
}

func statusToInfo(resp *http.Response) (Info, error) {
	switch resp.StatusCode {
	case http.StatusSwitchingProtocols:
		if got := resp.Header.Get("Upgrade"); got != Protocol {
			return Info{}, &Fatal{Status: resp.StatusCode,
				Reason: fmt.Sprintf("switched to %q, not %q", got, Protocol)}
		}
		return Info{
			Node:   resp.Header.Get("Meandr-Node"),
			Region: resp.Header.Get("Meandr-Region"),
			Conns:  atoiOr(resp.Header.Get("Meandr-Tunnel-Connections"), 0),
		}, nil

	case http.StatusUnauthorized:
		return Info{}, &Fatal{Status: resp.StatusCode,
			Reason: "token rejected; it may have been revoked — check the dashboard"}

	case http.StatusNotFound:
		return Info{}, &Fatal{Status: resp.StatusCode,
			Reason: "no tunnel with this id; check --id against the dashboard"}

	case http.StatusUpgradeRequired:
		return Info{}, &Fatal{Status: resp.StatusCode,
			Reason: fmt.Sprintf("this build is too old; the service requires %s",
				resp.Header.Get("Upgrade"))}

	case http.StatusTooManyRequests:
		// Another process probably holds the connection slots.
		return Info{}, &Retryable{Status: resp.StatusCode,
			Reason: "connection limit reached for this server", After: 30 * time.Second}

	case http.StatusServiceUnavailable:
		// Another node will accept us, so retry at once.
		return Info{}, &Retryable{Status: resp.StatusCode,
			Reason: "node draining", After: 0}

	default:
		return Info{}, &Retryable{Status: resp.StatusCode, Reason: "unexpected response"}
	}
}

func newInstanceID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
}

func atoiOr(s string, fallback int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}
