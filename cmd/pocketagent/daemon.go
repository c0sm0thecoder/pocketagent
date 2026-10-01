package main

import (
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

func pidPath(home string) string  { return filepath.Join(home, "pocketagent.pid") }
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
		f.Close()
		return nil, errors.New("pocketagent is already running (see `pocketagent status`)")
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return f, nil
}

// runningPID returns the pid of the running bot, or 0.
func runningPID(home string) int {
	f, err := os.OpenFile(lockPath(home), os.O_RDWR, 0o600)
	if err != nil {
		return 0
	}
	defer f.Close()
	// If we can take the lock, nobody holds it.
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
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
	defer logf.Close()
	cmd := exec.Command(exe, "run", "--config", cfgPath)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // survive the terminal closing
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Release()

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
	syscall.Kill(pid, syscall.SIGTERM)
	for range 40 {
		time.Sleep(250 * time.Millisecond)
		if runningPID(home) == 0 {
			fmt.Println("stopped")
			return nil
		}
	}
	syscall.Kill(pid, syscall.SIGKILL)
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
	cmd := exec.Command("tail", "-n", "50", "-f", logPath(home))
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
