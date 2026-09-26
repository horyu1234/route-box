package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/horyu1234/route-box/internal/config"
	"github.com/horyu1234/route-box/internal/control"
	"github.com/horyu1234/route-box/internal/core"
	"github.com/horyu1234/route-box/internal/router"
	"github.com/horyu1234/route-box/internal/ssh"
	"github.com/horyu1234/route-box/internal/tui/components"
)

func newRootCmd() *cobra.Command {
	var (
		configPath string
		f          runFlags
	)
	root := &cobra.Command{
		Use:   "routebox",
		Short: "RouteBox — route chosen domains through chosen tunnels",
		Long: `RouteBox runs a local HTTP CONNECT proxy. Each domain you register is sent
through the upstream you pick (an SSH SOCKS5 tunnel or an existing SOCKS5
server), with hostnames resolved on the remote side. Everything else connects
directly. Without a subcommand it starts the TUI.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMain(configPath, f)
		},
	}
	root.PersistentFlags().StringVar(&configPath, "config", os.Getenv("ROUTEBOX_CONFIG"), "config file path (default: user config dir/routebox/config.json)")
	root.Flags().BoolVar(&f.noTUI, "no-tui", false, "run headless and log to stdout")
	root.Flags().StringVar(&f.listen, "listen", "", "HTTP proxy listen address for this run (not saved), e.g. 127.0.0.1:8080")
	root.Flags().StringVar(&f.socks, "socks", "", "SOCKS5 address of the first upstream for this run (not saved)")
	root.Flags().StringSliceVar(&f.presets, "preset", nil, "add a route preset before starting (see `routebox preset list`)")
	root.Flags().StringVar(&f.lang, "lang", "", "TUI language for this run: en or ko (default: saved setting, then $LANG)")

	root.AddCommand(
		newRouteCmd(&configPath),
		newUpstreamCmd(&configPath),
		newPresetCmd(&configPath),
		newSSHCmd(&configPath),
		newStatusCmd(&configPath),
	)
	return root
}

// session 은 제어 소켓으로 실행 중인 인스턴스에 접근하며, 아무것도 실행
// 중이지 않으면 같은 core 코드로 설정 파일에 폴백한다.
type session struct {
	client *control.Client
	app    *core.App
}

func openSession(ctx context.Context, configPath string) (*session, error) {
	ld, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}
	c := control.NewClient(control.SocketPath(ld.path))
	if _, err := c.Status(ctx); err == nil {
		return &session{client: c}, nil
	} else if !errors.Is(err, control.ErrNotRunning) {
		return nil, err
	}
	if ld.err != nil {
		return nil, fmt.Errorf("%w\n(the file was not modified)", ld.err)
	}
	return &session{app: core.New(core.Options{ConfigPath: ld.path, Config: ld.cfg, FirstRun: ld.firstRun})}, nil
}

func (s *session) running() bool { return s.client != nil }

func (s *session) note(cmd *cobra.Command) {
	if !s.running() {
		fmt.Fprintln(cmd.ErrOrStderr(), "(RouteBox is not running; saved to config, applies on next start)")
	}
}

// withSession 은 세션을 열어 fn 을 실행한 뒤, 실행 중이 아니었으면 그 사실을 알린다.
func withSession(configPath *string, fn func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
		defer cancel()
		s, err := openSession(ctx, *configPath)
		if err != nil {
			return err
		}
		if err := fn(ctx, cmd, s, args); err != nil {
			return err
		}
		s.note(cmd)
		return nil
	}
}

func viaLabel(r router.Route) string {
	switch v := r.Via(); v {
	case "":
		return "(first upstream)"
	case router.ViaDirect:
		return "DIRECT"
	default:
		return v
	}
}

func newRouteCmd(configPath *string) *cobra.Command {
	route := &cobra.Command{Use: "route", Short: "Choose which domains go through which upstream"}

	var via string
	add := &cobra.Command{
		Use:   "add <domain>",
		Short: "Route a domain (and its subdomains) through an upstream or direct",
		Args:  cobra.ExactArgs(1),
		RunE: withSession(configPath, func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error {
			var r router.Route
			var err error
			if s.running() {
				r, err = s.client.AddRoute(ctx, args[0], via)
			} else {
				r, err = s.app.AddRoute(args[0], via)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Route added: %s → %s\n", r.Domain, viaLabel(r))
			return nil
		}),
	}
	add.Flags().StringVar(&via, "via", "", "upstream name or \"direct\" (default: first upstream)")

	set := &cobra.Command{
		Use:   "via <domain> <upstream|direct>",
		Short: "Change where an existing route goes",
		Args:  cobra.ExactArgs(2),
		RunE: withSession(configPath, func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error {
			domain, err := router.NormalizeRouteInput(args[0])
			if err != nil {
				return err
			}
			var r router.Route
			if s.running() {
				r, err = s.client.SetRouteVia(ctx, domain, args[1])
			} else {
				r, err = s.app.SetRouteVia(domain, args[1])
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Route updated: %s → %s\n", r.Domain, viaLabel(r))
			return nil
		}),
	}

	remove := &cobra.Command{
		Use:     "remove <domain>",
		Aliases: []string{"rm", "delete"},
		Short:   "Stop routing a domain",
		Args:    cobra.ExactArgs(1),
		RunE: withSession(configPath, func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error {
			var r router.Route
			var err error
			if s.running() {
				r, err = s.client.RemoveRoute(ctx, args[0])
			} else {
				r, err = s.app.RemoveRoute(args[0])
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Route removed: %s\n", r.Domain)
			return nil
		}),
	}

	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List routes",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			s, err := openSession(ctx, *configPath)
			if err != nil {
				return err
			}
			routes := []router.Route{}
			if s.running() {
				routes, err = s.client.Routes(ctx)
			} else {
				routes = s.app.Routes()
			}
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), routes)
			}
			if len(routes) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No routes. Add one with: routebox route add example.com --via <upstream>")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "DOMAIN\tVIA")
			for _, r := range routes {
				fmt.Fprintf(tw, "%s\t%s\n", r.Domain, viaLabel(r))
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print JSON")

	route.AddCommand(add, set, remove, list)
	return route
}

func newUpstreamCmd(configPath *string) *cobra.Command {
	upstream := &cobra.Command{Use: "upstream", Aliases: []string{"upstreams"}, Short: "Manage named tunnels (SSH or SOCKS5) that routes can use"}

	var (
		u           config.Upstream
		externalFlg bool
		noReconnect bool
	)
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add an upstream: --host for managed SSH, or --external --socks for an existing SOCKS5 server",
		Example: `  routebox upstream add seoul --host proxy-seoul
  routebox upstream add tokyo --host tokyo.example.net --user me --identity ~/.ssh/id_ed25519
  routebox upstream add lab --external --socks 127.0.0.1:9050`,
		Args: cobra.ExactArgs(1),
		RunE: withSession(configPath, func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error {
			nu := u
			nu.Name = args[0]
			nu.Mode = config.SSHManaged
			if externalFlg {
				nu.Mode = config.SSHExternal
			}
			nu.Reconnect = !noReconnect
			if nu.Socks == "" {
				if externalFlg {
					return errors.New("--socks is required with --external")
				}
				cfg, err := sessionConfig(ctx, s, *configPath)
				if err != nil {
					return err
				}
				nu.Socks = core.SuggestSocks(cfg, "")
			}
			var added config.Upstream
			var err error
			if s.running() {
				added, err = s.client.AddUpstream(ctx, nu)
			} else {
				added, err = s.app.AddUpstream(nu)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Upstream added: %s (%s, SOCKS %s)\n", added.Name, describeUpstream(added), added.Socks)
			return nil
		}),
	}
	add.Flags().StringVar(&u.Host, "host", "", "ssh host or ~/.ssh/config alias (managed mode)")
	add.Flags().StringVar(&u.User, "user", "", "ssh user (default: from ~/.ssh/config)")
	add.Flags().IntVar(&u.Port, "port", 0, "ssh port (default: from ~/.ssh/config)")
	add.Flags().StringVar(&u.IdentityFile, "identity", "", "identity file path (only the path is stored)")
	add.Flags().StringVar(&u.Socks, "socks", "", "local SOCKS5 address (default: next free 127.0.0.1:10xx)")
	add.Flags().BoolVar(&externalFlg, "external", false, "use an existing SOCKS5 server instead of running ssh")
	add.Flags().BoolVar(&noReconnect, "no-reconnect", false, "do not restart ssh automatically when it exits")

	remove := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm", "delete"},
		Short:   "Remove an upstream that no route uses",
		Args:    cobra.ExactArgs(1),
		RunE: withSession(configPath, func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error {
			var err error
			if s.running() {
				err = s.client.RemoveUpstream(ctx, args[0])
			} else {
				err = s.app.RemoveUpstream(args[0])
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "✓ Upstream removed: %s\n", args[0])
			return nil
		}),
	}

	var asJSON bool
	list := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List upstreams and their state",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			c, ld, err := runningClient(ctx, *configPath)
			if err != nil {
				return err
			}
			var st *core.Status
			if c != nil {
				s, err := c.Status(ctx)
				if err != nil {
					return err
				}
				st = &s
			}
			if asJSON {
				if st != nil {
					return printJSON(cmd.OutOrStdout(), st.Upstreams)
				}
				return printJSON(cmd.OutOrStdout(), ld.cfg.Upstreams)
			}
			if len(ld.cfg.Upstreams) == 0 && (st == nil || len(st.Upstreams) == 0) {
				fmt.Fprintln(cmd.OutOrStdout(), "No upstreams. Add one with: routebox upstream add <name> --host <ssh-host>")
				return nil
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTYPE\tSOCKS\tSTATE\tROUTES")
			if st != nil {
				for _, u := range st.Upstreams {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", u.Name, describeUpstreamStatus(u), u.Socks, upstreamState(u), u.Routes)
				}
			} else {
				for _, u := range ld.cfg.Upstreams {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", u.Name, describeUpstream(u), u.Socks, "not running", ld.cfg.RoutesVia(u.Name))
				}
			}
			return tw.Flush()
		},
	}
	list.Flags().BoolVar(&asJSON, "json", false, "print JSON")

	upstream.AddCommand(add, remove, list)
	return upstream
}

func sessionConfig(ctx context.Context, s *session, configPath string) (config.Config, error) {
	if !s.running() {
		return s.app.Config(), nil
	}
	ups, err := s.client.Upstreams(ctx)
	if err != nil {
		return config.Config{}, err
	}
	cfg := config.Default()
	cfg.Upstreams = ups
	return cfg, nil
}

func describeUpstream(u config.Upstream) string {
	if u.Mode == config.SSHExternal {
		return "external SOCKS"
	}
	return "ssh " + u.Host
}

func describeUpstreamStatus(u core.UpstreamStatus) string {
	return describeUpstream(config.Upstream{Mode: u.Mode, Host: u.Host})
}

func upstreamState(u core.UpstreamStatus) string {
	switch {
	case u.Mode == config.SSHManaged && u.SSH.State == ssh.StateConnected:
		return fmt.Sprintf("connected (pid %d)", u.SSH.PID)
	case u.Mode == config.SSHManaged:
		s := u.SSH.State.String()
		if u.SSH.Err != "" {
			s += ": " + u.SSH.Err
		}
		return s
	case u.Health.Reachable:
		return "reachable"
	case u.Health.Checked.IsZero():
		return "checking"
	default:
		return "unreachable"
	}
}

func newPresetCmd(configPath *string) *cobra.Command {
	preset := &cobra.Command{Use: "preset", Aliases: []string{"presets"}, Short: "Add groups of related domains in one step"}
	var via string
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add all domains of a preset (see `routebox preset list`)",
		Args:  cobra.ExactArgs(1),
		RunE: withSession(configPath, func(ctx context.Context, cmd *cobra.Command, s *session, args []string) error {
			var added []router.Route
			var err error
			if s.running() {
				added, err = s.client.AddPreset(ctx, args[0], via)
			} else {
				added, err = s.app.AddPreset(args[0], via)
			}
			if err != nil {
				return err
			}
			if len(added) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "All %s routes are already present.\n", args[0])
				return nil
			}
			for _, r := range added {
				fmt.Fprintf(cmd.OutOrStdout(), "✓ Route added: %s → %s\n", r.Domain, viaLabel(r))
			}
			return nil
		}),
	}
	add.Flags().StringVar(&via, "via", "", "upstream name or \"direct\" (default: first upstream)")
	list := &cobra.Command{
		Use:   "list",
		Short: "Show available presets by category",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			printPresets(cmd.OutOrStdout())
			return nil
		},
	}
	preset.AddCommand(add, list)
	return preset
}

func printPresets(w io.Writer) {
	category := ""
	for _, p := range router.Presets() {
		if p.Category != category {
			if category != "" {
				fmt.Fprintln(w)
			}
			category = p.Category
			fmt.Fprintf(w, "%s\n", category)
		}
		fmt.Fprintf(w, "  %-11s %s\n", p.Name, strings.Join(p.Domains, ", "))
	}
}

func newSSHCmd(configPath *string) *cobra.Command {
	sshCmd := &cobra.Command{Use: "ssh", Short: "Inspect or restart managed SSH tunnels"}
	status := &cobra.Command{
		Use:   "status [upstream]",
		Short: "Show tunnel state and recent ssh output (all upstreams when omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			c, ld, err := runningClient(ctx, *configPath)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if c == nil {
				for _, u := range ld.cfg.Upstreams {
					if name == "" || u.Name == name {
						fmt.Fprintf(out, "%s  %s  SOCKS %s  not running\n", u.Name, describeUpstream(u), u.Socks)
					}
				}
				return errSilent
			}
			infos, err := c.SSH(ctx, name)
			if err != nil {
				return err
			}
			st, err := c.Status(ctx)
			if err != nil {
				return err
			}
			for i, info := range infos {
				if i > 0 {
					fmt.Fprintln(out)
				}
				u, _ := st.Upstream(info.Upstream)
				fmt.Fprintf(out, "%s  %s  SOCKS %s  %s\n", u.Name, describeUpstreamStatus(u), u.Socks, upstreamState(u))
				for _, l := range info.Log[max(0, len(info.Log)-10):] {
					fmt.Fprintln(out, "  "+l)
				}
			}
			return nil
		},
	}
	restart := &cobra.Command{
		Use:   "restart [upstream]",
		Short: "Restart managed SSH tunnels of the running instance (all when omitted)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			c, _, err := runningClient(ctx, *configPath)
			if err != nil {
				return err
			}
			if c == nil {
				return control.ErrNotRunning
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			if err := c.RestartSSH(ctx, name); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "✓ Restart requested")
			return nil
		},
	}
	sshCmd.AddCommand(status, restart)
	return sshCmd
}

func newStatusCmd(configPath *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the status of the running RouteBox",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()
			c, ld, err := runningClient(ctx, *configPath)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if c == nil {
				if asJSON {
					_ = printJSON(out, map[string]any{"running": false, "config": ld.path})
					return errSilent
				}
				fmt.Fprintln(out, "RouteBox   not running")
				fmt.Fprintf(out, "Config     %s\n", ld.path)
				fmt.Fprintf(out, "Upstreams  %d\n", len(ld.cfg.Upstreams))
				fmt.Fprintf(out, "Routes     %d\n", len(ld.cfg.Routes))
				return errSilent
			}
			st, err := c.Status(ctx)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(out, st)
			}
			up := "—"
			if !st.StartedAt.IsZero() {
				up = components.HumanDuration(st.Uptime().Truncate(time.Second))
			}
			fmt.Fprintf(out, "RouteBox   %s\n", st.Badge)
			fmt.Fprintf(out, "Proxy      %s\n", st.Proxy.Listen)
			fmt.Fprintf(out, "Uptime     %s\n", up)
			fmt.Fprintf(out, "Traffic    active %d · total %d (proxied %d, direct %d, failed %d) · RX %s · TX %s\n",
				st.Stats.Active, st.Stats.Total, st.Stats.Proxied, st.Stats.Direct, st.Stats.Failed,
				components.HumanBytes(st.Stats.RX), components.HumanBytes(st.Stats.TX))
			fmt.Fprintf(out, "Routes     %d (%d direct)\n", st.Routes, st.Direct)
			fmt.Fprintf(out, "Config     %s\n", ld.path)
			if len(st.Upstreams) > 0 {
				fmt.Fprintln(out)
				tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "UPSTREAM\tTYPE\tSOCKS\tSTATE\tROUTES")
				for _, u := range st.Upstreams {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n", u.Name, describeUpstreamStatus(u), u.Socks, upstreamState(u), u.Routes)
				}
				_ = tw.Flush()
			}
			for _, w := range st.Warnings {
				fmt.Fprintf(out, "\nWARNING:\n%s\n", w)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// runningClient 는 실행 중인 인스턴스의 client 를, 없으면 nil 을 돌려준다.
func runningClient(ctx context.Context, configPath string) (*control.Client, loaded, error) {
	ld, err := loadConfig(configPath)
	if err != nil {
		return nil, ld, err
	}
	c := control.NewClient(control.SocketPath(ld.path))
	if _, err := c.Status(ctx); err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return nil, ld, nil
		}
		return nil, ld, err
	}
	if ld.err != nil {
		ld.cfg = config.Default()
	}
	return c, ld, nil
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
