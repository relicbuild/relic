package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func serverStopCmd(g *globals) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use: "stop",
		Annotations: map[string]string{
			progressAnnotation: "off",
		},
		Short: "Stop the Relic service on this machine, after asking",
		Long: "Stops the systemd service relic server setup installed. It names the service and asks first " +
			"(--yes does not). The service stays enabled: it starts again at the next boot, or with " +
			"systemctl start relic.service (--user for a user service).",
		Example: "  relic server stop\n" +
			"  relic server stop --yes\n" +
			"  sudo relic server stop   # a system service",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := newMachineSetup(cmd, g, g.getenv("HOME"), "/")
			if err != nil {
				return err
			}

			m.yes = yes

			return m.runStop(cmd.Context())
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before stopping the service")

	return cmd
}

func (m *machineSetup) runStop(ctx context.Context) error {
	service := "user"
	systemctl := []string{
		"systemctl",
		"--user",
	}

	if !exists(filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user", "relic.service")) {
		if !exists(filepath.Join(m.root, systemUnit)) {
			return errors.New("no Relic service on this machine: install one with relic server setup")
		}

		service = "system"
		systemctl = []string{
			"systemctl",
		}
	}

	out, err := m.query(ctx, systemctl[0], append(systemctl[1:],
		"show",
		"relic.service",
		"--property=ActiveState,MainPID",
	)...)
	if err != nil {
		return fmt.Errorf("stop: read the service's state from systemd: %w", err)
	}

	p := keyValues(out, "=")

	state := p["ActiveState"]
	if state == "inactive" || state == "failed" {
		_, _ = fmt.Fprintf(m.out, "The Relic %s service is not running (%s).\n", service, state)

		return nil
	}

	if !m.yes {
		question := "Stop the Relic " + service + " service"
		if pid := p["MainPID"]; pid != "" && pid != "0" {
			question += " (pid " + pid + ")"
		}

		ok, err := m.ask(question+"? Clients cannot reach it until it starts again. [y/N] ", false)
		if err != nil {
			return err
		}

		if !ok {
			return errors.New("stop: cancelled; the service is still running")
		}
	}

	if err := m.run(ctx, systemctl[0], append(systemctl[1:], "stop", "relic.service")...); err != nil {
		return fmt.Errorf("stop the service: %w", err)
	}

	_, _ = fmt.Fprintf(m.out, "Stopped the Relic %s service. Start it again with: %s start relic.service\n",
		service, strings.Join(systemctl, " "))

	return nil
}
