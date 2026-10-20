//go:build linux || darwin

package pty

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
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
	answers []answer     // to pasted files
	badKind byte         // a frame the cli should never send
}

type answer struct {
	kind byte
	text string
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

// read takes the cli's frames: the program's output, and answers to files.
func (pr *peer) read() {
	hdr := make([]byte, headerLen)
	for {
		if _, err := io.ReadFull(pr.conn, hdr); err != nil {
			return
		}
		b := make([]byte, binary.BigEndian.Uint32(hdr[1:]))
		if _, err := io.ReadFull(pr.conn, b); err != nil {
			return
		}
		pr.mu.Lock()
		switch hdr[0] {
		case frameData:
			pr.out.Write(b)
		case frameFileSaved, frameFileFailed:
			pr.answers = append(pr.answers, answer{hdr[0], string(b)})
		default:
			pr.badKind = hdr[0]
		}
		pr.mu.Unlock()
	}
}

// sendFile sends a pasted file in frames of at most chunk bytes.
func (pr *peer) sendFile(name string, data []byte, chunk int) {
	pr.t.Helper()
	head := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	if _, err := pr.conn.Write(newFrame(frameFile, append(head, name...))); err != nil {
		pr.t.Fatalf("send file: %v", err)
	}
	for len(data) > 0 {
		n := min(chunk, len(data))
		if _, err := pr.conn.Write(newFrame(frameFileData, data[:n])); err != nil {
			pr.t.Fatalf("send file data: %v", err)
		}
		data = data[n:]
	}
}

func (pr *peer) awaitAnswer() answer {
	pr.t.Helper()
	deadline := time.Now().Add(within)
	for {
		pr.mu.Lock()
		got, bad := pr.answers, pr.badKind
		pr.mu.Unlock()
		if bad != 0 {
			pr.t.Fatalf("the cli sent a frame of type %d", bad)
		}
		if len(got) > 0 {
			return got[0]
		}
		if time.Now().After(deadline) {
			pr.t.Fatal("no answer to the pasted file")
		}
		time.Sleep(10 * time.Millisecond)
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
	stream = append(stream, newFrame(frameFile, append([]byte{0, 0, 1, 0}, "a.png"...))...)
	stream = append(stream, newFrame(frameFileData, []byte("png"))...)
	fr := frameReader{r: bytes.NewReader(stream)}

	for _, want := range []frame{
		{kind: frameData, data: []byte("hello")},
		{kind: frameResize, cols: 120, rows: 40},
		{kind: frameData, data: []byte{}},
		{kind: frameFile, size: 256, name: "a.png"},
		{kind: frameFileData, data: []byte("png")},
	} {
		got, err := fr.next()
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if got.kind != want.kind || !bytes.Equal(got.data, want.data) || got.cols != want.cols || got.rows != want.rows ||
			got.size != want.size || got.name != want.name {
			t.Fatalf("frame = %+v, want %+v", got, want)
		}
	}
}

func startWithStorage(t *testing.T, dir string) *Process {
	t.Helper()
	p, err := Start(Config{Command: "cat", Storage: dir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })
	return p
}

func TestAPastedFileLandsWholeAndTheAnswerSaysWhere(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "uploads")
	v := attach(t, startWithStorage(t, dir))

	data := bytes.Repeat([]byte("0123456789"), 15_000)
	v.sendFile("k3x9-shot.png", data, maxData)
	got := v.awaitAnswer()
	want := filepath.Join(dir, "k3x9-shot.png")
	if got.kind != frameFileSaved || got.text != want {
		t.Fatalf("answer = %d %q, want saved %q", got.kind, got.text, want)
	}
	if b, err := os.ReadFile(want); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("the file holds %d bytes (%v), want %d", len(b), err, len(data))
	}
	if st, err := os.Stat(want); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v (%v), want 0600", st.Mode().Perm(), err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("the directory holds %d entries, want the file alone", len(entries))
	}

	v.send("still typing\n")
	v.await("still typing", 2)
}

func TestAnEmptyFileIsSavedAtOnce(t *testing.T) {
	dir := t.TempDir()
	v := attach(t, startWithStorage(t, dir))

	v.sendFile("empty.txt", nil, maxData)
	if got := v.awaitAnswer(); got.kind != frameFileSaved || got.text != filepath.Join(dir, "empty.txt") {
		t.Fatalf("answer = %d %q, want saved", got.kind, got.text)
	}
}

func TestAStreamEndingMidFileLeavesNoFile(t *testing.T) {
	dir := t.TempDir()
	p := startWithStorage(t, dir)
	v := attach(t, p)

	head := binary.BigEndian.AppendUint32(nil, 10)
	if _, err := v.conn.Write(newFrame(frameFile, append(head, "half.bin"...))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := v.conn.Write(newFrame(frameFileData, []byte("12345"))); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = v.conn.Close()
	if err := v.result(); err != nil {
		t.Fatalf("Attach = %v, want nil", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the directory holds %v, want nothing", entries)
	}
	running(t, p)
}

// A name the proxy should never send is refused, and the stream goes on.
func TestANameThatIsNoPlainFileIsRefused(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "uploads")
	v := attach(t, startWithStorage(t, dir))

	for i, name := range []string{"../evil.sh", "a/b.png", ".bashrc", ""} {
		v.sendFile(name, []byte("x"), maxData)
		deadline := time.Now().Add(within)
		var a answer
		for {
			v.mu.Lock()
			n := len(v.answers)
			if n > i {
				a = v.answers[i]
			}
			v.mu.Unlock()
			if n > i {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%q: no answer", name)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if a.kind != frameFileFailed {
			t.Fatalf("%q: answer = %d %q, want failed", name, a.kind, a.text)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "evil.sh")); err == nil {
		t.Fatal("a file was written outside the directory")
	}
	v.send("still here\n")
	v.await("still here", 2)
}

// The directory being a file stands for any write that fails.
func TestAFileThatCannotBeWrittenIsAnsweredAndTheRestDropped(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "uploads")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	v := attach(t, startWithStorage(t, dir))

	v.sendFile("shot.png", bytes.Repeat([]byte("x"), 100_000), maxData)
	if got := v.awaitAnswer(); got.kind != frameFileFailed || got.text == "" {
		t.Fatalf("answer = %d %q, want failed with a reason", got.kind, got.text)
	}
	v.send("after\n")
	v.await("after", 2)
}

func TestFileFramesWithoutStorageEndTheStream(t *testing.T) {
	p := start(t, "cat")
	v := attach(t, p)

	head := binary.BigEndian.AppendUint32(nil, 1)
	if _, err := v.conn.Write(newFrame(frameFile, append(head, "shot.png"...))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := v.result(); !errors.Is(err, errFrame) {
		t.Fatalf("Attach = %v, want a bad frame", err)
	}
	running(t, p)
}

func TestFileDataPastItsFileEndsTheStream(t *testing.T) {
	dir := t.TempDir()
	v := attach(t, startWithStorage(t, dir))

	head := binary.BigEndian.AppendUint32(nil, 3)
	if _, err := v.conn.Write(newFrame(frameFile, append(head, "x.txt"...))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := v.conn.Write(newFrame(frameFileData, []byte("12345"))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := v.result(); !errors.Is(err, errFrame) {
		t.Fatalf("Attach = %v, want a bad frame", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the directory holds %v, want nothing", entries)
	}
}
