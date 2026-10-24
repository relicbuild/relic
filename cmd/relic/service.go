package main

import (
	"context"
	"errors"
	"fmt"
)

// serviceManager runs relic server start as a background service under one
// init system, for one user or for the whole machine.
type serviceManager interface {
	initSystem() string
	// tool is the command that controls the init system.
	tool() string
	scope() string
	// definition is the file that declares the service.
	definition() string
	settingsPath() string
	// binary is the relic binary the installed definition runs.
	binary() string
	installActions(ctx context.Context) ([]machineAction, error)
	uninstallActions(ctx context.Context) (remove, keep []machineAction)
	status(ctx context.Context) (serviceState, error)
	stop(ctx context.Context) error
	// restart starts the service again on the settings file as it is now.
	restart(ctx context.Context) error
	startCommand() string
	statusCommand() string
	logsCommand() string
}

type serviceState struct {
	active   string
	detail   string
	enabled  string
	since    string
	pid      int
	restarts int
}

func (s serviceState) running() bool {
	return s.active != "" && s.active != "inactive" && s.active != "failed"
}

func (s serviceState) String() string {
	if s.detail == "" || s.detail == s.active {
		return s.active
	}

	return s.active + " (" + s.detail + ")"
}

// serviceManager is the init system of m.goos at the scope m.system selects.
func (m *machineSetup) serviceManager() (serviceManager, error) {
	return m.serviceManagerAt(m.system)
}

func (m *machineSetup) serviceManagerAt(system bool) (serviceManager, error) {
	switch m.goos {
	case "linux":
		return &systemd{
			m:      m,
			system: system,
		}, nil
	case "darwin":
		if system {
			return nil, errors.New(
				"on macOS, relic server setup runs the server as a launch agent of your user: run it without --system and sudo",
			)
		}

		return &launchd{
			m: m,
		}, nil
	default:
		return nil, fmt.Errorf(
			"relic server setup runs the server under systemd (Linux) or launchd (macOS); on %s, "+
				"run relic server start by hand",
			m.goos,
		)
	}
}

// installedService is the service relic server setup installed on this
// machine, the user's before the system's, or nil; it sets m.system to its scope.
func (m *machineSetup) installedService() serviceManager {
	for _, system := range []bool{
		false,
		true,
	} {
		svc, err := m.serviceManagerAt(system)
		if err == nil && exists(svc.definition()) {
			m.system = system

			return svc
		}
	}

	return nil
}
