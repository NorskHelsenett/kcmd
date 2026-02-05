package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type step int

const (
	stepPickNS step = iota
	stepPickType
	stepPickOwnerOrPod
	stepPickPodFromOwner
	stepPickContainer
	stepShell
)

type resType string

const (
	rtPod         resType = "pod"
	rtDeployment  resType = "deployment"
	rtStatefulSet resType = "statefulset"
)

type item struct {
	title string
	desc  string
}

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.desc }
func (i item) FilterValue() string { return i.title }

type model struct {
	step step

	// selections
	namespace string
	rtype     resType

	ownerName string // deployment/sts name
	podName   string
	container string

	// ui components
	lst     list.Model
	input   textinput.Model
	vp      viewport.Model
	spin    spinner.Model
	loading bool

	// data cache
	nsList       []string
	typeList     []resType
	ownerList    []string
	podList      []string
	containerList []string

	// repl
	output         strings.Builder
	outputLines    []string  // track output lines for copying
	autocompleteWords map[string]bool  // unique words from output for autocomplete
	history        []string
	histIdx        int
	lastErr        string
	width          int
	height         int
	currentDir     string // track current directory in pod
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true)
	borderStyle = lipgloss.NewStyle().Padding(0, 1)
	helpStyle   = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)

func initialModel() *model {
	// list
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)  // Compact spacing
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Velg namespace"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)

	// input (REPL)
	in := textinput.New()
	in.Placeholder = "skriv kommando… (clear / ctrl+r / q)"
	in.Focus()
	in.CharLimit = 4096
	in.Width = 60

	// viewport
	vp := viewport.New(0, 0)
	vp.SetContent("")

	// spinner
	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &model{
		step:              stepPickNS,
		lst:               l,
		input:             in,
		vp:                vp,
		spin:              sp,
		typeList:          []resType{rtPod, rtDeployment, rtStatefulSet},
		histIdx:           -1,
		autocompleteWords: make(map[string]bool),
	}
}

// ---------- kubectl helpers ----------

func runKubectl(args ...string) ([]byte, []byte, error) {
	cmd := exec.Command("kubectl", args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	e := cmd.Run()
	return out.Bytes(), errb.Bytes(), e
}

type kList[T any] struct {
	Items []T `json:"items"`
}

type kNamespace struct {
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

type kMeta struct {
	Name string `json:"name"`
}

type kWorkload struct {
	Metadata kMeta `json:"metadata"`
	Spec     struct {
		Selector struct {
			MatchLabels map[string]string `json:"matchLabels"`
		} `json:"selector"`
	} `json:"spec"`
}

type kPod struct {
	Metadata kMeta `json:"metadata"`
	Spec     struct {
		Containers []struct {
			Name string `json:"name"`
		} `json:"containers"`
	} `json:"spec"`
}

func getNamespaces() ([]string, error) {
	out, errb, err := runKubectl("get", "ns", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get ns: %w: %s", err, strings.TrimSpace(string(errb)))
	}
	var parsed kList[kNamespace]
	if e := json.Unmarshal(out, &parsed); e != nil {
		return nil, e
	}
	var res []string
	for _, it := range parsed.Items {
		res = append(res, it.Metadata.Name)
	}
	sort.Strings(res)
	return res, nil
}

func getNamesInNS(namespace string, kind resType) ([]string, error) {
	args := []string{"-n", namespace, "get", string(kind), "-o", "json"}
	out, errb, err := runKubectl(args...)
	if err != nil {
		return nil, fmt.Errorf("kubectl get %s: %w: %s", kind, err, strings.TrimSpace(string(errb)))
	}
	// deployment/statefulset share structure for metadata.name
	var parsed struct {
		Items []struct {
			Metadata kMeta `json:"metadata"`
		} `json:"items"`
	}
	if e := json.Unmarshal(out, &parsed); e != nil {
		return nil, e
	}
	var res []string
	for _, it := range parsed.Items {
		res = append(res, it.Metadata.Name)
	}
	sort.Strings(res)
	return res, nil
}

func getSelectorForWorkload(namespace string, kind resType, name string) (string, error) {
	if kind != rtDeployment && kind != rtStatefulSet {
		return "", errors.New("selector only supported for deployment/statefulset")
	}
	out, errb, err := runKubectl("-n", namespace, "get", string(kind), name, "-o", "json")
	if err != nil {
		return "", fmt.Errorf("kubectl get %s/%s: %w: %s", kind, name, err, strings.TrimSpace(string(errb)))
	}
	var wl kWorkload
	if e := json.Unmarshal(out, &wl); e != nil {
		return "", e
	}
	if len(wl.Spec.Selector.MatchLabels) == 0 {
		return "", errors.New("matchLabels empty; kan ikke auto-finne pods")
	}
	// stable order for selector string
	keys := make([]string, 0, len(wl.Spec.Selector.MatchLabels))
	for k := range wl.Spec.Selector.MatchLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, wl.Spec.Selector.MatchLabels[k]))
	}
	return strings.Join(parts, ","), nil
}

