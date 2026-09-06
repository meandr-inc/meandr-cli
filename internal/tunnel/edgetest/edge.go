// Package edgetest is a stand-in for the service, for tests: TLS, the
// Upgrade handshake, and a yamux client that opens streams.
//
// Test-only. Nothing here ships in the binary.
package edgetest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// Edge is a fake service listening on localhost.
type Edge struct {
	// Status to answer the Upgrade with. Zero means 101.
	Status int
	// Headers added to the response.
	Headers map[string]string

	listener net.Listener
	roots    *x509.CertPool

	mu       sync.Mutex
	sessions []*yamux.Session
	requests []*http.Request
}

// Start brings up an edge on a random port with a self-signed
// certificate. Closed when the test ends.
func Start(t *testing.T) *Edge {
	t.Helper()

	cert, roots := selfSigned(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"http/1.1"},
	})
	if err != nil {
		t.Fatalf("edgetest: listen: %v", err)
	}

	e := &Edge{listener: ln, roots: roots}
	go e.serve()
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// Addr is the host:port to dial.
func (e *Edge) Addr() string { return e.listener.Addr().String() }

// Roots trusts this edge's certificate, for Dialer.RootCAs.
func (e *Edge) Roots() *x509.CertPool { return e.roots }

// OpenStream opens one yamux stream, which is how the service asks for an
// MCP server.
func (e *Edge) OpenStream(t *testing.T) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		n := len(e.sessions)
		var sess *yamux.Session
		if n > 0 {
			sess = e.sessions[n-1]
		}
		e.mu.Unlock()

		if sess != nil {
			stream, err := sess.Open()
			if err == nil {
				return stream
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("edgetest: no session to open a stream on")
	return nil
}

// Sessions is how many connections the edge is holding.
func (e *Edge) Sessions() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.sessions)
}

// LastRequest is the most recent Upgrade request, for asserting headers.
func (e *Edge) LastRequest() *http.Request {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.requests) == 0 {
		return nil
	}
	return e.requests[len(e.requests)-1]
}

// GoAway asks every connection to wind down, as a draining node would.
func (e *Edge) GoAway() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range e.sessions {
		_ = s.GoAway()
	}
}

func (e *Edge) Close() error {
	e.mu.Lock()
	for _, s := range e.sessions {
		_ = s.Close()
	}
	e.sessions = nil
	e.mu.Unlock()
	return e.listener.Close()
}

func (e *Edge) serve() {
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			return
		}
		go e.handshake(conn)
	}
}

func (e *Edge) handshake(conn net.Conn) {
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		_ = conn.Close()
		return
	}

	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.mu.Unlock()

	status := e.Status
	if status == 0 {
		status = http.StatusSwitchingProtocols
	}

	resp := fmt.Sprintf("HTTP/1.1 %d %s\r\n", status, http.StatusText(status))
	if status == http.StatusSwitchingProtocols {
		resp += "Upgrade: meandr-tunnel/1\r\nConnection: Upgrade\r\n"
	} else {
		resp += "Content-Length: 0\r\n"
	}
	for k, v := range e.Headers {
		resp += k + ": " + v + "\r\n"
	}
	resp += "\r\n"

	if _, err := conn.Write([]byte(resp)); err != nil {
		_ = conn.Close()
		return
	}
	if status != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return
	}

	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.LogOutput = discard{}

	// The service is the yamux client: it opens streams, the cli accepts
	// them.
	sess, err := yamux.Client(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}

	e.mu.Lock()
	e.sessions = append(e.sessions, sess)
	e.mu.Unlock()
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("edgetest: key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("edgetest: cert: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: der})) {
		t.Fatal("edgetest: could not add the certificate to a pool")
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots
}
