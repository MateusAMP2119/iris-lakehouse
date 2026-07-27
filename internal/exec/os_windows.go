//go:build windows

package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// OSRunner is the real subprocess runner on Windows: it spawns an OS process,
// places it in a Job Object -- the Windows equivalent of a Unix process group --
// captures its output through the standard library, and terminates the whole job
// on Kill or context cancellation. It is the production implementation of the
// Runner seam, mirroring the unix runner in os_unix.go.
type OSRunner struct{}

// NewOSRunner returns the real subprocess runner.
func NewOSRunner() *OSRunner { return &OSRunner{} }

// compile-time proof the real runner satisfies the seam.
var _ Runner = (*OSRunner)(nil)

// waitDelay bounds the time cmd.Wait spends, after the subprocess has exited,
// waiting for the standard library's output copy goroutines to finish. Same
// rationale as the unix runner: a descendant that inherited the output pipe can
// keep it open indefinitely, so once the child is reaped os/exec waits at most
// this long for the copiers, then closes the pipes itself and Wait returns
// ErrWaitDelay.
const waitDelay = 2 * time.Second

// Start spawns spec as a direct exec (never a shell), assigns the new process to
// a fresh Job Object, and streams stdout and stderr to the spec's writers via the
// standard library. The returned Handle's PGID is the child's pid (Windows has no
// pgid; the pid is the recorded handle and the taskkill target for crash
// reconciliation). When ctx is cancelled the job is terminated.
//
// The child is assigned to the job immediately after start rather than created
// suspended, so a grandchild spawned in that instant can escape the job -- a
// best-effort window comparable to the pgid-recycling race the unix runner
// accepts.
func (r *OSRunner) Start(ctx context.Context, spec Spec) (Handle, error) {
	if len(spec.Argv) == 0 {
		return nil, errors.New("exec: empty argv")
	}
	cmd := osexec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	// Assign the writers directly: os/exec owns the pipes and copy goroutines,
	// dedups a shared stdout/stderr writer to a single copier, and closes the pipe
	// read end when a copy stops.
	cmd.Stdout = spec.Stdout
	cmd.Stderr = spec.Stderr
	cmd.Stdin = spec.Stdin
	// A new process group detaches the child from the console's Ctrl+C so only
	// the engine's explicit Kill (job termination) stops it.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	cmd.WaitDelay = waitDelay

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("exec: create job object: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("exec: start %v: %w", spec.Argv, err)
	}

	// Assign the child to the job so Kill reaches it and every descendant. A
	// child that already exited cannot be opened or assigned; that is not an
	// error -- the job is simply empty and Kill is a no-op.
	if proc, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid), //nolint:gosec // G115: a freshly started child's pid is positive and fits uint32.
	); err == nil {
		_ = windows.AssignProcessToJobObject(job, proc)
		_ = windows.CloseHandle(proc)
	}

	h := &osHandle{cmd: cmd, pgid: cmd.Process.Pid, job: job, done: make(chan struct{})}

	// Reap on a Start-owned goroutine so Handle.Wait can be called (or not)
	// freely; cmd.Wait bounds itself by waitDelay.
	go h.reap()

	// Cancelling ctx terminates the job. The watcher stays armed until reap.
	go func() {
		select {
		case <-ctx.Done():
			_ = h.Kill()
		case <-h.done:
		}
	}()

	return h, nil
}

// osHandle is a started OS subprocess and the Job Object holding its tree.
type osHandle struct {
	cmd     *osexec.Cmd
	pgid    int
	job     windows.Handle
	killed  atomic.Bool   // set before job termination so Wait reports Signaled
	done    chan struct{} // closed when the child is reaped and its status recorded
	status  ExitStatus
	waitErr error
}

// reap waits on the child, records its terminal status and any output-drain
// error, releases the job handle, then closes done. Closing the job handle does
// not terminate surviving descendants (no KILL_ON_JOB_CLOSE), matching the unix
// runner where a daemonizing descendant outlives the run unless killed.
func (h *osHandle) reap() {
	h.status, h.waitErr = h.translateWait(h.cmd.Wait())
	_ = windows.CloseHandle(h.job)
	close(h.done)
}

// PGID returns the run's recorded handle: on Windows, the child's pid.
func (h *osHandle) PGID() int { return h.pgid }

// Wait waits for the subprocess to be reaped and returns its exit status. A
// killed or non-zero termination is a terminal status, not an error; output
// drain past waitDelay or a destination-writer failure surfaces as a non-nil
// error alongside the recorded status.
func (h *osHandle) Wait() (ExitStatus, error) {
	<-h.done
	return h.status, h.waitErr
}

// Kill terminates every process in the job. An already-empty job is not an
// error -- termination of a job with no live members simply does nothing.
func (h *osHandle) Kill() error {
	h.killed.Store(true)
	if err := windows.TerminateJobObject(h.job, 1); err != nil {
		if errors.Is(err, windows.ERROR_INVALID_HANDLE) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil // job already released or empty
		}
		return fmt.Errorf("exec: terminate job for pid %d: %w", h.pgid, err)
	}
	return nil
}

// KillGroup best-effort terminates the process tree rooted at pid. It is the
// crash-reconciliation kill path: a restarting same-host leader has no live
// Handle (and so no Job Object) for a surviving run, only its recorded handle
// (runs.handle = pid), so it fells the tree with taskkill /T /F. An
// already-gone process is not an error -- the survivor may already have exited.
// The pid may in principle be recycled, the accepted best-effort trade shared
// with the unix runner's pgid path.
func KillGroup(pgid int) error {
	out, err := osexec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pgid)).CombinedOutput()
	if err == nil {
		return nil
	}
	// taskkill exits 128 when the pid does not exist: the tree is already gone.
	var ee *osexec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 128 {
		return nil
	}
	return fmt.Errorf("exec: kill tree %d: %s: %w", pgid, string(out), err)
}

// translateWait maps an os/exec Wait error into an ExitStatus plus the error the
// seam surfaces. An engine-initiated kill (Handle.Kill, including ctx
// cancellation) is reported as a signaled termination so dispatch distinguishes
// "killed" from "failed", mirroring the unix runner; Windows has no signals, so
// SIGKILL stands in as the recorded signal. A non-zero exit (an *ExitError) is a
// terminal status with no error; a destination-writer error or ErrWaitDelay is
// surfaced alongside the status.
func (h *osHandle) translateWait(err error) (ExitStatus, error) {
	st := statusOf(h.cmd.ProcessState)
	if h.killed.Load() {
		st = ExitStatus{Code: -1, Signaled: true, Signal: syscall.SIGKILL}
	}
	if err == nil {
		return st, nil
	}
	var ee *osexec.ExitError
	if errors.As(err, &ee) {
		return st, nil
	}
	return st, fmt.Errorf("exec: wait %v: %w", h.cmd.Args, err)
}

// statusOf reads the terminal ExitStatus from a finished process's state.
func statusOf(ps *os.ProcessState) ExitStatus {
	if ps == nil {
		return ExitStatus{}
	}
	return ExitStatus{Code: ps.ExitCode()}
}
