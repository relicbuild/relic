package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"relic/internal/platform/terminal"
)

func uninstallCmd(g *globals) *cobra.Command {
	var (
		home, root         string
		yes, system, purge bool
	)

	cmd := &cobra.Command{
		Use: "uninstall",
		Annotations: map[string]string{
			progressAnnotation: "off",
		},
		Short: "Remove what relic server setup installed on this machine, keeping the server's data",
		Long: "Stops and removes the service (systemd on Linux, launchd on macOS), the shell completion and the " +
			"relic binary that relic server setup installed. The server's data, its settings (which hold the " +
			"encryption key), its certificate authority and the client config stay, unless --purge, whose question " +
			"defaults to no.",
		Example: "  relic server uninstall\n" +
			"  sudo relic server uninstall --system\n" +
			"  relic server uninstall --purge --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newMachineSetup(cmd, g, home, root)
			if err != nil {
				return err
			}

			m.yes, m.system = yes, system

			return m.runUninstall(cmd.Context(), purge)
		},
	}
	cmd.Flags().StringVar(&home, "home", userHome(), "the home directory user files are under")
	cmd.Flags().StringVar(&root, "root", "/", "the root directory system files are under")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before removing anything")
	cmd.Flags().
		BoolVar(&system, "system", false, "as root: remove the system service relic server setup --system installed")
	cmd.Flags().
		BoolVar(&purge, "purge", false, "also delete the server's data, its settings, its certificates and the client config")

	return cmd
}

func (m *machineSetup) runUninstall(ctx context.Context, purge bool) error {
	if _, err := m.serviceManager(); err != nil {
		return fmt.Errorf("uninstall: %w", err)
	}

	if m.system && m.euid != 0 {
		return errors.New("uninstall: --system removes files under /etc and /usr: run it with sudo")
	}

	remove, keep, err := m.uninstallPlan(ctx)
	if err != nil {
		return err
	}

	if purge {
		remove = append(remove, keep...)
		keep = nil
	}

	if len(remove) == 0 {
		_, _ = fmt.Fprintln(m.out, "relic server setup installed nothing here.")
		m.printKept(keep)

		return nil
	}

	m.printPlan("relic server uninstall will:", remove)

	if !m.yes {
		question, answer := "Proceed? [Y/n] ", true
		if purge {
			question, answer = "Proceed and delete the server's data? [y/N] ", false
		}

		ok, err := m.ask(question, answer)
		if err != nil {
			return err
		}

		if !ok {
			return errors.New("uninstall: cancelled; nothing was changed")
		}
	}

	for _, a := range remove {
		if err := m.apply(ctx, a); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintln(m.out, "Done.")
	m.printKept(keep)

	return nil
}

func (m *machineSetup) uninstallPlan(ctx context.Context) (remove, keep []machineAction, err error) {
	svc, err := m.serviceManager()
	if err != nil {
		return nil, nil, fmt.Errorf("uninstall: %w", err)
	}

	actions, serviceKeep := svc.uninstallActions(ctx)
	if m.system {
		return actions, serviceKeep, nil
	}

	for _, rc := range []string{
		".bashrc",
		".zshrc",
	} {
		if a, ok := m.withoutCompletionBlock(filepath.Join(m.home, rc)); ok {
			actions = append(actions, a)
		}
	}

	for _, p := range []string{
		filepath.Join(m.configDir(), "completion.bash"),
		filepath.Join(m.configDir(), "completion.zsh"),
		filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "fish", "completions", "relic.fish"),
	} {
		if exists(p) {
			actions = append(actions, removal("shell completion", p))
		}
	}

	if strings.HasPrefix(m.executable, m.home+"/") && !strings.Contains(m.executable, "go-build") {
		actions = append(actions, existing(removal("relic binary", m.executable))...)
	}

	return actions, append(existing(
		removal("client config: contexts and tokens", m.configPath),
		removal("server settings, with the encryption key", filepath.Join(m.configDir(), "server.env")),
		removal("the server's certificate authority and certificate", filepath.Join(m.configDir(), "tls")),
		removal("the server's database and files", m.dataDir()),
	), serviceKeep...), nil
}

func (m *machineSetup) withoutCompletionBlock(rc string) (machineAction, bool) {
	old, err := os.ReadFile(rc) //nolint:gosec // the user's shell rc file
	if err != nil {
		return machineAction{}, false
	}

	begin, end := blockMarkers("completion")

	i := strings.Index(string(old), begin+"\n")
	if i < 0 {
		return machineAction{}, false
	}

	j := strings.Index(string(old[i:]), end+"\n")
	if j < 0 {
		return machineAction{}, false
	}

	a := m.file("completion block", rc, append(old[:i:i], old[i+j+len(end)+1:]...), 0o644)
	if fi, err := os.Stat(rc); err == nil {
		a.mode = fi.Mode().Perm()
	}

	a.state = "update"

	return a, true
}

func (m *machineSetup) printKept(keep []machineAction) {
	kept := terminal.Table{
		Headers: []string{
			"KEPT",
			"WHAT",
			"FILES",
			"SIZE",
		},
		RightAligned: []int{
			2,
			3,
		},
	}

	for _, a := range keep {
		if a.command != nil {
			kept.Rows = append(kept.Rows, []string{
				"user " + systemUser,
				a.what,
				"",
				"",
			})

			continue
		}

		files, size := usage(a.path)
		if files == 0 {
			continue
		}

		kept.Rows = append(kept.Rows, []string{
			m.tilde(a.path),
			a.what,
			strconv.Itoa(files),
			terminal.ByteSize(size),
		})
	}

	if len(kept.Rows) == 0 {
		return
	}

	_, _ = fmt.Fprintln(m.out, "Kept on this machine (relic server uninstall --purge deletes them):")
	_ = kept.Print(m.out)
}

func removal(what, path string) machineAction {
	return machineAction{
		what:  what,
		path:  path,
		state: "remove",
	}
}

func existing(actions ...machineAction) []machineAction {
	var out []machineAction

	for _, a := range actions {
		if exists(a.path) {
			out = append(out, a)
		}
	}

	return out
}

func exists(path string) bool {
	_, err := os.Lstat(path)

	return err == nil
}

func usage(path string) (files int, size int64) {
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is left out of the count
		}

		files++

		if fi, err := d.Info(); err == nil {
			size += fi.Size()
		}

		return nil
	})

	return files, size
}
