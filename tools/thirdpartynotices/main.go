// Command thirdpartynotices writes the license texts of the modules linked
// into Relic's binaries and of the programs shipped beside them.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

const preamble = `THIRD-PARTY NOTICES

Relic is distributed with the software listed below. Each license text is
the one its authors publish, and the components it covers are named above it.
`

const separator = "--------------------------------------------------------------------------------\n"

var licenseFileName = regexp.MustCompile(`(?i)^(licen[cs]e|copying|notice|unlicense)`)

type component struct {
	Name    string
	Version string
	Dir     string
}

func (c component) label() string {
	return strings.TrimSpace(c.Name + " " + c.Version)
}

func (c component) licenseTexts() ([]string, error) {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return nil, fmt.Errorf("reading the files of %s: %w", c.label(), err)
	}

	var texts []string

	for _, entry := range entries {
		if entry.IsDir() || !licenseFileName.MatchString(entry.Name()) {
			continue
		}

		raw, err := os.ReadFile(filepath.Join(c.Dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s of %s: %w", entry.Name(), c.label(), err)
		}

		texts = append(texts, strings.TrimSpace(string(raw)))
	}

	if len(texts) == 0 {
		return nil, fmt.Errorf("%s has no license file in %s", c.label(), c.Dir)
	}

	return texts, nil
}

type section struct {
	Title      string
	Components []component
}

func (s section) render() (string, error) {
	labelsByText := map[string][]string{}

	for _, c := range s.Components {
		texts, err := c.licenseTexts()
		if err != nil {
			return "", err
		}

		for _, text := range texts {
			labelsByText[text] = append(labelsByText[text], c.label())
		}
	}

	texts := make([]string, 0, len(labelsByText))
	for text, labels := range labelsByText {
		slices.Sort(labels)

		texts = append(texts, text)
	}

	slices.SortFunc(texts, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(labelsByText[a][0], labelsByText[b][0]),
			cmp.Compare(a, b),
		)
	})

	var out strings.Builder

	out.WriteString(s.Title + "\n" + strings.Repeat("=", len(s.Title)) + "\n\n")

	for _, text := range texts {
		for _, label := range slices.Compact(labelsByText[text]) {
			out.WriteString("  " + label + "\n")
		}

		out.WriteString("\n" + text + "\n\n" + separator + "\n")
	}

	return out.String(), nil
}

type program struct {
	Name    string
	Module  string
	Version string
}

func parseProgram(argument string) (program, error) {
	name, moduleVersion, hasName := strings.Cut(argument, "=")
	module, version, hasVersion := strings.Cut(moduleVersion, "@")

	if !hasName || !hasVersion || name == "" || module == "" || version == "" {
		return program{}, fmt.Errorf("program %q is not name=module@version", argument)
	}

	return program{
		Name:    name,
		Module:  module,
		Version: version,
	}, nil
}

func (p program) component(ctx context.Context) (component, error) {
	raw, err := goOutput(ctx, os.TempDir(), nil, "mod", "download", "-json", p.Module+"@"+p.Version)
	if err != nil {
		return component{}, fmt.Errorf("downloading %s: %w", p.Name, err)
	}

	var downloaded struct {
		Dir string
	}
	if err := json.Unmarshal(raw, &downloaded); err != nil {
		return component{}, fmt.Errorf("reading the download of %s: %w", p.Name, err)
	}

	return component{
		Name:    p.Name + " (" + p.Module + ")",
		Version: p.Version,
		Dir:     downloaded.Dir,
	}, nil
}

func goOutput(ctx context.Context, dir string, environment []string, arguments ...string) ([]byte, error) {
	var stderr bytes.Buffer

	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = dir

	command.Env = append(os.Environ(), environment...)
	command.Stderr = &stderr

	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %w: %s", strings.Join(arguments, " "), err, strings.TrimSpace(stderr.String()))
	}

	return out, nil
}

func goDistribution(ctx context.Context) (component, error) {
	raw, err := goOutput(ctx, os.TempDir(), nil, "env", "GOROOT", "GOVERSION")
	if err != nil {
		return component{}, err
	}

	fields, err := goEnvValues(raw, 2)
	if err != nil {
		return component{}, err
	}

	return component{
		Name:    "Go standard library and runtime",
		Version: fields[1],
		Dir:     fields[0],
	}, nil
}

