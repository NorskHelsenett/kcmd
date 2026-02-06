package tui

import (
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"kui/internal/kubectl"
	"kui/internal/types"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.Width = msg.Width
		m.Height = msg.Height

		if m.Step == types.StepShell {
			m.Vp.Width = msg.Width - 2
			m.Vp.Height = msg.Height - 3
			m.Input.Width = msg.Width - 2
		} else {
			m.Lst.SetSize(msg.Width-2, msg.Height-4)
		}
		return m, nil

	case LoadMsg:
		m.Loading = false
		m.LastErr = ""
		if msg.Err != nil {
			m.LastErr = msg.Err.Error()
			return m, nil
		}

		switch msg.Step {
		case types.StepPickNS:
			m.NsList = msg.Values
			m.SetList("Velg namespace", msg.Values)
		case types.StepPickType:
			m.SetList("Velg type", msg.Values)
		case types.StepPickOwnerOrPod:
			if m.Rtype == types.RtPod {
				m.PodList = msg.Values
				m.SetList("Velg pod", msg.Values)
			} else {
				m.OwnerList = msg.Values
				m.SetList("Velg workload", msg.Values)
			}
		case types.StepPickPodFromOwner:
			m.PodList = msg.Values
			m.SetList("Velg pod", msg.Values)
		case types.StepPickContainer:
			m.ContainerList = msg.Values
			m.SetList("Velg container", msg.Values)
		}
		return m, nil

	case CmdResultMsg:
		m.Loading = false
		m.LastErr = ""

		if msg.Err != nil && !m.UseDebugContainer &&
			(strings.Contains(msg.Stderr, "executable file not found") ||
				strings.Contains(msg.Stderr, "OCI runtime exec failed") ||
				strings.Contains(msg.Stderr, "not found")) {
			m.AppendOutput(ErrStyle.Render("Container has no shell. Creating ephemeral debug container..."))

			currentPolicy, err := kubectl.GetPodSecurityPolicy(m.Namespace)
			if err != nil {
				m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Failed to get current policy: %v", err)))
				return m, nil
			}

			if currentPolicy != "privileged" {
				m.AppendOutput(fmt.Sprintf("Namespace policy is '%s', changing to 'privileged'...", currentPolicy))
				m.OriginalPodSecurityPolicy = currentPolicy

				if err := kubectl.SetPodSecurityPolicy(m.Namespace, "privileged"); err != nil {
					m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Failed to change policy: %v", err)))
					m.AppendOutput("You may need permissions to modify namespace labels.")
					return m, nil
				}

				m.ChangedPodSecurityPolicy = true
				m.AppendOutput(OkStyle.Render("✓ Changed namespace policy to 'privileged'"))
				m.AppendOutput("Policy will be restored to original on quit.")
				m.AppendOutput("")
			}

			m.Loading = true
			return m, tea.Batch(m.Spin.Tick, createDebugContainerCmd(m.Namespace, m.PodName, m.Container))
		}

		status := OkStyle.Render("OK")
		if msg.Err != nil {
			status = ErrStyle.Render("ERR")
			m.LastErr = strings.TrimSpace(msg.Stderr)
		}
		m.AppendOutput(fmt.Sprintf("» %s  (%s)  [%s]", msg.Cmd, msg.Took.Round(time.Millisecond), status))
		if strings.TrimSpace(msg.Stdout) != "" {
			m.AppendOutput(msg.Stdout)
		}
		if strings.TrimSpace(msg.Stderr) != "" {
			m.AppendOutput(ErrStyle.Render(msg.Stderr))
		}
		return m, nil

	case DebugContainerMsg:
		m.Loading = false
		if msg.Err != nil {
			m.LastErr = msg.Err.Error()

			if strings.Contains(msg.Err.Error(), "runAsNonRoot") ||
				strings.Contains(msg.Err.Error(), "runAsUser=0") ||
				strings.Contains(msg.Err.Error(), "PodSecurity") {
				m.AppendOutput(ErrStyle.Render("Debug container creation blocked by restricted PodSecurity policy"))
				m.AppendOutput("")
				m.AppendOutput("Attempting to temporarily change namespace policy to 'privileged'...")

				currentPolicy, err := kubectl.GetPodSecurityPolicy(m.Namespace)
				if err != nil {
					m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Failed to get current policy: %v", err)))
					return m, nil
				}
				m.OriginalPodSecurityPolicy = currentPolicy

				if err := kubectl.SetPodSecurityPolicy(m.Namespace, "privileged"); err != nil {
					m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Failed to change policy: %v", err)))
					m.AppendOutput("You may need permissions to modify namespace labels.")
					return m, nil
				}

				m.ChangedPodSecurityPolicy = true
				if currentPolicy == "" {
					m.AppendOutput(OkStyle.Render("Changed namespace from no policy (unrestricted) to 'privileged'"))
				} else {
					m.AppendOutput(OkStyle.Render(fmt.Sprintf("Changed namespace policy from '%s' to 'privileged'", currentPolicy)))
				}
				m.AppendOutput("Policy will be restored to original on quit.")
				m.AppendOutput("")
				m.AppendOutput("Retrying debug container creation...")
				m.Loading = true
				return m, tea.Batch(m.Spin.Tick, createDebugContainerCmd(m.Namespace, m.PodName, m.Container))
			} else {
				m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Failed to create debug container: %v", msg.Err)))
			}
			return m, nil
		}

		m.UseDebugContainer = true
		m.DebugContainer = msg.DebugContainer
		m.TargetRoot = msg.TargetRoot
		m.CurrentDir = "/"
		m.AppendOutput(OkStyle.Render(fmt.Sprintf("Debug container '%s' created.", msg.DebugContainer)))
		m.AppendOutput(fmt.Sprintf("Target container filesystem: %s", msg.TargetRoot))
		m.AppendOutput("")
		m.AppendOutput("Testing filesystem access...")

		testCmd := fmt.Sprintf("ls %s 2>&1 | head -5", msg.TargetRoot)
		m.Loading = true
		return m, tea.Batch(m.Spin.Tick, runCommand(m.Namespace, m.PodName, m.Container, testCmd, "", true, m.DebugContainer, m.TargetRoot))

	case tea.KeyMsg:
		return m.handleKeyPress(msg, &cmds)

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.Spin, cmd = m.Spin.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m *Model) handleKeyPress(msg tea.KeyMsg, cmds *[]tea.Cmd) (tea.Model, tea.Cmd) {
	k := msg.String()

	if k == "ctrl+c" {
		return m, tea.Quit
	}

	if m.Step != types.StepShell && k == "esc" {
		return m.handleBackNavigation()
	}

	if m.Step == types.StepShell {
		return m.handleShellInput(k, cmds)
	}

	return m.handleSelection(k, cmds)
}

