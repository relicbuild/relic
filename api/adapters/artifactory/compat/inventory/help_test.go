package main

import (
	"context"
	"os"
	"slices"
	"testing"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func findEntry(entries []helpEntry, name string) (helpEntry, bool) {
	i := slices.IndexFunc(entries, func(e helpEntry) bool {
		return e.Name == name
	})
	if i < 0 {
		return helpEntry{}, false
	}

	return entries[i], true
}

func TestParseHelpNamespaces(t *testing.T) {
	t.Parallel()

	tests := []struct {
		file         string
		name         string
		wantAliases  []string
		wantCategory string
	}{
		{
			"root.txt",
			"rt",
			nil,
			"Command Namespaces",
		},
		{
			"root.txt",
			"config",
			[]string{
				"c",
			},
			"Command Namespaces",
		},
		{
			"root.txt",
			"release-bundle-delete-local",
			[]string{
				"rbdell",
			},
			"Lifecycle",
		},
		{
			"root.txt",
			"setup",
			nil,
			"",
		},
		{
			"rt.txt",
			"ping",
			[]string{
				"p",
			},
			"",
		},
		{
			"rt.txt",
			"upload",
			[]string{
				"u",
			},
			"Files Management",
		},
		{
			"rt.txt",
			"build-add-dependencies",
			[]string{
				"bad",
			},
			"Build Info",
		},
	}
	for _, tt := range tests {
		t.Run(tt.file+"/"+tt.name, func(t *testing.T) {
			t.Parallel()
			page := parseHelp(readTestdata(t, tt.file))

			e, ok := findEntry(page.Subcommands, tt.name)
			if !ok {
				t.Fatalf("%s not found in %v", tt.name, page.Subcommands)
			}

			if !slices.Equal(e.Aliases, tt.wantAliases) || e.Category != tt.wantCategory {
				t.Errorf(
					"got aliases %v category %q, want %v %q",
					e.Aliases,
					e.Category,
					tt.wantAliases,
					tt.wantCategory,
				)
			}
		})
	}
}

func TestParseHelpSkipsHelpAndWrappedLines(t *testing.T) {
	t.Parallel()

	for _, file := range []string{
		"root.txt",
		"rt.txt",
		"config.txt",
	} {
		for _, e := range parseHelp(readTestdata(t, file)).Subcommands {
			if e.Name == "help" || e.Name == "as" || e.Name == "when" {
				t.Errorf("%s: unexpected entry %q", file, e.Name)
			}
		}
	}
}

func TestParseHelpFlags(t *testing.T) {
	t.Parallel()

	page := parseHelp(readTestdata(t, "rt-upload.txt"))
	if len(page.Subcommands) != 0 {
		t.Errorf("leaf has subcommands: %v", page.Subcommands)
	}

	for _, want := range []string{
		"flat",
		"recursive",
		"regexp",
		"archive",
		"explode",
		"sync-deletes",
		"target-props",
		"spec",
		"build-name",
	} {
		if !slices.Contains(page.Flags, want) {
			t.Errorf("flag --%s missing from %v", want, page.Flags)
		}
	}
}

func TestIsHelpFor(t *testing.T) {
	t.Parallel()

	if !isHelpFor(readTestdata(t, "rt-upload.txt")) {
		t.Error("rt upload help not recognised")
	}

	if !isHelpFor(readTestdata(t, "rt.txt")) {
		t.Error("rt namespace help not recognised")
	}

	if isHelpFor("'jf nope' is not a jf command. See --help") {
		t.Error("error text recognised as help")
	}
}

func TestProbeHiddenSkipsBlankCandidates(t *testing.T) {
	t.Parallel()

	upload := readTestdata(t, "rt-upload.txt")
	help := func(_ context.Context, args ...string) string {
		if slices.Equal(args, []string{
			"rt",
			"help",
			"upload",
		}) {
			return upload
		}

		return ""
	}

	got := probeHidden(t.Context(), help, []string{
		"",
		"   ",
		"rt upload",
	}, nil)
	if len(got) != 1 || got[0].Path != "rt upload" || !got[0].Hidden {
		t.Errorf("probeHidden = %+v, want rt upload alone", got)
	}
}
