package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	relicv1 "relic/api/gen/relic/v1"
	"relic/internal/platform/localdisk"

	"relic/internal/platform/terminal"
)

type machineStatus struct {
	Service         string         `json:"service"`
	Unit            string         `json:"unit"`
	State           string         `json:"state,omitempty"`
	Enabled         string         `json:"enabled,omitempty"`
	Since           string         `json:"since,omitempty"`
	PID             int            `json:"pid,omitempty"`
	Restarts        int            `json:"restarts,omitempty"`
	Binary          string         `json:"binary,omitempty"`
	Settings        string         `json:"settings"`
	SettingsError   string         `json:"settingsError,omitempty"`
	EncryptionKey   *bool          `json:"encryptionKey,omitempty"`
	Address         string         `json:"address,omitempty"`
	Reachable       *bool          `json:"reachable,omitempty"`
	ServerVersion   string         `json:"serverVersion,omitempty"`
	Metrics         string         `json:"metrics,omitempty"`
	Database        storageStatus  `json:"database,omitzero"`
	Storage         storageStatus  `json:"storage,omitzero"`
	ScannerDatabase *storageStatus `json:"scannerDatabase,omitempty"`
	DiskFreeBytes   uint64         `json:"diskFreeBytes,omitempty"`
	DiskSizeBytes   uint64         `json:"diskSizeBytes,omitempty"`
	Completion      []string       `json:"completion,omitempty"`
	Healthy         bool           `json:"healthy"`
}

var remoteStorageKinds = []string{
	"postgresql",
	"s3",
	"azure",
	"gcs",
}

type storageStatus struct {
	Kind     string `json:"kind,omitempty"`
	Location string `json:"location,omitempty"`
	Files    int    `json:"files,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Error    string `json:"error,omitempty"`
	Missing  bool   `json:"missing,omitempty"`
}

func (m *machineSetup) machineStatus(ctx context.Context) *machineStatus {
	s := &machineStatus{
		Service:  "user",
		Unit:     filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user", "relic.service"),
		Settings: filepath.Join(m.configDir(), "server.env"),
	}
	systemctl := []string{
		"systemctl",
		"--user",
	}

	if !exists(s.Unit) {
		s.Service, s.Unit, s.Settings = "system", filepath.Join(
			m.root,
			systemUnit,
		), filepath.Join(
			m.root,
			systemSettings,
		)
		systemctl = []string{
			"systemctl",
		}
	}

	if !exists(s.Unit) {
		return nil
	}

	if unit, err := os.ReadFile(s.Unit); err == nil {
		for l := range strings.Lines(string(unit)) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(l), "ExecStart="); ok {
				s.Binary, _, _ = strings.Cut(v, " ")
			}
		}
	}

	if out, err := m.query(ctx, systemctl[0], append(systemctl[1:],
		"show",
		"relic.service",
		"--property=ActiveState,SubState,UnitFileState,MainPID,NRestarts,ActiveEnterTimestamp",
	)...); err == nil {
		p := keyValues(out, "=")
		s.State = p["ActiveState"]

		if sub := p["SubState"]; sub != "" && sub != s.State {
			s.State += " (" + sub + ")"
		}

		s.Enabled = p["UnitFileState"]
		s.Since = p["ActiveEnterTimestamp"]
		s.PID, _ = strconv.Atoi(p["MainPID"])
		s.Restarts, _ = strconv.Atoi(p["NRestarts"])
	}

	for _, p := range []string{
		filepath.Join(m.configDir(), "completion.bash"),
		filepath.Join(m.configDir(), "completion.zsh"),
		filepath.Join(m.xdg("XDG_CONFIG_HOME", ".config"), "fish", "completions", "relic.fish"),
		filepath.Join(m.root, systemCompletion),
	} {
		if exists(p) {
			s.Completion = append(s.Completion, p)
		}
	}

	settings, err := os.ReadFile(s.Settings)
	if err != nil {
		s.SettingsError = err.Error()

		return s
	}

	env := keyValues(string(settings), "=")
	s.EncryptionKey = new(env["RELIC_ENCRYPTION_KEY"] != "")
	s.Address = cmp.Or(env["RELIC_HTTP_LISTEN_ADDRESS"], defaultListenAddress)
	s.Metrics = cmp.Or(env["RELIC_METRICS_LISTEN_ADDRESS"], defaultMetricsListenAddress)

	if s.Metrics == "off" {
		s.Metrics = ""
	}

	s.Database = databaseStatus(env["RELIC_DATABASE_URL"])
	s.Storage = objectStorageStatus(env)

	if dir := env["RELIC_SCAN_VULNERABILITY_DATABASE_DIR"]; dir != "" {
		st := dirStatus("", dir)
		s.ScannerDatabase = &st
	}

	reachable := false
	if s.Address != "" {
		s.ServerVersion, reachable = probe(ctx, s.Address, m.localSettings().certificateAuthority(env))
	}

	s.Reachable = &reachable

	if dir := dataDirOf(env); dir != "" {
		s.DiskFreeBytes, s.DiskSizeBytes, _ = localdisk.Usage(dir)
	}

	s.Healthy = strings.HasPrefix(s.State, "active") && reachable

	return s
}

func keyValues(text, sep string) map[string]string {
	kv := map[string]string{}

	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}

		if k, v, ok := strings.Cut(l, sep); ok {
			kv[k] = strings.Trim(v, `"'`)
		}
	}

	return kv
}