func getPodsBySelector(namespace, selector string) ([]string, error) {
	out, errb, err := runKubectl("-n", namespace, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get pods -l %q: %w: %s", selector, err, strings.TrimSpace(string(errb)))
	}
	var parsed kList[kPod]
	if e := json.Unmarshal(out, &parsed); e != nil {
		return nil, e
	}
	var res []string
	for _, it := range parsed.Items {
		res = append(res, it.Metadata.Name)
	}
	sort.Strings(res)
	return res, nil
}

func getContainers(namespace, pod string) ([]string, error) {
	out, errb, err := runKubectl("-n", namespace, "get", "pod", pod, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("kubectl get pod/%s: %w: %s", pod, err, strings.TrimSpace(string(errb)))
	}
	var p kPod
	if e := json.Unmarshal(out, &p); e != nil {
		return nil, e
	}
	var res []string
	for _, c := range p.Spec.Containers {
		res = append(res, c.Name)
	}
	sort.Strings(res)
	return res, nil
}

func execInPod(namespace, pod, container, cmdline, currentDir string) (string, string, error) {
	// Ikke TTY (-it) => stabilt i TUI. Bruk sh -lc for pipes/redirects.
	fullCmd := cmdline
	if currentDir != "" {
		fullCmd = fmt.Sprintf("cd %s && %s", currentDir, cmdline)
	}
	out, errb, err := runKubectl("-n", namespace, "exec", pod, "-c", container, "--", "sh", "-lc", fullCmd)
	return string(out), string(errb), err
}

// ---------- tea messages ----------

type loadMsg struct {
	step step
	err  error

	values []string
}

type cmdResultMsg struct {
	cmd   string
	stdout string
	stderr string
	err    error
	took   time.Duration
}

func loadStep(s step, m model) tea.Cmd {
	return func() tea.Msg {
		var vals []string
		var err error

		switch s {
		case stepPickNS:
			vals, err = getNamespaces()
		case stepPickType:
			for _, t := range m.typeList {
				vals = append(vals, string(t))
			}
		case stepPickOwnerOrPod:
			if m.rtype == rtPod {
				vals, err = getNamesInNS(m.namespace, rtPod)
			} else {
				vals, err = getNamesInNS(m.namespace, m.rtype)
			}
		case stepPickPodFromOwner:
			selector, e := getSelectorForWorkload(m.namespace, m.rtype, m.ownerName)
			if e != nil {
				err = e
				break
			}
			vals, err = getPodsBySelector(m.namespace, selector)
		case stepPickContainer:
			vals, err = getContainers(m.namespace, m.podName)
		default:
			err = nil
		}
		return loadMsg{step: s, err: err, values: vals}
	}
}

func runCommand(ns, pod, container, cmdline, currentDir string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		stdout, stderr, err := execInPod(ns, pod, container, cmdline, currentDir)
		return cmdResultMsg{
			cmd: cmdline, stdout: stdout, stderr: stderr, err: err, took: time.Since(start),
		}
	}
}

