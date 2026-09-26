package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/horyu1234/route-box/internal/control"
	"github.com/horyu1234/route-box/internal/service"
)

func newServiceCmd(configPath *string) *cobra.Command {
	svc := &cobra.Command{
		Use:   "service",
		Short: "Run RouteBox in the background at login (launchd on macOS, systemd --user on Linux)",
		Long: `Registers RouteBox as a per-user background service that starts at login and
restarts if it crashes, so the browser proxy is always there. Running
` + "`routebox`" + ` afterwards opens the TUI as a management panel for that service;
quitting the panel leaves the service running.`,
	}

	install := &cobra.Command{
		Use:   "install",
		Short: "Install and start the service (reinstalling replaces it)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := service.NewManager()
			if err != nil {
				return err
			}
			ld, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			if ld.err != nil {
				return fmt.Errorf("%w\n(fix or remove the config before installing the service)", ld.err)
			}
			cfgPath, err := filepath.Abs(ld.path)
			if err != nil {
				return err
			}
			exe, err := executablePath()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			// 서비스가 아닌 다른 인스턴스(터미널의 TUI 등)가 떠 있으면, 서비스는 그게
			// 끝날 때까지 재시도하다가 이어받는다.
			foreground := !m.Loaded() && instanceRunning(cmd.Context(), ld.path)
			if err := m.Install(m.Spec(exe, cfgPath, os.Getenv("PATH"))); err != nil {
				return err
			}
			fmt.Fprintf(out, "✓ Service installed: %s\n", m.DefinitionPath())
			if foreground {
				fmt.Fprintln(out, "  RouteBox is already running elsewhere (e.g. a TUI in another terminal).")
				fmt.Fprintln(out, "  The service takes over within about 10 seconds after you quit it.")
			} else if waitRunning(cmd.Context(), ld.path, 5*time.Second) {
				fmt.Fprintln(out, "✓ RouteBox is running in the background")
			} else {
				fmt.Fprintln(out, "  RouteBox has not answered yet; check `routebox service status` and the log.")
			}
			printServiceHints(out, m)
			return nil
		},
	}

	uninstall := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the service and remove it from login items",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := service.NewManager()
			if err != nil {
				return err
			}
			if !m.Installed() {
				fmt.Fprintln(cmd.OutOrStdout(), "The service is not installed.")
				return nil
			}
			if err := m.Uninstall(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✓ Service uninstalled")
			return nil
		},
	}

	action := func(use, short, done string, fn func(*service.Manager) error) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				m, err := service.NewManager()
				if err != nil {
					return err
				}
				if err := fn(m); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), done)
				return nil
			},
		}
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show whether the service is installed and running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := service.NewManager()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !m.Installed() {
				fmt.Fprintln(out, "Service    not installed (routebox service install)")
				return errSilent
			}
			ld, err := loadConfig(*configPath)
			if err != nil {
				return err
			}
			state := "stopped"
			if m.Loaded() {
				state = "loaded"
			}
			if instanceRunning(cmd.Context(), ld.path) {
				state = "running"
			}
			fmt.Fprintf(out, "Service    %s\n", state)
			fmt.Fprintf(out, "Definition %s\n", m.DefinitionPath())
			printServiceHints(out, m)
			if state != "running" {
				return errSilent
			}
			return nil
		},
	}

	svc.AddCommand(install, uninstall,
		action("start", "Start the installed service", "✓ Service started", (*service.Manager).Start),
		action("stop", "Stop the service until the next login (or `routebox service start`)", "✓ Service stopped", (*service.Manager).Stop),
		action("restart", "Restart the service (e.g. after replacing the binary)", "✓ Service restarted", (*service.Manager).Restart),
		status,
	)
	return svc
}

func printServiceHints(out io.Writer, m *service.Manager) {
	if p := m.LogPath(); p != "" {
		fmt.Fprintf(out, "Log        %s\n", p)
	} else {
		fmt.Fprintf(out, "Log        journalctl --user -u %s\n", service.Unit)
	}
	fmt.Fprintln(out, "Manage     routebox   (opens the TUI as a management panel; q leaves the service running)")
}

// executablePath 는 서비스에 기록할 바이너리 경로다. go run 이 만든 임시
// 바이너리는 곧 사라지므로 거부한다.
func executablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, string(filepath.Separator)+"go-build") {
		return "", errors.New("refusing to install a temporary `go run` binary; build or install routebox first")
	}
	return exe, nil
}

func instanceRunning(ctx context.Context, configPath string) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := control.NewClient(control.SocketPath(configPath)).Status(ctx)
	return err == nil
}

func waitRunning(ctx context.Context, configPath string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if instanceRunning(ctx, configPath) {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}