func databaseStatus(dsn string) storageStatus {
	if path, ok := strings.CutPrefix(dsn, "sqlite://"); ok {
		st := storageStatus{
			Kind:     "sqlite",
			Location: path,
		}

		for _, p := range []string{
			path,
			path + "-wal",
		} {
			if fi, err := os.Stat(p); err == nil {
				st.Files++
				st.Bytes += fi.Size()
			} else if p == path {
				st.Error = err.Error()
			}
		}

		return st
	}

	u, err := url.Parse(dsn)
	if err != nil || dsn == "" {
		return storageStatus{}
	}

	return storageStatus{
		Kind:     "postgresql",
		Location: u.Host + u.Path,
	}
}

func objectStorageStatus(env map[string]string) storageStatus {
	kind := cmp.Or(env["RELIC_OBJECT_STORAGE_TYPE"], "s3")
	if kind == "filesystem" {
		return dirStatus(kind, env["RELIC_OBJECT_STORAGE_PATH"])
	}

	location := cmp.Or(env["RELIC_OBJECT_STORAGE_ARTIFACTS_BUCKET"], "artifacts")
	if endpoint := strings.TrimRight(env["RELIC_OBJECT_STORAGE_ENDPOINT"], "/"); endpoint != "" {
		location = endpoint + "/" + location
	}

	return storageStatus{
		Kind:     kind,
		Location: location,
	}
}

func dirStatus(kind, dir string) storageStatus {
	st := storageStatus{
		Kind:     kind,
		Location: dir,
	}

	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		st.Missing = true

		return st
	} else if err != nil {
		st.Error = err.Error()

		return st
	}

	st.Files, st.Bytes = usage(dir)

	return st
}

func dataDirOf(env map[string]string) string {
	if p, ok := strings.CutPrefix(env["RELIC_DATABASE_URL"], "sqlite://"); ok {
		return filepath.Dir(p)
	}

	return env["RELIC_OBJECT_STORAGE_PATH"]
}

