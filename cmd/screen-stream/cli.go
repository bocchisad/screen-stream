package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/bocchisad/screen-stream/engine"
	"github.com/bocchisad/screen-stream/streamer"
)

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	mutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	cursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
)

type cliPhase int

const (
	phaseMode cliPhase = iota
	phaseField
	phaseDone
)

type cliField struct {
	key, label, value string
	kind              string // text | int | float | bool | capture
}

type cliModel struct {
	phase   cliPhase
	modes   []string
	cursor  int
	mode    string
	fields  []cliField
	fi      int
	editing bool
	input   string
	err     string
	signal  string
	opts    sessionOpts
}

// runCLI is an interactive bubbletea menu that fills flags then returns mode + opts.
func runCLI() (mode string, o sessionOpts, signalAddr string) {
	m := newCLIModel()
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cli: %v\n", err)
		os.Exit(1)
	}
	out, ok := final.(cliModel)
	if !ok || out.phase != phaseDone || out.mode == "" {
		os.Exit(0)
	}
	return out.mode, out.opts, out.signal
}

func newCLIModel() cliModel {
	return cliModel{
		phase: phaseMode,
		modes: []string{"hub", "send", "recv", "loopback", "bench", "doctor"},
	}
}

func (m cliModel) Init() tea.Cmd { return nil }

func (m cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch m.phase {
		case phaseMode:
			return m.updateMode(msg)
		case phaseField:
			return m.updateField(msg)
		}
	}
	return m, nil
}

func (m cliModel) updateMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.modes)-1 {
			m.cursor++
		}
	case "enter":
		m.mode = m.modes[m.cursor]
		m.fields = m.fieldsFor(m.mode)
		m.fi = 0
		m.phase = phaseField
		m.err = ""
	}
	return m, nil
}