func (m *Model) handleBackNavigation() (tea.Model, tea.Cmd) {
	switch m.Step {
	case types.StepPickType:
		m.Step = types.StepPickNS
		m.Loading = true
		return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickNS, m))
	case types.StepPickOwnerOrPod:
		m.Step = types.StepPickType
		m.Loading = true
		return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickType, m))
	case types.StepPickPodFromOwner:
		m.Step = types.StepPickOwnerOrPod
		m.Loading = true
		return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickOwnerOrPod, m))
	case types.StepPickContainer:
		if m.Rtype == types.RtPod {
			m.Step = types.StepPickOwnerOrPod
			m.Loading = true
			return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickOwnerOrPod, m))
		}
		m.Step = types.StepPickPodFromOwner
		m.Loading = true
		return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickPodFromOwner, m))
	}
	return m, nil
}

func (m *Model) handleShellInput(k string, cmds *[]tea.Cmd) (tea.Model, tea.Cmd) {
	switch k {
	case "ctrl+r":
		m = InitialModel()
		m.Loading = true
		return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickNS, m))
	case "tab":
		return m.handleAutocomplete(), nil
	case "up":
		return m.handleHistoryUp(), nil
	case "down":
		return m.handleHistoryDown(), nil
	case "enter":
		return m.handleCommand(cmds)
	}

	var cmd tea.Cmd
	m.Input, cmd = m.Input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	*cmds = append(*cmds, cmd)

	m.Vp, cmd = m.Vp.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	*cmds = append(*cmds, cmd)

	return m, tea.Batch(*cmds...)
}