func probe(ctx context.Context, listen, certificateAuthority string) (version string, ok bool) {
	url, ok := loopbackURL(listen)
	if !ok {
		return "", false
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	c := newClient(url, certificateAuthority, credentials{})

	v, err := c.system.GetVersion(ctx, connect.NewRequest(&relicv1.GetVersionRequest{}))
	if err != nil {
		return "", false
	}

	return v.Msg.GetVersion(), true
}

func (m *machineSetup) printMachineStatus(w io.Writer, s *machineStatus) {
	printStyled(w, panel("This machine", s.Service+" service · "+m.tilde(s.Unit), m.machineRows(s)))
}

func (m *machineSetup) machineRows(s *machineStatus) []panelRow {
	state := styleBad.Render("● " + s.State)

	switch {
	case s.State == "":
		state = styleWarn.Render("● unknown") + styleMuted.Render(" (systemctl gave no answer)")
	case strings.HasPrefix(s.State, "active"):
		state = styleGood.Render("● " + s.State)
	}

	var details []string

	if s.Since != "" && strings.HasPrefix(s.State, "active") {
		details = append(details, "since "+s.Since)
	}

	if s.PID > 0 {
		details = append(details, fmt.Sprintf("pid %d", s.PID))
	}

	if s.Enabled != "" {
		details = append(details, s.Enabled)
	}

	if len(details) > 0 {
		state += styleMuted.Render("  " + strings.Join(details, " · "))
	}

	if s.Restarts > 0 {
		state += "  " + styleWarn.Render(fmt.Sprintf("%d restarts", s.Restarts))
	}

	rows := []panelRow{
		{
			label: "server",
			value: state,
		},
	}

	if s.SettingsError != "" {
		return append(rows, panelRow{
			label: "settings",
			value: styleValue.Render(m.tilde(s.Settings)) + "  " +
				styleWarn.Render(s.SettingsError+"; run with sudo for the rest"),
		})
	}

	if s.Address != "" {
		address, ok := loopbackURL(s.Address)
		if !ok {
			address = "https://" + s.Address
		}

		rows = append(rows, panelRow{
			label: "address",
			value: styleValue.Render(address) + "  " +
				check(*s.Reachable, "Relic "+s.ServerVersion, "does not answer"),
		})
	}

	if s.Metrics != "" {
		address, ok := loopbackAddress(s.Metrics)
		if !ok {
			address = s.Metrics
		}

		rows = append(rows, panelRow{
			label: "metrics",
			value: styleValue.Render("http://" + address + "/metrics"),
		})
	}

	rows = append(rows,
		panelRow{
			label: "settings",
			value: styleValue.Render(m.tilde(s.Settings)) + "  " +
				check(*s.EncryptionKey, "encryption key set", "no encryption key"),
		},
		panelRow{
			label: "database",
			value: m.storageLine(s.Database, false),
		},
		panelRow{
			label: "files",
			value: m.storageLine(s.Storage, true),
		},
	)

	if s.ScannerDatabase != nil {
		rows = append(rows, panelRow{
			label: "scanner db",
			value: m.storageLine(*s.ScannerDatabase, false),
		})
	}

	if s.DiskSizeBytes > 0 {
		free := s.DiskFreeBytes * 100 / s.DiskSizeBytes

		style := styleGood
		if free < 10 {
			style = styleBad
		} else if free < 20 {
			style = styleWarn
		}

		rows = append(rows, panelRow{
			label: "disk",
			value: style.Render(fmt.Sprintf("%d%% free", free)) + styleMuted.Render(fmt.Sprintf(
				"  %s of %s",
				terminal.ByteSize(int64(s.DiskFreeBytes)), //nolint:gosec // disk sizes fit in int64
				terminal.ByteSize(int64(s.DiskSizeBytes)), //nolint:gosec // disk sizes fit in int64
			)),
		})
	}

	if s.Binary != "" {
		rows = append(rows, panelRow{
			label: "binary",
			value: styleValue.Render(m.tilde(s.Binary)),
		})
	}

	if len(s.Completion) > 0 {
		names := make([]string, 0, len(s.Completion))
		for _, p := range s.Completion {
			names = append(names, m.tilde(p))
		}

		rows = append(rows, panelRow{
			label: "completion",
			value: styleValue.Render(strings.Join(names, ", ")),
		})
	}

	return rows
}

func (m *machineSetup) storageLine(st storageStatus, files bool) string {
	line := styleMuted.Render(st.Kind+" ") + styleValue.Render(m.tilde(st.Location))
	if st.Kind == "" {
		line = styleValue.Render(m.tilde(st.Location))
	}

	switch {
	case st.Error != "":
		return line + "  " + styleBad.Render(st.Error)
	case st.Missing:
		return line + "  " + styleMuted.Render("not created yet")
	case slices.Contains(remoteStorageKinds, st.Kind):
		return line
	case files:
		return line + "  " + styleGood.Render(
			terminal.ByteSize(st.Bytes),
		) + styleMuted.Render(
			fmt.Sprintf(" in %d files", st.Files),
		)
	default:
		return line + "  " + styleGood.Render(terminal.ByteSize(st.Bytes))
	}
}
