//go:build linux

package container

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeProc is a /proc of PID 1 running init, meandr as self, and others.
type fakeProc struct {
	t    *testing.T
	root string
	sent []syscall.Signal
	// exits, once SIGTERM is sent, removes the others that heed it.
	exits bool
}

const self = 7

func newProc(t *testing.T, init string) *fakeProc {
	t.Helper()
	p := &fakeProc{t: t, root: t.TempDir()}
	p.add(1, "init", 'S')
	if err := os.WriteFile(filepath.Join(p.root, "1", "comm"), []byte(init+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p.add(self, "meandr", 'S')
	return p
}

func (p *fakeProc) add(pid int, comm string, state byte) {
	p.t.Helper()
	dir := filepath.Join(p.root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (" + comm + ") " + string(state) + " 1 1 1 0 -1"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *fakeProc) rest(parent int) *rest {
	return &rest{proc: p.root, self: self, parent: parent, reap: func() {}, sleep: func(time.Duration) {},
		kill: func(pid int, sig syscall.Signal) error {
			if pid != -1 {
				p.t.Fatalf("signalled %d, want -1", pid)
			}
			p.sent = append(p.sent, sig)
			if sig == syscall.SIGTERM && p.exits {
				entries, _ := os.ReadDir(p.root)
				for _, e := range entries {
					if e.Name() != "1" && e.Name() != strconv.Itoa(self) {
						_ = os.RemoveAll(filepath.Join(p.root, e.Name()))
					}
				}
			}
			return nil
		}}
}

// A daemon an MCP server left in a session of its own hears the stop, and
// one that exits is never killed.
func TestTheRestOfTheContainerHearsTheStopUnderDockersInit(t *testing.T) {
	p := newProc(t, "docker-init")
	p.add(42, "daemon", 'S')
	p.exits = true
	p.rest(1).stop(time.Second)
	if !slices.Equal(p.sent, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("sent %v, want SIGTERM alone", p.sent)
	}
}

func TestWhatOutlastsTheWaitIsKilled(t *testing.T) {
	p := newProc(t, "tini")
	p.add(42, "stubborn (worker) x", 'R')
	p.rest(1).stop(10 * time.Millisecond)
	if !slices.Equal(p.sent, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("sent %v, want SIGTERM then SIGKILL", p.sent)
	}
}

// On a host, or under a supervisor, meandr is no container's first process:
// signalling every process it may would reach the user's own.
func TestNothingIsSignalledOutsideAContainersFirstProcess(t *testing.T) {
	for name, tc := range map[string]struct {
		init   string
		parent int
	}{
		"a shell's child":      {"docker-init", 300},
		"systemd's child":      {"systemd", 1},
		"launched by any init": {"init", 1},
	} {
		p := newProc(t, tc.init)
		p.add(42, "other", 'S')
		p.rest(tc.parent).stop(time.Second)
		if len(p.sent) != 0 {
			t.Errorf("%s: sent %v", name, p.sent)
		}
	}
}

func TestZombiesAndAContainerAloneNeedNoSignal(t *testing.T) {
	p := newProc(t, "docker-init")
	p.add(42, "gone", 'Z')
	p.rest(1).stop(time.Second)
	if len(p.sent) != 0 {
		t.Fatalf("sent %v with only a zombie left", p.sent)
	}
}

func TestPID1StopsTheRestWhateverItsParent(t *testing.T) {
	p := newProc(t, "meandr")
	if err := os.RemoveAll(filepath.Join(p.root, strconv.Itoa(self))); err != nil {
		t.Fatal(err)
	}
	p.add(42, "daemon", 'S')
	p.exits = true
	r := p.rest(0)
	r.self = 1
	r.stop(time.Second)
	if !slices.Equal(p.sent, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("sent %v, want SIGTERM alone", p.sent)
	}
}
