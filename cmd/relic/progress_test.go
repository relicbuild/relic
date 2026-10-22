package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

func TestProgressModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		messages  []tea.Msg
		wantView  string
		wantEmpty bool
		wantDone  bool
	}{
		{
			name:     "the label beside the spinner",
			wantView: "Working",
		},
		{
			name: "a new label",
			messages: []tea.Msg{
				progressLabel("Copying files"),
			},
			wantView: "Copying files",
		},
		{
			name: "nothing while input is read",
			messages: []tea.Msg{
				progressPause(true),
			},
			wantEmpty: true,
		},
		{
			name: "the label again once the input is read",
			messages: []tea.Msg{
				progressPause(true),
				progressPause(false),
			},
			wantView: "Working",
		},
		{
			name: "nothing once the work has finished",
			messages: []tea.Msg{
				workFinished{},
			},
			wantEmpty: true,
			wantDone:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done := make(chan struct{})

			var model tea.Model = newProgressModel("Working", false, done)
			for _, message := range tt.messages {
				model, _ = model.Update(message)
			}

			view := model.View().Content
			if tt.wantEmpty != (view == "") || !strings.Contains(view, tt.wantView) {
				t.Errorf("view = %q, want it to hold %q (empty: %v)", view, tt.wantView, tt.wantEmpty)
			}

			if got := model.(progressModel).isDone; got != tt.wantDone {
				t.Errorf("isDone = %v, want %v", got, tt.wantDone)
			}
		})
	}
}

func TestProgressOutputWritesStraightThroughWithoutAProgram(t *testing.T) {
	t.Parallel()

	file, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = file.Close()
	})

	out := (&commandProgress{}).output(file)

	for _, text := range []string{
		"Password: ",
		"\nfirst line\nsecond",
	} {
		if _, err := out.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}

	written, err := os.ReadFile(file.Name())
	if err != nil || string(written) != "Password: \nfirst line\nsecond" {
		t.Errorf("the file holds %q, %v", written, err)
	}

	var none *commandProgress

	none.describe("nothing")
}

func TestProgressSkipsProgramWhenOutputIsNotATerminal(t *testing.T) {
	t.Parallel()

	file, err := os.Create(filepath.Join(t.TempDir(), "output"))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = file.Close()
	})

	var g globals

	ran := false

	if err := g.runWithProgress(&cobra.Command{}, func() error {
		ran = true

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if !ran {
		t.Error("runWithProgress did not execute run")
	}
}