// ---------- UI helpers ----------

func (m *model) setList(title string, values []string) {
	items := make([]list.Item, 0, len(values))
	for _, v := range values {
		items = append(items, item{title: v, desc: ""})
	}
	m.lst.SetItems(items)
	m.lst.Title = title
	m.lst.ResetSelected()
}

func (m *model) header() string {
	target := fmt.Sprintf("ns=%s type=%s pod=%s container=%s", m.namespace, m.rtype, m.podName, m.container)
	switch m.step {
	case stepPickNS:
		return titleStyle.Render("KCMD — Velg namespace")
	case stepPickType:
		return titleStyle.Render("KCMD — Velg type")
	case stepPickOwnerOrPod:
		if m.rtype == rtPod {
			return titleStyle.Render("KCMD — Velg pod") + "  " + helpStyle.Render(target)
		}
		return titleStyle.Render("KCMD — Velg workload (deployment/statefulset)") + "  " + helpStyle.Render(target)
	case stepPickPodFromOwner:
		return titleStyle.Render("KCMD — Velg pod fra workload") + "  " + helpStyle.Render(target)
	case stepPickContainer:
		return titleStyle.Render("KCMD — Velg container") + "  " + helpStyle.Render(target)
	case stepShell:
		return titleStyle.Render("KCMD — Shell") + "  " + helpStyle.Render(target)
	default:
		return titleStyle.Render("KCMD")
	}
}

func (m *model) help() string {
	switch m.step {
	case stepShell:
		return helpStyle.Render("enter=kjør  tab=autocomplete  ↑/↓=historikk  pgup/pgdn=scroll  /copy 1,10=copy  ctrl+r=retarget  q=quit")
	default:
		return helpStyle.Render("enter=velg  / = filter  esc=tilbake  q=quit")
	}
}

func (m *model) appendOutput(s string) {
	if s == "" {
		return
	}
	
	// Split into lines and add each non-empty line to our tracking array
	lines := strings.Split(s, "\n")
	var numbered strings.Builder
	
	for _, line := range lines {
		if line == "" && !strings.HasSuffix(s, "\n") {
			// Skip empty lines unless they were explicitly part of the input
			continue
		}
		
		// Add to tracking array
		m.outputLines = append(m.outputLines, line)
		
		// Extract words for autocomplete (skip line numbers and separators)
		words := strings.Fields(line)
		for _, word := range words {
			// Only add meaningful words (longer than 2 chars, alphanumeric)
			if len(word) > 2 && !strings.HasPrefix(word, "/") {
				m.autocompleteWords[word] = true
			}
		}
		
		// Build numbered display
		lineNum := len(m.outputLines)
		if line != "" {
			numbered.WriteString(fmt.Sprintf("%4d │ %s\n", lineNum, line))
		} else {
			numbered.WriteString(fmt.Sprintf("%4d │\n", lineNum))
		}
	}
	
	m.output.WriteString(numbered.String())
	content := m.output.String()
	m.vp.SetContent(content)
	m.vp.GotoBottom()
}

// ---------- tea.Model ----------