func linkedModules(ctx context.Context, mainPackageDir string, platforms []string) ([]component, error) {
	const format = "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}"

	var modules []component

	for _, platform := range platforms {
		operatingSystem, architecture, ok := strings.Cut(platform, "/")
		if !ok {
			return nil, fmt.Errorf("platform %q is not os/arch", platform)
		}

		raw, err := goOutput(
			ctx,
			mainPackageDir,
			[]string{
				"GOOS=" + operatingSystem,
				"GOARCH=" + architecture,
				"CGO_ENABLED=0",
			},
			"list", "-deps", "-f", format, ".",
		)
		if err != nil {
			return nil, fmt.Errorf("listing the modules of %s on %s: %w", mainPackageDir, platform, err)
		}

		for line := range strings.Lines(string(raw)) {
			fields := strings.Split(strings.TrimSpace(line), "\t")
			if len(fields) != 3 || fields[0] == "relic" || strings.HasPrefix(fields[0], "relic/") {
				continue
			}

			modules = append(modules, component{
				Name:    fields[0],
				Version: fields[1],
				Dir:     fields[2],
			})
		}
	}

	return modules, nil
}

func uniqueComponents(components []component) []component {
	slices.SortFunc(components, func(a, b component) int {
		return cmp.Or(
			cmp.Compare(a.Name, b.Name),
			cmp.Compare(a.Version, b.Version),
		)
	})

	return slices.Compact(components)
}

type options struct {
	output          string
	platforms       []string
	programs        []program
	includedFiles   []string
	mainPackageDirs []string
}

func parseOptions(arguments []string) (options, error) {
	var parsed options

	platforms := runtime.GOOS + "/" + runtime.GOARCH

	flags := flag.NewFlagSet("thirdpartynotices", flag.ContinueOnError)
	flags.StringVar(&parsed.output, "output", "", "the file to write")
	flags.StringVar(&platforms, "platforms", platforms, "comma-separated os/arch pairs the binaries are built for")
	flags.Func(
		"program",
		"a program shipped beside the binaries, as name=module@version (repeatable)",
		func(v string) error {
			p, err := parseProgram(v)
			parsed.programs = append(parsed.programs, p)

			return err
		},
	)
	flags.Func("include", "a file of notices to append as it is (repeatable)", func(v string) error {
		parsed.includedFiles = append(parsed.includedFiles, v)

		return nil
	})

	if err := flags.Parse(arguments); err != nil {
		return options{}, fmt.Errorf("parsing the arguments: %w", err)
	}

	parsed.platforms = strings.Split(platforms, ",")
	parsed.mainPackageDirs = flags.Args()

	if parsed.output == "" || len(parsed.mainPackageDirs) == 0 {
		return options{}, errors.New(
			"usage: thirdpartynotices -output FILE [-platforms os/arch,...] " +
				"[-program name=module@version] [-include FILE] MAIN_PACKAGE_DIR",
		)
	}

	return parsed, nil
}

func (o options) notices(ctx context.Context) (string, error) {
	distribution, err := goDistribution(ctx)
	if err != nil {
		return "", err
	}

	linked := section{
		Title: "Linked into the relic binaries",
		Components: []component{
			distribution,
		},
	}

	for _, dir := range o.mainPackageDirs {
		modules, err := linkedModules(ctx, dir, o.platforms)
		if err != nil {
			return "", err
		}

		linked.Components = append(linked.Components, modules...)
	}

	linked.Components = uniqueComponents(linked.Components)
	sections := []section{
		linked,
	}

	if len(o.programs) > 0 {
		shipped := section{
			Title: "Programs shipped in the image",
		}

		for _, p := range o.programs {
			c, err := p.component(ctx)
			if err != nil {
				return "", err
			}

			shipped.Components = append(shipped.Components, c)
		}

		sections = append(sections, shipped)
	}

	var out strings.Builder

	out.WriteString(preamble + "\n")

	for _, s := range sections {
		rendered, err := s.render()
		if err != nil {
			return "", err
		}

		out.WriteString(rendered)
	}

	for _, file := range o.includedFiles {
		raw, err := os.ReadFile(file) //nolint:gosec // a file the release build names
		if err != nil {
			return "", fmt.Errorf("reading the included notices: %w", err)
		}

		out.Write(raw)
	}

	return out.String(), nil
}

func main() {
	parsed, err := parseOptions(os.Args[1:])
	if err == nil {
		var text string
		if text, err = parsed.notices(context.Background()); err == nil {
			err = os.WriteFile(parsed.output, []byte(text), 0o644) //nolint:gosec // a public notices file
		}
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "thirdpartynotices:", err)
		os.Exit(1)
	}
}

func goEnvValues(raw []byte, want int) ([]string, error) {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
	if len(lines) != want {
		return nil, fmt.Errorf("go env answered %q, want %d values", raw, want)
	}

	return lines, nil
}
