package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ErrToolMissing means an external command was not found on PATH. Callers turn
// this into a clear per-job error rather than a crash: a home server must keep
// serving its library when a distro upgrade removes ffmpeg.
var ErrToolMissing = errors.New("media: external tool not found on PATH")

// DefaultToolTimeout bounds any single external command.
const DefaultToolTimeout = 2 * time.Minute

// Runner executes external commands. It exists so tests can substitute a fake
// without touching PATH when that is more convenient.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands as argv arrays. It never invokes a shell, so
// no argument can be reinterpreted as a command.
type ExecRunner struct {
	Timeout time.Duration
}

// NewExecRunner returns a runner with the default timeout.
func NewExecRunner() *ExecRunner {
	return &ExecRunner{Timeout: DefaultToolTimeout}
}

// Run executes name with args and returns its standard output. The tool is
// resolved on PATH at call time, which is what lets tests stub it.
func (r *ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrToolMissing, name)
	}

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultToolTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Run the tool in its own process group so a timeout or cancellation kills
	// the whole tree, not just the direct child. Go's default Cancel behaviour
	// (cmd.Process.Kill) only signals the immediate child; a stub or wrapper
	// script that forks a grandchild instead of exec-replacing itself would
	// otherwise leave that grandchild running, reparented to PID 1.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("media: %s timed out or was cancelled: %s", name, detail)
		}
		return nil, fmt.Errorf("media: %s failed: %s", name, detail)
	}
	return stdout.Bytes(), nil
}