func (m cliModel) updateField(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editing {
		switch msg.String() {
		case "esc":
			m.editing = false
			m.input = ""
		case "enter":
			m.fields[m.fi].value = m.input
			m.editing = false
			m.input = ""
		case "backspace":
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		default:
			if len(msg.Runes) == 1 && msg.Type == tea.KeyRunes {
				m.input += string(msg.Runes)
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	case "up", "k":
		if m.fi > 0 {
			m.fi--
		}
	case "down", "j", "tab":
		if m.fi < len(m.fields)-1 {
			m.fi++
		}
	case " ":
		f := &m.fields[m.fi]
		if f.kind == "bool" {
			if f.value == "y" {
				f.value = "n"
			} else {
				f.value = "y"
			}
		}
	case "enter":
		f := m.fields[m.fi]
		if f.key == "_go" {
			if err := m.apply(); err != nil {
				m.err = err.Error()
				return m, nil
			}
			m.phase = phaseDone
			return m, tea.Quit
		}
		if f.kind == "bool" {
			if f.value == "y" {
				f.value = "n"
			} else {
				f.value = "y"
			}
			m.fields[m.fi] = f
			return m, nil
		}
		m.editing = true
		m.input = f.value
	case "backspace":
		m.phase = phaseMode
		m.err = ""
	}
	return m, nil
}

func (m cliModel) fieldsFor(mode string) []cliField {
	tokenDef := os.Getenv(streamer.EnvAuthToken)
	fields := []cliField{
		{key: "signal", label: "signal", value: "127.0.0.1:8787", kind: "text"},
		{key: "token", label: "token", value: tokenDef, kind: "text"},
	}
	switch mode {
	case "hub", "doctor":
		// signal + token only
	case "bench":
		fields = append(fields,
			cliField{key: "seconds", label: "seconds", value: "3", kind: "float"},
			cliField{key: "fps", label: "fps", value: "60", kind: "int"},
			cliField{key: "synthetic", label: "synthetic", value: "y", kind: "bool"},
			cliField{key: "pii", label: "pii/ocr", value: "n", kind: "bool"},
			cliField{key: "capture", label: "capture", value: "cpu", kind: "capture"},
		)
	case "loopback":
		fields = append(fields,
			cliField{key: "seconds", label: "seconds", value: "3", kind: "float"},
			cliField{key: "fps", label: "fps", value: "60", kind: "int"},
			cliField{key: "scale", label: "scale %", value: "25", kind: "int"},
			cliField{key: "synthetic", label: "synthetic", value: "y", kind: "bool"},
			cliField{key: "compress", label: "compress", value: "y", kind: "bool"},
			cliField{key: "pii", label: "pii/ocr", value: "n", kind: "bool"},
			cliField{key: "capture", label: "capture", value: "cpu", kind: "capture"},
			cliField{key: "preview", label: "preview addr (empty=off)", value: "", kind: "text"},
		)
	case "send":
		fields = append(fields,
			cliField{key: "seconds", label: "seconds", value: "30", kind: "float"},
			cliField{key: "fps", label: "fps", value: "60", kind: "int"},
			cliField{key: "scale", label: "scale %", value: "75", kind: "int"},
			cliField{key: "synthetic", label: "synthetic", value: "n", kind: "bool"},
			cliField{key: "compress", label: "compress", value: "y", kind: "bool"},
			cliField{key: "pii", label: "pii/ocr", value: "n", kind: "bool"},
			cliField{key: "capture", label: "capture", value: "cpu", kind: "capture"},
			cliField{key: "peer", label: "peer / peers", value: "default", kind: "text"},
			cliField{key: "stun", label: "stun", value: "", kind: "text"},
			cliField{key: "tofu", label: "tofu", value: "auto", kind: "text"},
		)
	case "recv":
		fields = append(fields,
			cliField{key: "seconds", label: "seconds", value: "30", kind: "float"},
			cliField{key: "compress", label: "compress", value: "y", kind: "bool"},
			cliField{key: "peer", label: "peer", value: "default", kind: "text"},
			cliField{key: "stun", label: "stun", value: "", kind: "text"},
			cliField{key: "tofu", label: "tofu", value: "auto", kind: "text"},
			cliField{key: "preview", label: "preview addr", value: "127.0.0.1:8090", kind: "text"},
		)
	}
	fields = append(fields, cliField{key: "_go", label: "▶ start", value: "", kind: "text"})
	return fields
}

func (m *cliModel) apply() error {
	get := func(k, def string) string {
		for _, f := range m.fields {
			if f.key == k {
				return f.value
			}
		}
		return def
	}
	yes := func(k string, def bool) bool {
		v := strings.ToLower(get(k, ""))
		if v == "" {
			return def
		}
		return v == "y" || v == "yes" || v == "1" || v == "true"
	}
	asInt := func(k string, def int) int {
		n, err := strconv.Atoi(get(k, strconv.Itoa(def)))
		if err != nil {
			return def
		}
		return n
	}
	asFloat := func(k string, def float64) float64 {
		n, err := strconv.ParseFloat(get(k, strconv.FormatFloat(def, 'g', -1, 64)), 64)
		if err != nil {
			return def
		}
		return n
	}

	m.signal = get("signal", "127.0.0.1:8787")
	o := sessionOpts{
		token:       streamer.ResolveAuthToken(get("token", "")),
		seconds:     asFloat("seconds", 30),
		fps:         asInt("fps", 60),
		scale:       asInt("scale", 75),
		synthetic:   yes("synthetic", m.mode == "bench" || m.mode == "loopback"),
		compress:    yes("compress", true),
		pii:         yes("pii", false),
		stun:        get("stun", ""),
		peer:        get("peer", "default"),
		tofu:        strings.ToLower(get("tofu", "auto")),
		preview:     get("preview", ""),
		block:       64,
		width:       1920,
		height:      1080,
		signalURL:   "http://" + m.signal,
	}
	o.ocr = o.pii
	o.peers = splitCSV(o.peer)
	if o.tofu != "auto" && o.tofu != "off" && o.tofu != "" {
		return fmt.Errorf("tofu must be auto|off")
	}
	if o.tofu == "" {
		o.tofu = "auto"
	}
	cm, err := engine.ParseCaptureMode(get("capture", "cpu"))
	if err != nil {
		return err
	}
	o.captureMode = cm
	if cm == engine.CaptureGPUSurface {
		fmt.Fprintln(os.Stderr, "WARN: gpu-surface is EXPERIMENTAL — still ScreenCaptureKit/PipeWire; at your own risk")
	}
	m.opts = o
	return nil
}

func (m cliModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("screen-stream"))
	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("↑↓ navigate · enter select/edit · space toggle · esc back/quit"))
	b.WriteString("\n\n")

	switch m.phase {
	case phaseMode:
		b.WriteString("mode\n")
		for i, name := range m.modes {
			cur := "  "
			if i == m.cursor {
				cur = cursorStyle.Render("▸ ")
			}
			b.WriteString(cur + name + "\n")
		}
	case phaseField:
		b.WriteString(okStyle.Render("mode: "+m.mode) + "\n\n")
		for i, f := range m.fields {
			cur := "  "
			if i == m.fi {
				cur = cursorStyle.Render("▸ ")
			}
			val := f.value
			if f.key == "_go" {
				b.WriteString(cur + f.label + "\n")
				continue
			}
			if m.editing && i == m.fi {
				val = m.input + "█"
			}
			if f.kind == "bool" {
				val = "[" + val + "]"
			}
			b.WriteString(fmt.Sprintf("%s%s  %s\n", cur, f.label, mutedStyle.Render(val)))
		}
		if m.err != "" {
			b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(m.err) + "\n")
		}
	}
	return b.String()
}
