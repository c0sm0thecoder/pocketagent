package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func logPath(home string) string  { return filepath.Join(home, "pocketagent.log") }
func lockPath(home string) string { return filepath.Join(home, "pocketagent.lock") }

// lockInstance makes sure only one bot polls Telegram per home directory;
// two pollers on one token fight over updates.
func lockInstance(home string) (*os.File, error) {
	f, err := os.OpenFile(lockPath(home), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, errors.Join(errors.New("pocketagent is already running (see `pocketagent status`)"), f.Close())
	}
	// The lock file also records the pid, for status and stop.
	if err := f.Truncate(0); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// runningPID returns the pid of the running bot, or 0.
func runningPID(home string) int {
	// Read-only is enough to test the lock.
	f, err := os.Open(lockPath(home))
	if err != nil {
		return 0
	}
	defer f.Close()
	// If we can take the lock, nobody holds it.
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // also released by Close
		return 0
	}
	data, _ := os.ReadFile(lockPath(home))
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func start(cfgPath, home string) error {
	if serviceInstalled() {
		return serviceCtl("start")
	}
	if pid := runningPID(home); pid != 0 {
		fmt.Printf("already running (pid %d)\n", pid)
		return nil
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logf, err := os.OpenFile(logPath(home), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(context.Background(), exe, "run", "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive the terminal closing
	err = cmd.Start()
	// The child has its own copy of the log file; ours can go.
	if cerr := logf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}

	// Give it a moment to load the config and connect.
	for range 20 {
		time.Sleep(250 * time.Millisecond)
		if runningPID(home) != 0 {
			fmt.Printf("started (pid %d), logs: %s\n", runningPID(home), logPath(home))
			return nil
		}
	}
	return fmt.Errorf("it exited right away; last log lines:\n%s", lastLogLines(home, 10))
}

func stop(home string) error {
	if serviceInstalled() {
		return serviceCtl("stop")
	}
	pid := runningPID(home)
	if pid == 0 {
		fmt.Println("not running")
		return nil
	}
	// SIGTERM lets runs stop cleanly; the bot kills agent processes on exit.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("stop pid %d: %w", pid, err)
	}
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		if runningPID(home) == 0 {
			fmt.Println("stopped")
			return nil
		}
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	fmt.Println("stopped (forced)")
	return nil
}

func status(home string) error {
	if pid := runningPID(home); pid != 0 {
		how := "background"
		if serviceInstalled() {
			how = "service"
		}
		fmt.Printf("running (pid %d, %s)\n", pid, how)
	} else {
		fmt.Println("not running")
	}
	if l := lastLogLines(home, 3); l != "" {
		fmt.Println(l)
	}
	return nil
}

func logs(home string) error {
	cmd := exec.CommandContext(context.Background(), "tail", "-n", "50", "-f", logPath(home))
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

func lastLogLines(home string, n int) string {
	data, err := os.ReadFile(logPath(home))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