func (m *Model) handleAutocomplete() tea.Model {
	currentInput := m.Input.Value()
	words := strings.Fields(currentInput)
	if len(words) == 0 {
		return m
	}

	lastWord := words[len(words)-1]

	var dirPath, partialFile string

	if strings.Contains(lastWord, "/") {
		lastSlash := strings.LastIndex(lastWord, "/")
		dirPath = lastWord[:lastSlash+1]
		partialFile = lastWord[lastSlash+1:]
	} else {
		dirPath = "./"
		partialFile = lastWord
	}

	listCmd := fmt.Sprintf(
		`for f in %s%s*; do [ -e "$f" ] && echo "$f" && break; done 2>/dev/null`,
		dirPath, partialFile,
	)

	var stdout string
	var err error

	if m.UseDebugContainer {
		stdout, _, err = kubectl.ExecInDebugContainer(m.Namespace, m.PodName, m.DebugContainer, m.TargetRoot, listCmd, m.CurrentDir)
	} else {
		stdout, _, err = kubectl.ExecInPod(m.Namespace, m.PodName, m.Container, listCmd, m.CurrentDir)
	}

	if err == nil && stdout != "" {
		firstMatch := strings.TrimSpace(stdout)
		if strings.Contains(firstMatch, "/") {
			firstMatch = firstMatch[strings.LastIndex(firstMatch, "/")+1:]
		}

		checkDirCmd := fmt.Sprintf(`[ -d "%s%s" ] && echo "DIR" || echo "FILE"`, dirPath, firstMatch)
		var isDirOut string
		if m.UseDebugContainer {
			isDirOut, _, _ = kubectl.ExecInDebugContainer(m.Namespace, m.PodName, m.DebugContainer, m.TargetRoot, checkDirCmd, m.CurrentDir)
		} else {
			isDirOut, _, _ = kubectl.ExecInPod(m.Namespace, m.PodName, m.Container, checkDirCmd, m.CurrentDir)
		}

		isDirOut = strings.TrimSpace(isDirOut)

		if isDirOut == "DIR" {
			firstMatch = firstMatch + "/"
		}

		if dirPath == "./" {
			words[len(words)-1] = firstMatch
		} else {
			words[len(words)-1] = dirPath + firstMatch
		}

		m.Input.SetValue(strings.Join(words, " "))
		m.Input.CursorEnd()
		return m
	}

	if len(lastWord) >= 2 {
		var matches []string
		for word := range m.AutocompleteWords {
			if strings.HasPrefix(word, lastWord) && word != lastWord {
				matches = append(matches, word)
			}
		}

		if len(matches) > 0 {
			sort.Strings(matches)
			words[len(words)-1] = matches[0]
			m.Input.SetValue(strings.Join(words, " "))
			m.Input.CursorEnd()
		}
	}
	return m
}

func (m *Model) handleHistoryUp() tea.Model {
	if len(m.History) == 0 {
		return m
	}
	if m.HistIdx == -1 {
		m.HistIdx = len(m.History) - 1
	} else if m.HistIdx > 0 {
		m.HistIdx--
	}
	m.Input.SetValue(m.History[m.HistIdx])
	m.Input.CursorEnd()
	return m
}

func (m *Model) handleHistoryDown() tea.Model {
	if len(m.History) == 0 {
		return m
	}
	if m.HistIdx == -1 {
		return m
	}
	if m.HistIdx < len(m.History)-1 {
		m.HistIdx++
		m.Input.SetValue(m.History[m.HistIdx])
	} else {
		m.HistIdx = -1
		m.Input.SetValue("")
	}
	m.Input.CursorEnd()
	return m
}

func (m *Model) handleCommand(cmds *[]tea.Cmd) (tea.Model, tea.Cmd) {
	cmdline := strings.TrimSpace(m.Input.Value())

	if cmdline == "" {
		return m, nil
	}
	m.Input.SetValue("")
	m.HistIdx = -1

	if cmdline == "clear" {
		m.Output.Reset()
		m.OutputLines = nil
		m.Vp.SetContent("")
		return m, nil
	}

	if cmdline == "/quit" {
		return m, tea.Quit
	}

	if strings.HasPrefix(cmdline, "/copy ") {
		return m.handleCopyCommand(cmdline), nil
	}

	if strings.HasPrefix(cmdline, "cd ") || cmdline == "cd" {
		return m.handleCdCommand(cmdline), nil
	}

	if len(m.History) == 0 || m.History[len(m.History)-1] != cmdline {
		m.History = append(m.History, cmdline)
	}

	m.Loading = true
	*cmds = append(*cmds, m.Spin.Tick, runCommand(m.Namespace, m.PodName, m.Container, cmdline, m.CurrentDir, m.UseDebugContainer, m.DebugContainer, m.TargetRoot))
	return m, tea.Batch(*cmds...)
}