func (m *model) Init() tea.Cmd {
	m.loading = true
	return tea.Batch(m.spin.Tick, loadStep(stepPickNS, *m))
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		// layout
		if m.step == stepShell {
			m.vp.Width = msg.Width - 2  // just padding
			m.vp.Height = msg.Height - 3  // header(1) + help(1) + input(1)
			m.input.Width = msg.Width - 2
		} else {
			m.lst.SetSize(msg.Width-2, msg.Height-4)
		}
		return m, nil

	case loadMsg:
		m.loading = false
		m.lastErr = ""
		if msg.err != nil {
			m.lastErr = msg.err.Error()
			// keep on same step
			return m, nil
		}

		switch msg.step {
		case stepPickNS:
			m.nsList = msg.values
			m.setList("Velg namespace", msg.values)
		case stepPickType:
			m.setList("Velg type", msg.values)
		case stepPickOwnerOrPod:
			if m.rtype == rtPod {
				m.podList = msg.values
				m.setList("Velg pod", msg.values)
			} else {
				m.ownerList = msg.values
				m.setList("Velg workload", msg.values)
			}
		case stepPickPodFromOwner:
			m.podList = msg.values
			m.setList("Velg pod", msg.values)
		case stepPickContainer:
			m.containerList = msg.values
			m.setList("Velg container", msg.values)
		}
		return m, nil

	case cmdResultMsg:
		m.loading = false
		m.lastErr = ""

		// Pretty block
		status := okStyle.Render("OK")
		if msg.err != nil {
			status = errStyle.Render("ERR")
			m.lastErr = strings.TrimSpace(msg.stderr)
		}
		m.appendOutput(fmt.Sprintf("» %s  (%s)  [%s]", msg.cmd, msg.took.Round(time.Millisecond), status))
		if strings.TrimSpace(msg.stdout) != "" {
			m.appendOutput(msg.stdout)
		}
		if strings.TrimSpace(msg.stderr) != "" {
			m.appendOutput(errStyle.Render(msg.stderr))
		}
		return m, nil

	case tea.KeyMsg:
		k := msg.String()

		// global quit
		if k == "ctrl+c" || k == "q" {
			return m, tea.Quit
		}

		// back navigation (non-shell)
		if m.step != stepShell && k == "esc" {
			switch m.step {
			case stepPickType:
				m.step = stepPickNS
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickNS, *m))
			case stepPickOwnerOrPod:
				m.step = stepPickType
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickType, *m))
			case stepPickPodFromOwner:
				m.step = stepPickOwnerOrPod
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickOwnerOrPod, *m))
			case stepPickContainer:
				if m.rtype == rtPod {
					m.step = stepPickOwnerOrPod
					m.loading = true
					return m, tea.Batch(m.spin.Tick, loadStep(stepPickOwnerOrPod, *m))
				}
				m.step = stepPickPodFromOwner
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickPodFromOwner, *m))
			}
		}

		if m.step == stepShell {
			switch k {
			case "ctrl+r":
				// retarget: restart flow
				*m = *initialModel()
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickNS, *m))
			case "tab":
				// Filesystem path autocomplete
				currentInput := m.input.Value()
				words := strings.Fields(currentInput)
				if len(words) == 0 {
					return m, nil
				}
				
				// Get the last word being typed
				lastWord := words[len(words)-1]
				
				// Check if it looks like a path (contains /)
				if strings.Contains(lastWord, "/") {
					// Extract directory and partial filename
					lastSlash := strings.LastIndex(lastWord, "/")
					dirPath := lastWord[:lastSlash+1]
					partialFile := lastWord[lastSlash+1:]
					
					// Query filesystem in pod for matching files/directories
					// Use -F to append / to directories, -1 for one per line
					lsCmd := fmt.Sprintf("ls -1F %s 2>/dev/null | grep '^%s'", dirPath, partialFile)
					stdout, _, err := execInPod(m.namespace, m.podName, m.container, lsCmd, m.currentDir)
					
					if err == nil && stdout != "" {
						lines := strings.Split(strings.TrimSpace(stdout), "\n")
						if len(lines) > 0 && lines[0] != "" {
							// Use first match (already has / appended if directory thanks to -F)
							completedPath := dirPath + lines[0]
							words[len(words)-1] = completedPath
							m.input.SetValue(strings.Join(words, " "))
							m.input.CursorEnd()
						}
					}
				} else if len(lastWord) >= 2 {
					// Fall back to word-based autocomplete from output
					var matches []string
					for word := range m.autocompleteWords {
						if strings.HasPrefix(word, lastWord) && word != lastWord {
							matches = append(matches, word)
						}
					}
					
					if len(matches) > 0 {
						sort.Strings(matches)
						words[len(words)-1] = matches[0]
						m.input.SetValue(strings.Join(words, " "))
						m.input.CursorEnd()
					}
				}
				return m, nil
			case "up":
				if len(m.history) == 0 {
					return m, nil
				}
				if m.histIdx == -1 {
					m.histIdx = len(m.history) - 1
				} else if m.histIdx > 0 {
					m.histIdx--
				}
				m.input.SetValue(m.history[m.histIdx])
				m.input.CursorEnd()
				return m, nil
			case "down":
				if len(m.history) == 0 {
					return m, nil
				}
				if m.histIdx == -1 {
					return m, nil
				}
				if m.histIdx < len(m.history)-1 {
					m.histIdx++
					m.input.SetValue(m.history[m.histIdx])
				} else {
					m.histIdx = -1
					m.input.SetValue("")
				}
				m.input.CursorEnd()
				return m, nil
			case "enter":
				cmdline := strings.TrimSpace(m.input.Value())
				if cmdline == "" {
					return m, nil
				}
				m.input.SetValue("")
				m.histIdx = -1

				if cmdline == "clear" {
					m.output.Reset()
					m.outputLines = nil
					m.vp.SetContent("")
					return m, nil
				}

				// Check if command is /copy to copy lines to clipboard
				if strings.HasPrefix(cmdline, "/copy ") {
					rangeStr := strings.TrimSpace(strings.TrimPrefix(cmdline, "/copy"))
					var startLine, endLine int
					
					// Parse range: "255,512" or "255-512" or just "255"
					if strings.Contains(rangeStr, ",") {
						fmt.Sscanf(rangeStr, "%d,%d", &startLine, &endLine)
					} else if strings.Contains(rangeStr, "-") {
						fmt.Sscanf(rangeStr, "%d-%d", &startLine, &endLine)
					} else {
						fmt.Sscanf(rangeStr, "%d", &startLine)
						endLine = startLine
					}
					
					// Validate range
					if startLine < 1 || endLine < startLine || endLine > len(m.outputLines) {
						m.appendOutput(errStyle.Render(fmt.Sprintf("Invalid range. Available lines: 1-%d", len(m.outputLines))))
						return m, nil
					}
					
					// Copy to clipboard
					linesToCopy := m.outputLines[startLine-1 : endLine]
					textToCopy := strings.Join(linesToCopy, "\n")
					
					// Use pbcopy on macOS, xclip/xsel on Linux, clip.exe on Windows
					var cmd *exec.Cmd
					if _, err := exec.LookPath("pbcopy"); err == nil {
						cmd = exec.Command("pbcopy")
					} else if _, err := exec.LookPath("xclip"); err == nil {
						cmd = exec.Command("xclip", "-selection", "clipboard")
					} else if _, err := exec.LookPath("xsel"); err == nil {
						cmd = exec.Command("xsel", "--clipboard", "--input")
					} else if _, err := exec.LookPath("clip.exe"); err == nil {
						cmd = exec.Command("clip.exe")
					} else {
						m.appendOutput(errStyle.Render("No clipboard utility found (pbcopy/xclip/xsel/clip.exe)"))
						return m, nil
					}
					
					cmd.Stdin = strings.NewReader(textToCopy)
					if err := cmd.Run(); err != nil {
						m.appendOutput(errStyle.Render(fmt.Sprintf("Failed to copy: %v", err)))
					} else {
						lineCount := endLine - startLine + 1
						m.appendOutput(okStyle.Render(fmt.Sprintf("✓ Copied %d line(s) to clipboard", lineCount)))
					}
					
					return m, nil
				}

				// Check if command is 'cd' to track directory
				if strings.HasPrefix(cmdline, "cd ") || cmdline == "cd" {
					newDir := strings.TrimSpace(strings.TrimPrefix(cmdline, "cd"))
					if newDir == "" || newDir == "~" {
						m.currentDir = ""
					} else if strings.HasPrefix(newDir, "/") {
						m.currentDir = newDir
					} else {
						// Relative path - append to current
						if m.currentDir == "" {
							m.currentDir = newDir
						} else {
							m.currentDir = m.currentDir + "/" + newDir
						}
					}
					
					// Clear autocomplete words when changing directory
					m.autocompleteWords = make(map[string]bool)
					
					// store in history
					if len(m.history) == 0 || m.history[len(m.history)-1] != cmdline {
						m.history = append(m.history, cmdline)
					}
					
					// Show feedback without executing
					dirDisplay := m.currentDir
					if dirDisplay == "" {
						dirDisplay = "~"
					}
					m.appendOutput(fmt.Sprintf("» %s", cmdline))
					m.appendOutput(okStyle.Render(fmt.Sprintf("Working directory: %s", dirDisplay)))
					return m, nil
				}

				// store history (dedupe consecutive)
				if len(m.history) == 0 || m.history[len(m.history)-1] != cmdline {
					m.history = append(m.history, cmdline)
				}

				m.loading = true
				cmds = append(cmds, m.spin.Tick, runCommand(m.namespace, m.podName, m.container, cmdline, m.currentDir))
				return m, tea.Batch(cmds...)
			}

			// normal input updates + viewport scrolling
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			cmds = append(cmds, cmd)

			m.vp, cmd = m.vp.Update(msg)
			cmds = append(cmds, cmd)

			return m, tea.Batch(cmds...)
		}

		// selection mode
		switch k {
		case "enter":
			if len(m.lst.Items()) == 0 {
				return m, nil
			}
			chosen, ok := m.lst.SelectedItem().(item)
			if !ok {
				return m, nil
			}
			val := chosen.title

			switch m.step {
			case stepPickNS:
				m.namespace = val
				m.step = stepPickType
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickType, *m))

			case stepPickType:
				m.rtype = resType(val)
				m.step = stepPickOwnerOrPod
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickOwnerOrPod, *m))

			case stepPickOwnerOrPod:
				if m.rtype == rtPod {
					m.podName = val
					m.step = stepPickContainer
					m.loading = true
					return m, tea.Batch(m.spin.Tick, loadStep(stepPickContainer, *m))
				}
				m.ownerName = val
				m.step = stepPickPodFromOwner
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickPodFromOwner, *m))

			case stepPickPodFromOwner:
				m.podName = val
				m.step = stepPickContainer
				m.loading = true
				return m, tea.Batch(m.spin.Tick, loadStep(stepPickContainer, *m))

			case stepPickContainer:
				m.container = val
				m.step = stepShell
				m.loading = false
				m.output.Reset()
				m.vp.SetContent("")
				m.input.Focus()
				
				// Ensure viewport is sized correctly when entering shell mode
				if m.width > 0 && m.height > 0 {
					m.vp.Width = m.width - 2
					m.vp.Height = m.height - 3
					m.input.Width = m.width - 2
				}
				
				return m, nil
			}
		}

		var cmd tea.Cmd
		m.lst, cmd = m.lst.Update(msg)
		cmds = append(cmds, cmd)
		return m, tea.Batch(cmds...)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *model) View() string {
	head := m.header()
	help := m.help()

	errLine := ""
	if m.lastErr != "" {
		errLine = errStyle.Render(m.lastErr)
	}

	if m.step == stepShell {
		loading := ""
		if m.loading {
			loading = " " + m.spin.View() + " kjører…"
		}

		body := borderStyle.Render(m.vp.View())
		foot := borderStyle.Render(m.input.View() + loading)

		parts := []string{
			head,
			help,
		}
		if errLine != "" {
			parts = append(parts, errLine)
		}
		parts = append(parts, body, foot)
		return strings.Join(parts, "\n")
	}

	loading := ""
	if m.loading {
		loading = " " + m.spin.View()
	}
	top := fmt.Sprintf("%s%s\n%s", head, loading, help)
	if errLine != "" {
		top += "\n" + errLine
	}
	return top + "\n" + borderStyle.Width(m.width-2).Render(m.lst.View())
}

func main() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		fmt.Fprintln(os.Stderr, "Fant ikke kubectl i PATH.")
		os.Exit(1)
	}

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

