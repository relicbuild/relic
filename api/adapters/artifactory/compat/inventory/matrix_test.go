package main

import (
	"strings"
	"testing"
)

func TestWriteMatrix(t *testing.T) {
	t.Parallel()

	var b strings.Builder

	err := writeMatrix(&b, Inventory{
		JFVersion: "2.124.0",
		Commands: []Command{
			{
				Command:  "rt upload",
				Tier:     "A",
				Category: "files",
			},
			{
				Command:  "rt ping",
				Tier:     "A",
				Category: "platform",
			},
			{
				Command:  "worker deploy",
				Tier:     "C",
				Category: "workers",
			},
		},
	}, map[string]bool{
		"rt upload": true,
	})
	if err != nil {
		t.Fatal(err)
	}

	out := b.String()
	for _, want := range []string{
		"| Tier | Total | E2E |\n|---|---|---|\n",
		"| A | 2 | 1 |\n",
		"| C | 1 | 0 |\n",
		"| Command | Aliases | Category | Local | E2E |\n",
		"| `jf rt upload` |  | files | false | ✅ |\n",
		"| `jf rt ping` |  | platform | false |  |\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("matrix lacks %q:\n%s", want, out)
		}
	}

	for _, gone := range []string{
		"passing",
		"todo",
		"Status",
	} {
		if strings.Contains(out, gone) {
			t.Errorf("matrix still says %q", gone)
		}
	}

	err = writeMatrix(&b, Inventory{
		Commands: []Command{
			{
				Command: "rt ping",
				Tier:    "D",
			},
		},
	}, nil)
	if err == nil {
		t.Error("a command in an unknown tier was left out of the matrix without an error")
	}
}
