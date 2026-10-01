package main

import (
	"bytes"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const serviceLabel = "dev.pocketagent"

func serviceFile() string {
	home, _ := os.UserHomeDir()
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", serviceLabel+".plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", "pocketagent.service")
}

func serviceInstalled() bool {
	_, err := os.Stat(serviceFile())
	return err == nil
}

func sh(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}

func launchdTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) }

// serviceInstall registers pocketagent to start at login and restart if it
// crashes. The current PATH is baked in: service managers start with a
// minimal PATH that would not find claude, npx, whisper-cli or ffmpeg.
func serviceInstall(cfgPath, home string) error {
	if pid := runningPID(home); pid != 0 && !serviceInstalled() {
		fmt.Println("stopping the background instance first")
		stop(home)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, _ = filepath.EvalSymlinks(exe)
	cfgPath, _ = filepath.Abs(cfgPath)
	path := os.Getenv("PATH")
	if err := os.MkdirAll(filepath.Dir(serviceFile()), 0o755); err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		esc := html.EscapeString
		plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + serviceLabel + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>` + esc(exe) + `</string>
    <string>run</string>
    <string>--config</string>
    <string>` + esc(cfgPath) + `</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>` + esc(path) + `</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>StandardOutPath</key><string>` + esc(logPath(home)) + `</string>
  <key>StandardErrorPath</key><string>` + esc(logPath(home)) + `</string>
</dict>
</plist>
`
		if err := os.WriteFile(serviceFile(), []byte(plist), 0o644); err != nil {
			return err
		}
		sh("launchctl", "bootout", launchdTarget()+"/"+serviceLabel) // ignore "not loaded"
		if err := sh("launchctl", "bootstrap", launchdTarget(), serviceFile()); err != nil {
			return err
		}
	case "linux":
		unit := fmt.Sprintf(`[Unit]
Description=pocketagent: coding agents over Telegram
After=network-online.target

[Service]
ExecStart=%q run --config %q
Environment=PATH=%s
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, exe, cfgPath, path)
		if err := os.WriteFile(serviceFile(), []byte(unit), 0o644); err != nil {
			return err
		}
		if err := sh("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if err := sh("systemctl", "--user", "enable", "--now", "pocketagent"); err != nil {
			return err
		}
		fmt.Println("tip: run `loginctl enable-linger $USER` to keep it running while you're logged out")
	default:
		return fmt.Errorf("services are supported on macOS and Linux")
	}
	fmt.Printf("installed %s; pocketagent now starts at login and restarts if it crashes\n", serviceFile())
	return nil
}

func serviceUninstall() error {
	if !serviceInstalled() {
		fmt.Println("no service installed")
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		sh("launchctl", "bootout", launchdTarget()+"/"+serviceLabel)
	case "linux":
		sh("systemctl", "--user", "disable", "--now", "pocketagent")
	}
	if err := os.Remove(serviceFile()); err != nil {
		return err
	}
	fmt.Println("service removed")
	return nil
}

func serviceCtl(action string) error {
	switch runtime.GOOS {
	case "darwin":
		target := launchdTarget() + "/" + serviceLabel
		switch action {
		case "start":
			if err := sh("launchctl", "bootstrap", launchdTarget(), serviceFile()); err != nil {
				// Already loaded: just (re)start it.
				return sh("launchctl", "kickstart", "-k", target)
			}
		case "stop":
			// bootout, not kill: KeepAlive would restart a killed job.
			if err := sh("launchctl", "bootout", target); err != nil && !strings.Contains(err.Error(), "No such process") {
				return err
			}
		}
	case "linux":
		if err := sh("systemctl", "--user", action, "pocketagent"); err != nil {
			return err
		}
	}
	fmt.Printf("%s (service)\n", map[string]string{"start": "started", "stop": "stopped"}[action])
	return nil
}
