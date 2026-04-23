package bash

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

func TestKillProcessTree(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	err := killProcessTree(cmd.Process.Pid)
	if err != nil {
		t.Errorf("killProcessTree() error: %v", err)
	}
	// Process was killed; Wait should return a signal-related error.
	if waitErr := cmd.Wait(); waitErr == nil {
		t.Error("expected non-nil error from cmd.Wait() after killProcessTree")
	}
}

func TestKillProcessTree_AlreadyExited(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait for 'true': %v", err)
	}

	err := killProcessTree(cmd.Process.Pid)
	if err != nil {
		t.Errorf("killProcessTree on exited process: %v", err)
	}
}

func TestKillProcess(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid

	killProcess(pid)
	// Process was killed; Wait should return a signal-related error.
	if waitErr := cmd.Wait(); waitErr == nil {
		t.Error("expected non-nil error from cmd.Wait() after killProcess")
	}

	// Verify process was killed by checking if it's still running
	_, err := os.Stat("/proc/" + strconv.Itoa(pid))
	if !os.IsNotExist(err) {
		t.Errorf("process %d may still be running", pid)
	}
}

func TestKillProcess_AlreadyExited(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait for 'true': %v", err)
	}

	// Killing already-exited process should not error
	killProcess(cmd.Process.Pid)
	// If we get here without panic, test passes
}

func TestKillProcessTree_Setsid(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getpgid error: %v", err)
	}

	myPgid, _ := syscall.Getpgid(0)
	if pgid == myPgid {
		t.Errorf("child pgid %d == parent pgid %d, Setpgid not working", pgid, myPgid)
	}

	_ = killProcessTree(cmd.Process.Pid)
	// Process was killed; Wait should return a signal-related error.
	if waitErr := cmd.Wait(); waitErr == nil {
		t.Error("expected non-nil error from cmd.Wait() after killProcessTree")
	}
}

func TestKillProcessTree_ESRCHPath(t *testing.T) {
	t.Parallel()
	// Start a process that exits immediately
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait for 'true': %v", err)
	}

	// Now killProcessTree: pgid may still be valid but Kill(-pgid, 0)
	// returns ESRCH (process group doesn't exist). This covers the
	// err != nil && err != syscall.ESRCH branch in killProcessTree.
	_, _ = syscall.Getpgid(cmd.Process.Pid)
	// Send SIGKILL to already-exited group — may get ESRCH
	// The key is that the function handles both paths gracefully.
	err := killProcessTree(cmd.Process.Pid)
	if err != nil && err != syscall.ESRCH {
		t.Errorf("unexpected error: %v", err)
	}
}
