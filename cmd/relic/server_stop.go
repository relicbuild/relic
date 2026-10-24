package main

import (
	"context"
	"errors"
	"fmt"

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
		Long: "Stops the service relic server setup installed (systemd on Linux, launchd on macOS). It names " +
			"the service and asks first (--yes does not). The service stays installed: it starts again at the " +
			"next boot (at the next login for a launch agent), and the message names the command that starts it now.",
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
	svc := m.installedService()
	if svc == nil {
		return errors.New("no Relic service on this machine: install one with relic server setup")
	}

	state, err := svc.status(ctx)
	if err != nil {
		return fmt.Errorf("stop: read the service's state from %s: %w", svc.tool(), err)
	}

	if !state.running() {
		_, _ = fmt.Fprintf(m.out, "The Relic %s service is not running (%s).\n", svc.scope(), state.active)

		return nil
	}

	if !m.yes {
		question := "Stop the Relic " + svc.scope() + " service"
		if state.pid > 0 {
			question += fmt.Sprintf(" (pid %d)", state.pid)
		}

		ok, err := m.ask(question+"? Clients cannot reach it until it starts again. [y/N] ", false)
		if err != nil {
			return err
		}

		if !ok {
			return errors.New("stop: cancelled; the service is still running")
		}
	}

	if err := svc.stop(ctx); err != nil {
		return fmt.Errorf("stop the service: %w", err)
	}

	_, _ = fmt.Fprintf(
		m.out,
		"Stopped the Relic %s service. Start it again with: %s\n",
		svc.scope(),
		svc.startCommand(),
	)

	return nil
}
