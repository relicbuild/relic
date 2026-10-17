package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var secretSetting = regexp.MustCompile(`(KEY|PASSWORD|SECRET|TOKEN)$`)

func serverConfigCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show the settings of the Relic service on this machine, secrets masked",
		Long: "Prints the settings file relic server setup wrote (server.env, or /etc/relic/relic.env for a system " +
			"service), with keys, passwords, secrets and tokens masked. relic server config set changes one " +
			"setting and restarts the service.",
		Example: "  relic server config\n" +
			"  relic server config set RELIC_HTTP_LISTEN_ADDRESS=0.0.0.0:8090",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _, err := serviceSettings(cmd, g)
			if err != nil {
				return err
			}

			b, err := os.ReadFile(path) //nolint:gosec // the settings file relic server setup wrote
			if err != nil {
				return fmt.Errorf("read %s: %w (a system service's settings need sudo)", path, err)
			}

			for l := range strings.Lines(string(b)) {
				key, _, ok := strings.Cut(strings.TrimSpace(l), "=")
				if ok && !strings.HasPrefix(key, "#") && secretSetting.MatchString(key) {
					l = key + "=********\n"
				}

				_, _ = fmt.Fprint(cmd.OutOrStdout(), l)
			}

			return nil
		},
	}
	cmd.AddCommand(serverConfigSetCmd(g))

	return cmd
}

func serverConfigSetCmd(g *globals) *cobra.Command {
	var valueStdin bool

	cmd := &cobra.Command{
		Use:   "set <KEY>=<value>",
		Short: "Change one setting of the Relic service on this machine and restart it",
		Long: "Writes the setting and restarts the service, also when the setting already has that value, " +
			"so the service runs with it. --value-stdin reads the value from standard input, which keeps " +
			"a secret out of the process list and the shell history.",
		Example: "  relic server config set RELIC_LOG_LEVEL=debug\n" +
			"  printf '%s' \"$PASSWORD\" | relic server config set RELIC_ADMIN_PASSWORD --value-stdin",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value, err := settingArgument(args[0], valueStdin, cmd.InOrStdin())
			if err != nil {
				return err
			}

			path, m, err := serviceSettings(cmd, g)
			if err != nil {
				return err
			}

			return m.setServiceSetting(cmd.Context(), cmd.OutOrStdout(), path, key, value)
		},
	}
	cmd.Flags().
		BoolVar(&valueStdin, "value-stdin", false, "read the value from standard input; the argument is the key")

	return cmd
}

func settingArgument(arg string, valueStdin bool, in io.Reader) (string, string, error) {
	key, value, ok := strings.Cut(arg, "=")

	if valueStdin {
		if ok {
			return "", "", errors.New("with --value-stdin, give the key alone")
		}

		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", fmt.Errorf("read the value: %w", err)
		}

		value, ok = strings.TrimRight(line, "\r\n"), true
	}

	if !ok || !strings.HasPrefix(key, "RELIC_") {
		return "", "", errors.New("expected RELIC_<NAME>=<value>, or RELIC_<NAME> with --value-stdin")
	}

	return key, value, nil
}

func (m *machineSetup) setServiceSetting(ctx context.Context, w io.Writer, path, key, value string) error {
	b, err := os.ReadFile(path) //nolint:gosec // the settings file relic server setup wrote
	if err != nil {
		return fmt.Errorf("read %s: %w (a system service's settings need sudo)", path, err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	a := m.file("server settings", path, []byte(withSetting(string(b), key, value, true)), fi.Mode().Perm())

	done := "Set " + key + " and restarted the service."
	if a.state == "unchanged" {
		done = key + " is already set to that; restarted the service."
	} else if err := m.apply(ctx, a); err != nil {
		return err
	}

	systemctl := []string{
		"systemctl",
		"--user",
	}
	if m.system {
		systemctl = []string{
			"systemctl",
		}

		if err := m.run(ctx, "chown", "root:"+systemUser, path); err != nil {
			return fmt.Errorf("give the relic group the settings: %w", err)
		}
	}

	if err := m.run(
		ctx,
		systemctl[0],
		append(systemctl[1:], "restart", "relic.service")...); err != nil {
		return fmt.Errorf("restart the service: %w", err)
	}

	_, _ = fmt.Fprintln(w, done)

	return nil
}

func serviceSettings(cmd *cobra.Command, g *globals) (string, *machineSetup, error) {
	m, err := newMachineSetup(cmd, g, g.getenv("HOME"), "/")
	if err != nil {
		return "", nil, err
	}

	if exists(filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user", "relic.service")) {
		return filepath.Join(m.configDir(), "server.env"), m, nil
	}

	if exists(filepath.Join(m.root, systemUnit)) {
		m.system = true

		return filepath.Join(m.root, systemSettings), m, nil
	}

	return "", nil, errors.New("no Relic service on this machine: install one with relic server setup")
}
