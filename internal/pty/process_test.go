//go:build linux || darwin

package pty

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const within = 5 * time.Second

func start(t *testing.T, command string, args ...string) *Process {
	t.Helper()
	p, err := Start(Config{Command: command, Args: args})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

// peer is the service's end of one attached stream.
type peer struct {
	t     *testing.T
	conn  net.Conn
	ended chan error // Attach's result

	mu      sync.Mutex
	out     bytes.Buffer // the program's output, unframed
	badKind byte         // a frame the cli should never send
}

// attach attaches a stream whose output is read as it arrives.
func attach(t *testing.T, p *Process) *peer {
	pr := attachSilent(t, p)
	go pr.read()
	return pr
}

// attachSilent attaches a stream that nothing reads.
func attachSilent(t *testing.T, p *Process) *peer {
	t.Helper()
	ours, theirs := net.Pipe()
	pr := &peer{t: t, conn: theirs, ended: make(chan error, 1)}
	go func() { pr.ended <- p.Attach(context.Background(), ours) }()
	t.Cleanup(func() { _ = theirs.Close() })
	return pr
}

func (pr *peer) read() {
	fr := frameReader{r: pr.conn}
	for {
		f, err := fr.next()
		if err != nil {
			return
		}
		pr.mu.Lock()
		if f.kind == frameData {
			pr.out.Write(f.data)
		} else {
			pr.badKind = f.kind
		}
		pr.mu.Unlock()
	}
}

func (pr *peer) send(s string) {
	pr.t.Helper()
	if _, err := pr.conn.Write(dataFrame([]byte(s))); err != nil {
		pr.t.Fatalf("send: %v", err)
	}
}

func (pr *peer) resize(cols, rows uint16) {
	pr.t.Helper()
	f := []byte{frameResize, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(f[1:3], cols)
	binary.BigEndian.PutUint16(f[3:5], rows)
	if _, err := pr.conn.Write(f); err != nil {
		pr.t.Fatalf("resize: %v", err)
	}
}

// await waits for the output to hold want n times.
func (pr *peer) await(want string, n int) {
	pr.t.Helper()
	deadline := time.Now().Add(within)
	for {
		pr.mu.Lock()
		out, bad := pr.out.String(), pr.badKind
		pr.mu.Unlock()
		if bad != 0 {
			pr.t.Fatalf("the cli sent a frame of type %d", bad)
		}
		if strings.Count(out, want) >= n {
			return
		}
		if time.Now().After(deadline) {
			pr.t.Fatalf("the output never held %q %d times:\n%q", want, n, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (pr *peer) result() error {
	pr.t.Helper()
	select {
	case err := <-pr.ended:
		return err
	case <-time.After(within):
		pr.t.Fatal("the stream is still attached")
		return nil
	}
}

func viewers(p *Process) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.viewers)
}

func awaitViewers(t *testing.T, p *Process, n int) {
	t.Helper()
	deadline := time.Now().Add(within)
	for viewers(p) != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d viewers attached, want %d", viewers(p), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func running(t *testing.T, p *Process) {
	t.Helper()
	select {
	case <-p.Done():
		t.Fatalf("the program exited: %v", p.Err())
	default:
	}
}

// The echo comes from the terminal, then cat repeats the line.
func TestInputReachesTheProgramAndItsOutputTheStream(t *testing.T) {
	p := start(t, "cat")
	v := attach(t, p)

	v.send("ping\n")
	v.await("ping", 2)
}

func TestWithNobodyAttachedTheOutputNeverBlocksTheProgram(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "wrote")
	start(t, "sh", "-c", `head -c 1000000 /dev/zero; : > "$1"; sleep 60`, "sh", marker)

	deadline := time.Now().Add(within)
	for {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the program is still writing its output: nobody read the PTY")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A new viewer's first frame is its size, often the size already set; the
// program redraws for it either way.
func TestAResizeSignalsTheProgramEvenAtTheSameSize(t *testing.T) {
	p := start(t, "sh", "-c", `trap 'stty size' WINCH; read x; echo ready; while :; do sleep 0.1; done`)
	v := attach(t, p)
	v.send("go\n")
	v.await("ready", 1)

	v.resize(100, 30)
	v.await("30 100", 1)
	v.resize(100, 30)
	v.await("30 100", 2)
}

func TestAZeroSizeIsIgnored(t *testing.T) {
	p := start(t, "sh", "-c", `read x; stty size; cat`)
	v := attach(t, p)

	v.resize(0, 0)
	v.send("go\n")
	v.await("24 80", 1)
}

func TestTheProgramGetsATermWhenItHasNone(t *testing.T) {
	for _, tc := range []struct{ env, want string }{
		{"TERM=", "term=xterm-256color"},
		{"TERM=vt100", "term=vt100"},
	} {
		p, err := Start(Config{Command: "sh", Args: []string{"-c", `read x; echo "term=$TERM"; cat`}, Env: []string{tc.env}})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(func() { _ = p.Stop() })

		v := attach(t, p)
		v.send("go\n")
		v.await(tc.want, 1)
	}
}

func TestEveryViewerSeesTheOutputAndAnyCanType(t *testing.T) {
	p := start(t, "cat")
	a, b := attach(t, p), attach(t, p)
	awaitViewers(t, p, 2)

	a.send("one\n")
	a.await("one", 2)
	b.await("one", 2)

	b.send("two\n")
	a.await("two", 2)
	b.await("two", 2)
}

func TestABadFrameEndsOnlyItsStream(t *testing.T) {
	p := start(t, "cat")
	a, b := attach(t, p), attach(t, p)
	awaitViewers(t, p, 2)

	if _, err := a.conn.Write([]byte{9, 0, 0, 0, 0}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := a.result(); !errors.Is(err, errFrame) {
		t.Fatalf("Attach = %v, want a bad frame", err)
	}

	b.send("still\n")
	b.await("still", 2)
}

func TestDataOverTheCapEndsTheStream(t *testing.T) {
	p := start(t, "cat")
	v := attach(t, p)

	hdr := []byte{frameData, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(hdr[1:], maxData+1)
	if _, err := v.conn.Write(hdr); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := v.result(); !errors.Is(err, errFrame) {
		t.Fatalf("Attach = %v, want a bad frame", err)
	}
	running(t, p)
}

func TestTheProgramOutlivesItsStreams(t *testing.T) {
	p := start(t, "cat")
	a := attach(t, p)
	a.send("first\n")
	a.await("first", 2)

	_ = a.conn.Close()
	if err := a.result(); err != nil {
		t.Fatalf("Attach = %v, want nil for a closed stream", err)
	}
	running(t, p)

	b := attach(t, p)
	b.send("second\n")
	b.await("second", 2)
}

func TestAViewerThatStopsReadingIsDropped(t *testing.T) {
	prev := stallLimit
	stallLimit = 200 * time.Millisecond
	t.Cleanup(func() { stallLimit = prev })

	p := start(t, "yes")
	silent := attachSilent(t, p)
	if err := silent.result(); err != nil {
		t.Fatalf("Attach = %v, want nil for a dropped viewer", err)
	}
	running(t, p)

	v := attach(t, p)
	v.await("y", 1)
}

func TestTheProgramExitingEndsItsStreams(t *testing.T) {
	p := start(t, "sh", "-c", "read x; exit 3")
	v := attach(t, p)

	v.send("go\n")
	if err := v.result(); err != nil {
		t.Fatalf("Attach = %v, want nil", err)
	}
	select {
	case <-p.Done():
	case <-time.After(within):
		t.Fatal("the program is still running")
	}
	if err := p.Err(); err == nil || !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("Err = %v, want status 3", err)
	}
}

func TestAStreamOnAnExitedProgramEndsAtOnce(t *testing.T) {
	p := start(t, "true")
	<-p.Done()

	v := attach(t, p)
	if err := v.result(); err != nil {
		t.Fatalf("Attach = %v, want nil", err)
	}
}

// A program stopped as asked has not failed.
func TestStopEndsTheProgram(t *testing.T) {
	p := start(t, "sleep", "60")

	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop = %v, want nil", err)
		}
	case <-time.After(within):
		t.Fatal("Stop is still waiting")
	}
}

func TestFramesRoundTrip(t *testing.T) {
	stream := append(dataFrame([]byte("hello")), frameResize, 0, 120, 0, 40)
	stream = append(stream, dataFrame(nil)...)
	fr := frameReader{r: bytes.NewReader(stream)}

	for _, want := range []frame{
		{kind: frameData, data: []byte("hello")},
		{kind: frameResize, cols: 120, rows: 40},
		{kind: frameData, data: []byte{}},
	} {
		got, err := fr.next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if got.kind != want.kind || !bytes.Equal(got.data, want.data) || got.cols != want.cols || got.rows != want.rows {
			t.Fatalf("frame = %+v, want %+v", got, want)
		}
	}
}
