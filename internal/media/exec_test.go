package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestStubToolsInstallsFakeExecutables(t *testing.T) {
	StubTools(t, map[string]string{
		"ffprobe": `#!/bin/sh
echo '{"streams":[{"width":640,"height":480}]}'`,
	})
	out, err := NewExecRunner().Run(context.Background(), "ffprobe", "-show_streams", "x.mp4")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), `"width":640`) {
		t.Errorf("output = %q, want the stub's JSON", out)
	}
}

func TestRunnerPassesArgumentsWithoutAShell(t *testing.T) {
	StubTools(t, map[string]string{
		// Echo each argument on its own line so we can prove no shell splitting
		// or globbing happened.
		"faketool": `#!/bin/sh
for a in "$@"; do echo "[$a]"; done`,
	})
	out, err := NewExecRunner().Run(context.Background(), "faketool",
		"an argument with spaces", "*", "$(rm -rf /)", "a;b")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.TrimSpace(string(out))
	want := "[an argument with spaces]\n[*]\n[$(rm -rf /)]\n[a;b]"
	if got != want {
		t.Errorf("arguments were mangled.\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestRunnerReportsAMissingTool(t *testing.T) {
	StubTools(t, map[string]string{}) // empty PATH directory
	_, err := NewExecRunner().Run(context.Background(), "definitely-not-installed")
	if err == nil {
		t.Fatal("Run succeeded for a missing tool, want an error")
	}
	if !errors.Is(err, ErrToolMissing) {
		t.Errorf("error = %v, want it to wrap ErrToolMissing", err)
	}
}

func TestRunnerReturnsStderrInTheError(t *testing.T) {
	StubTools(t, map[string]string{
		"failing": `#!/bin/sh
echo "something went badly wrong" >&2
exit 3`,
	})
	_, err := NewExecRunner().Run(context.Background(), "failing")
	if err == nil {
		t.Fatal("Run succeeded for a failing tool, want an error")
	}
	if !strings.Contains(err.Error(), "something went badly wrong") {
		t.Errorf("error = %v, want it to carry the tool's stderr", err)
	}
}

func TestRunnerHonoursItsTimeout(t *testing.T) {
	// StubTools replaces PATH wholesale, so a real "sleep" would fail to
	// resolve and the script would exit immediately instead of hanging (see
	// TestRunnerKillsTheWholeProcessGroupOnTimeout for the full explanation).
	// Spin on shell builtins only so the stub genuinely blocks until the
	// runner's timeout kills it.
	StubTools(t, map[string]string{
		"slow": `#!/bin/sh
while :; do :; done`,
	})
	runner := NewExecRunner()
	runner.Timeout = 150 * time.Millisecond

	start := time.Now()
	_, err := runner.Run(context.Background(), "slow")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Run succeeded for a hanging tool, want a timeout error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Run took %v; the timeout was not applied", elapsed)
	}
}

func TestRunnerHonoursCallerCancellation(t *testing.T) {
	// See TestRunnerHonoursItsTimeout: a real "sleep" can't resolve under the
	// stubbed PATH, so the stub has to block on shell builtins instead.
	StubTools(t, map[string]string{
		"slow": `#!/bin/sh
while :; do :; done`,
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := NewExecRunner().Run(ctx, "slow"); err == nil {
		t.Fatal("Run succeeded despite cancellation")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run took %v; cancellation was ignored", elapsed)
	}
}

// TestRunnerKillsTheWholeProcessGroupOnTimeout proves that a timeout doesn't
// just kill the direct child, it kills the whole process tree. A shell stub
// that forks a grandchild (rather than exec-replacing itself, as e.g. a
// wrapper script around ffmpeg might) leaves that grandchild running,
// reparented to PID 1, if Run only signals the immediate child on timeout.
//
// The grandchild is a backgrounded subshell spinning on shell builtins only
// (no external binary), because StubTools replaces PATH wholesale: a real
// "sleep" would fail to resolve and the script would exit instead of
// hanging. It records its own PID to a file before its parent shell blocks
// on it, so the test can poll /proc for that PID's state deterministically
// instead of sleeping a fixed interval or racing a real sleep duration.
//
// The grandchild redirects its own stdout/stderr to /dev/null before
// looping. Without that, it would keep holding open its inherited copy of
// Run's stdout/stderr pipes forever, and (independent of whether the
// process group gets killed) cmd.Wait() would block on pipe EOF that never
// comes, hanging this test instead of failing it. Detaching those fds
// isolates the assertion this test makes (was the grandchild killed?) from
// that separate, already-known pipe-inheritance hazard.
func TestRunnerKillsTheWholeProcessGroupOnTimeout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	StubTools(t, map[string]string{
		"slow": `#!/bin/sh
( exec >/dev/null 2>&1; while :; do :; done ) &
echo $! > "$1"
wait`,
	})

	runner := NewExecRunner()
	runner.Timeout = 150 * time.Millisecond

	if _, err := runner.Run(context.Background(), "slow", pidFile); err == nil {
		t.Fatal("Run succeeded for a hanging tool, want a timeout error")
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", pidBytes, err)
	}
	// Best-effort: if the assertion below fails (old, unfixed code), this
	// stops the orphaned busy loop from spinning for the rest of the test run.
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	deadline := time.Now().Add(time.Second)
	for {
		if processGoneOrZombie(t, pid) {
			return // the grandchild was killed: the fix works.
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild pid %d is still running %v after Run returned; "+
				"the process group was not killed on timeout", pid, time.Second)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// processGoneOrZombie reports whether pid has been terminated, whether or not
// it has been reaped yet by its (possibly reparented) parent. A zombie still
// has a /proc entry, but its State line proves it already exited, so treating
// zombie as "gone" avoids depending on how quickly the test's init process
// reaps orphans.
//
// Reading /proc/<pid>/status can fail two different ways once the task is
// exiting, and both mean "gone": ENOENT if the directory entry has already
// been removed, or ESRCH if the entry was still present at lookup time but
// the kernel's task_struct became invalid before the read completed (a
// documented procfs race, not specific to this process). Treating only
// ENOENT as gone left ESRCH to reach the Fatalf below, which is exactly the
// "no such process" flake seen under -race: the grandchild really was dead,
// the poll just observed it mid-teardown.
func processGoneOrZombie(t *testing.T, pid int) bool {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return true
	}
	if err != nil {
		t.Fatalf("read /proc/%d/status: %v", pid, err)
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "State:"); ok {
			return strings.Contains(rest, "Z (zombie)")
		}
	}
	return false
}

func TestExecRunnerSatisfiesRunner(t *testing.T) {
	var _ Runner = NewExecRunner()
}
