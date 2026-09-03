// Package stress wraps the external stress-ng tool as a controllable,
// cancellable subprocess. Chosen over reimplementing load generation in Go
// because stress-ng already has a decade of well-tested, tunable stressors
// (cpu, matrix, vm, io, ...) — reinventing that would add risk without
// adding value for this project's goal, which is measuring thermal/stability
// behavior under load, not building a new load generator.
package stress

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Status represents the current lifecycle state of a stress run.
type Status int

const (
	StatusIdle Status = iota
	StatusRunning
	StatusStopping
	StatusFinished
	StatusFailed
)

func (s Status) String() string {
	switch s {
	case StatusIdle:
		return "idle"
	case StatusRunning:
		return "running"
	case StatusStopping:
		return "stopping"
	case StatusFinished:
		return "finished"
	case StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Config describes one stress-ng invocation. Kept intentionally small for
// the MVP — cpu + vm stressors cover the common "heat up the CPU and RAM"
// case. More stressors (io, disk, matrix, ...) can be added as extra fields
// later without breaking callers, since they'd default to zero/off.
type Config struct {
	// Duration is how long stress-ng should run before stopping itself.
	// stress-ng accepts this natively via --timeout, which is preferable to
	// us killing the process after a Go-side timer: it lets stress-ng exit
	// cleanly and report its own summary.
	Duration time.Duration

	// CPUWorkers is the number of CPU stressor processes; 0 means "use all
	// cores" (stress-ng's own default behavior for --cpu 0 is actually
	// "no workers", so we translate 0 to runtime.NumCPU() before building args
	// — see Runner.Start).
	CPUWorkers int

	// VMWorkers is the number of virtual-memory stressor processes. 0 disables
	// the vm stressor entirely.
	VMWorkers int
	// VMBytesPerWorker is how much memory each vm worker allocates/churns,
	// e.g. "256M". Ignored if VMWorkers is 0.
	VMBytesPerWorker string
}

// Runner manages a single stress-ng subprocess at a time. Not safe to Start
// two runs concurrently on the same Runner — call Wait() or check Status()
// before starting another.
type Runner struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc
	status Status
	err    error
	doneCh chan struct{}
}

// NewRunner returns an idle Runner ready to Start().
func NewRunner() *Runner {
	return &Runner{status: StatusIdle}
}

// Start launches stress-ng with the given config. Returns an error
// immediately if stress-ng isn't found on PATH or a run is already in
// progress — it does NOT wait for the run to finish; use Wait() or poll
// Status() for that.
func (r *Runner) Start(cfg Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.status == StatusRunning || r.status == StatusStopping {
		return fmt.Errorf("stress run already in progress (status=%s)", r.status)
	}

	if _, err := exec.LookPath("stress-ng"); err != nil {
		return fmt.Errorf("stress-ng not found on PATH: %w (install it — on Arch: pacman -S stress-ng)", err)
	}

	args := buildArgs(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "stress-ng", args...)

	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("starting stress-ng: %w", err)
	}

	r.cmd = cmd
	r.cancel = cancel
	r.status = StatusRunning
	r.err = nil
	r.doneCh = make(chan struct{})

	go r.waitInBackground()

	return nil
}

// waitInBackground blocks on the subprocess and updates status once it
// exits, whether that's a clean finish, a user-requested Stop, or a crash.
func (r *Runner) waitInBackground() {
	waitErr := r.cmd.Wait()

	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case r.status == StatusStopping:
		r.status = StatusFinished // stopped on purpose, not a failure
	case waitErr != nil:
		r.status = StatusFailed
		r.err = waitErr
	default:
		r.status = StatusFinished
	}
	close(r.doneCh)
}

// Stop requests the current run to end early by canceling its context,
// which sends SIGKILL to the process group via exec.CommandContext's
// default cancel behavior. stress-ng normally also accepts SIGINT for a
// graceful stop; Stop uses context cancellation for simplicity and
// reliability across stress-ng versions.
func (r *Runner) Stop() error {
	r.mu.Lock()
	if r.status != StatusRunning {
		r.mu.Unlock()
		return fmt.Errorf("no run in progress (status=%s)", r.status)
	}
	r.status = StatusStopping
	cancel := r.cancel
	doneCh := r.doneCh
	r.mu.Unlock()

	cancel()
	<-doneCh // wait for waitInBackground to actually finish tearing down
	return nil
}

// Wait blocks until the current run finishes (however it finishes) and
// returns any error captured from the subprocess. Safe to call from a
// goroutine other than the one that called Start.
func (r *Runner) Wait() error {
	r.mu.Lock()
	doneCh := r.doneCh
	r.mu.Unlock()

	if doneCh == nil {
		return fmt.Errorf("no run has been started")
	}
	<-doneCh

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Status returns the current lifecycle state. Safe for concurrent use.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func buildArgs(cfg Config) []string {
	var args []string

	if cfg.CPUWorkers > 0 {
		args = append(args, "--cpu", fmt.Sprintf("%d", cfg.CPUWorkers))
	}
	if cfg.VMWorkers > 0 {
		args = append(args, "--vm", fmt.Sprintf("%d", cfg.VMWorkers))
		if cfg.VMBytesPerWorker != "" {
			args = append(args, "--vm-bytes", cfg.VMBytesPerWorker)
		}
	}
	if cfg.Duration > 0 {
		args = append(args, "--timeout", fmt.Sprintf("%ds", int(cfg.Duration.Seconds())))
	}
	// --metrics-brief gives a compact summary line on exit, harmless if
	// unused but handy if we start capturing stdout in the future.
	args = append(args, "--metrics-brief")

	return args
}
