package main

import (
	"bytes"
	"os"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"relic/internal/platform/terminal"
)

const (
	progressAnnotation = "relic/progress"
	progressDelay      = 300 * time.Millisecond
)

type (
	workFinished  struct{}
	progressLabel string
	progressPause bool
)

type progressModel struct {
	spinner  spinner.Model
	label    string
	width    int
	isPaused bool
	isDone   bool
	done     <-chan struct{}
}

func newProgressModel(label string, isPaused bool, done <-chan struct{}) progressModel {
	return progressModel{
		spinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(terminal.Primary)),
		),
		label:    label,
		isPaused: isPaused,
		done:     done,
	}
}

func (m progressModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, func() tea.Msg {
		<-m.done

		return workFinished{}
	})
}

func (m progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case workFinished:
		m.isDone = true

		return m, tea.Quit
	case progressLabel:
		m.label = string(msg)
	case progressPause:
		m.isPaused = bool(msg)
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case spinner.TickMsg:
		var next tea.Cmd

		m.spinner, next = m.spinner.Update(msg)

		return m, next
	}

	return m, nil
}

func (m progressModel) View() tea.View {
	if m.isDone || m.isPaused {
		return tea.NewView("")
	}

	line := m.spinner.View() + m.label
	if m.width > 0 {
		line = lipgloss.NewStyle().MaxWidth(m.width).Render(line)
	}

	return tea.NewView(line)
}

type commandProgress struct {
	mu        sync.Mutex
	program   *tea.Program
	label     string
	isReading bool
	outputs   []*progressOutput
}

func (p *commandProgress) describe(label string) {
	if p == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.label = label
	if p.program != nil {
		p.program.Send(progressLabel(label))
	}
}

func (p *commandProgress) read(isReading bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.isReading = isReading
	if p.program != nil {
		p.program.Send(progressPause(isReading))
	}
}

func (p *commandProgress) output(file *os.File) *progressOutput {
	out := &progressOutput{
		File:     file,
		progress: p,
	}
	p.outputs = append(p.outputs, out)

	return out
}

func (p *commandProgress) show(output *os.File, done <-chan struct{}, options ...tea.ProgramOption) {
	p.mu.Lock()
	program := tea.NewProgram(
		newProgressModel(p.label, p.isReading, done),
		append([]tea.ProgramOption{
			tea.WithOutput(output),
			tea.WithInput(nil),
			tea.WithoutSignalHandler(),
		}, options...)...,
	)
	p.program = program
	p.mu.Unlock()

	_, _ = program.Run()

	p.mu.Lock()
	defer p.mu.Unlock()

	p.program = nil

	for _, out := range p.outputs {
		_, _ = out.File.Write(out.unfinishedLine)
		out.unfinishedLine = nil
	}
}

type progressOutput struct {
	*os.File

	progress       *commandProgress
	unfinishedLine []byte
}

func (o *progressOutput) Write(p []byte) (int, error) {
	o.progress.mu.Lock()
	defer o.progress.mu.Unlock()

	if o.progress.program == nil {
		return o.File.Write(p)
	}

	o.unfinishedLine = append(o.unfinishedLine, p...)

	for {
		line, rest, hasLine := bytes.Cut(o.unfinishedLine, []byte{
			'\n',
		})
		if !hasLine {
			break
		}

		o.progress.program.Println(string(line))
		o.unfinishedLine = rest
	}

	return len(p), nil
}

type progressInput struct {
	*os.File

	progress *commandProgress
}

func (i progressInput) Read(p []byte) (int, error) {
	i.progress.read(true)
	defer i.progress.read(false)

	return i.File.Read(p)
}

func (g *globals) showProgress(root *cobra.Command) {
	commands := []*cobra.Command{
		root,
	}

	for len(commands) > 0 {
		command := commands[0]
		commands = append(commands[1:], command.Commands()...)

		run := command.RunE
		if run == nil || command.Annotations[progressAnnotation] == "off" {
			continue
		}

		command.RunE = func(cmd *cobra.Command, args []string) error {
			return g.runWithProgress(cmd, func() error {
				return run(cmd, args)
			})
		}
	}
}

func (g *globals) runWithProgress(cmd *cobra.Command, run func() error) error {
	standardOutput, isOutputFile := cmd.OutOrStdout().(*os.File)
	standardError, isErrorFile := cmd.ErrOrStderr().(*os.File)

	if !isOutputFile || !isErrorFile || !term.IsTerminal(int(standardOutput.Fd())) ||
		!term.IsTerminal(int(standardError.Fd())) {
		return run()
	}

	progress := &commandProgress{
		label: "Working",
	}
	g.progress = progress

	root := cmd.Root()
	root.SetOut(progress.output(standardOutput))
	root.SetErr(progress.output(standardError))

	if standardInput, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(standardInput.Fd())) {
		root.SetIn(progressInput{
			File:     standardInput,
			progress: progress,
		})
	}

	var failure error

	done := make(chan struct{})

	go func() {
		defer close(done)

		failure = run()
	}()

	select {
	case <-done:
		return failure
	case <-time.After(progressDelay):
	}

	progress.show(standardError, done, tea.WithContext(cmd.Context()))
	<-done

	return failure
}
