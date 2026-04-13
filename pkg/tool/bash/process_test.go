package bash

import (
	"os/exec"
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
	_ = cmd.Wait()
}

func TestKillProcessTree_AlreadyExited(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	_ = cmd.Start()
	_ = cmd.Wait()

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

	killProcess(cmd.Process.Pid)
	_ = cmd.Wait()
}

func TestKillProcess_AlreadyExited(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	_ = cmd.Start()
	_ = cmd.Wait()

	killProcess(cmd.Process.Pid)
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
	_ = cmd.Wait()
}

func TestKillProcessTree_ESRCHPath(t *testing.T) {
	t.Parallel()
	// Start a process that exits immediately
	cmd := exec.Command("true")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	_ = cmd.Start()
	_ = cmd.Wait()

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