func (m *Model) handleCopyCommand(cmdline string) tea.Model {
	rangeStr := strings.TrimSpace(strings.TrimPrefix(cmdline, "/copy"))
	var startLine, endLine int

	if strings.Contains(rangeStr, ",") {
		fmt.Sscanf(rangeStr, "%d,%d", &startLine, &endLine)
	} else if strings.Contains(rangeStr, "-") {
		fmt.Sscanf(rangeStr, "%d-%d", &startLine, &endLine)
	} else {
		fmt.Sscanf(rangeStr, "%d", &startLine)
		endLine = startLine
	}

	if startLine < 1 || endLine < startLine || endLine > len(m.OutputLines) {
		m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Invalid range. Available lines: 1-%d", len(m.OutputLines))))
		return m
	}

	linesToCopy := m.OutputLines[startLine-1 : endLine]
	textToCopy := strings.Join(linesToCopy, "\n")

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
		m.AppendOutput(ErrStyle.Render("No clipboard utility found (pbcopy/xclip/xsel/clip.exe)"))
		return m
	}

	cmd.Stdin = strings.NewReader(textToCopy)
	if err := cmd.Run(); err != nil {
		m.AppendOutput(ErrStyle.Render(fmt.Sprintf("Failed to copy: %v", err)))
	} else {
		lineCount := endLine - startLine + 1
		m.AppendOutput(OkStyle.Render(fmt.Sprintf("✓ Copied %d line(s) to clipboard", lineCount)))
	}

	return m
}

func (m *Model) handleCdCommand(cmdline string) tea.Model {
	newDir := strings.TrimSpace(strings.TrimPrefix(cmdline, "cd"))
	if newDir == "" || newDir == "~" {
		m.CurrentDir = ""
	} else if strings.HasPrefix(newDir, "/") {
		m.CurrentDir = newDir
	} else {
		if m.CurrentDir == "" {
			m.CurrentDir = newDir
		} else {
			m.CurrentDir = m.CurrentDir + "/" + newDir
		}
	}

	m.AutocompleteWords = make(map[string]bool)

	if len(m.History) == 0 || m.History[len(m.History)-1] != cmdline {
		m.History = append(m.History, cmdline)
	}

	dirDisplay := m.CurrentDir
	if dirDisplay == "" {
		dirDisplay = "~"
	}
	m.AppendOutput(fmt.Sprintf("» %s", cmdline))
	m.AppendOutput(OkStyle.Render(fmt.Sprintf("Working directory: %s", dirDisplay)))
	return m
}

func (m *Model) handleSelection(k string, cmds *[]tea.Cmd) (tea.Model, tea.Cmd) {
	switch k {
	case "enter":
		if len(m.Lst.Items()) == 0 {
			return m, nil
		}
		chosen, ok := m.Lst.SelectedItem().(types.ListItem)
		if !ok {
			return m, nil
		}
		val := chosen.Title()

		switch m.Step {
		case types.StepPickNS:
			m.Namespace = val
			m.Step = types.StepPickType
			m.Loading = true
			return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickType, m))

		case types.StepPickType:
			m.Rtype = types.ResType(val)
			m.Step = types.StepPickOwnerOrPod
			m.Loading = true
			return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickOwnerOrPod, m))

		case types.StepPickOwnerOrPod:
			if m.Rtype == types.RtPod {
				m.PodName = val
				m.Step = types.StepPickContainer
				m.Loading = true
				return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickContainer, m))
			}
			m.OwnerName = val
			m.Step = types.StepPickPodFromOwner
			m.Loading = true
			return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickPodFromOwner, m))

		case types.StepPickPodFromOwner:
			m.PodName = val
			m.Step = types.StepPickContainer
			m.Loading = true
			return m, tea.Batch(m.Spin.Tick, loadStep(types.StepPickContainer, m))

		case types.StepPickContainer:
			m.Container = val
			m.Step = types.StepShell
			m.Loading = false
			m.Output.Reset()
			m.Vp.SetContent("")
			m.Input.Focus()

			if m.Width > 0 && m.Height > 0 {
				m.Vp.Width = m.Width - 2
				m.Vp.Height = m.Height - 3
				m.Input.Width = m.Width - 2
			}

			return m, nil
		}
	}

	var cmd tea.Cmd
	m.Lst, cmd = m.Lst.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	*cmds = append(*cmds, cmd)
	return m, tea.Batch(*cmds...)
}
