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
	nsList        []string
	typeList      []resType
	ownerList     []string
	podList       []string
	containerList []string

	// repl
	output            strings.Builder
	outputLines       []string        // track output lines for copying
	autocompleteWords map[string]bool // unique words from output for autocomplete
	history           []string
	histIdx           int
	lastErr           string
	width             int
	height            int
	currentDir        string // track current directory in pod

	// debug container support
	useDebugContainer         bool   // whether to use ephemeral debug container
	debugContainer            string // name of the debug container
	targetRoot                string // root filesystem path of target container (/proc/<pid>/root)
	originalPodSecurityPolicy string // original PodSecurity enforce policy to restore on quit
	changedPodSecurityPolicy  bool   // whether we changed the policy

	// quit handling
	quitting bool // whether we're in the process of quitting
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
	delegate.SetSpacing(0) // Compact spacing
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

func getPodSecurityPolicy(namespace string) (string, error) {
	// Get the current pod-security.kubernetes.io/enforce label
	args := []string{"get", "namespace", namespace, "-o", "jsonpath={.metadata.labels.pod-security\\.kubernetes\\.io/enforce}"}
	out, _, err := runKubectl(args...)
	if err != nil {
		return "", err
	}
	policy := strings.TrimSpace(string(out))
	if policy == "" {
		// No policy set - return empty string to indicate no label
		return "", nil
	}
	return policy, nil
}

func setPodSecurityPolicy(namespace, policy string) error {
	if policy == "" {
		// Remove the label if policy is empty
		args := []string{"label", "namespace", namespace, "pod-security.kubernetes.io/enforce-"}
		_, stderr, err := runKubectl(args...)
		if err != nil {
			return fmt.Errorf("failed to remove PodSecurity policy label: %w (stderr: %s)", err, string(stderr))
		}
		return nil
	}

	// Set the pod-security.kubernetes.io/enforce label
	args := []string{"label", "namespace", namespace, fmt.Sprintf("pod-security.kubernetes.io/enforce=%s", policy), "--overwrite"}
	_, stderr, err := runKubectl(args...)
	if err != nil {
		return fmt.Errorf("failed to set PodSecurity policy: %w (stderr: %s)", err, string(stderr))
	}
	return nil
}

func createDebugContainer(namespace, pod, targetContainer string) (string, string, error) {
	debugName := fmt.Sprintf("kcmd-debug-%d", time.Now().Unix())

	// Build ephemeral container spec
	// Run as root with capabilities needed for nsenter
	ephemeralContainer := map[string]interface{}{
		"name":                debugName,
		"image":               "busybox:latest",
		"targetContainerName": targetContainer,
		"command":             []string{"sleep", "3600"},
		"securityContext": map[string]interface{}{
			"allowPrivilegeEscalation": false,
			"runAsUser":                0, // Run as root
			"capabilities": map[string]interface{}{
				"drop": []string{"ALL"},
				"add":  []string{"SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE"}, // Required for nsenter
			},
			"seccompProfile": map[string]interface{}{
				"type": "RuntimeDefault",
			},
		},
	}

	// Get current pod spec to append ephemeral container
	getPodCmd := []string{"get", "pod", pod, "-n", namespace, "-o", "json"}
	podJSON, _, err := runKubectl(getPodCmd...)
	if err != nil {
		return "", "", fmt.Errorf("failed to get pod: %w", err)
	}

	var podSpec map[string]interface{}
	if err := json.Unmarshal(podJSON, &podSpec); err != nil {
		return "", "", fmt.Errorf("failed to parse pod spec: %w", err)
	}

	// Add ephemeral container to spec
	spec := podSpec["spec"].(map[string]interface{})
	ephemeralContainers, ok := spec["ephemeralContainers"].([]interface{})
	if !ok {
		ephemeralContainers = []interface{}{}
	}
	ephemeralContainers = append(ephemeralContainers, ephemeralContainer)
	spec["ephemeralContainers"] = ephemeralContainers

	// Marshal back to JSON
	patchedSpec, err := json.Marshal(podSpec)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal patched spec: %w", err)
	}

	// Apply the patch
	patchCmd := []string{
		"replace", "--raw",
		fmt.Sprintf("/api/v1/namespaces/%s/pods/%s/ephemeralcontainers", namespace, pod),
		"-f", "-",
	}

	cmd := exec.Command("kubectl", patchCmd...)
	cmd.Stdin = bytes.NewReader(patchedSpec)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", "", fmt.Errorf("failed to create ephemeral container: %w (stderr: %s)", err, stderr.String())
	}

	// Wait for the ephemeral container to be ready (up to 10 seconds)
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		checkCmd := []string{"get", "pod", pod, "-n", namespace, "-o", "jsonpath={.status.ephemeralContainerStatuses[?(@.name==\"" + debugName + "\")].state.running}"}
		out, _, err := runKubectl(checkCmd...)
		if err == nil && len(out) > 0 && string(out) != "map[]" {
			// Container is running
			break
		}
	}

	// Find the target container's PID
	// With --target, we share process namespace, so target processes are visible
	// Simply use PID 1 which is always the main process of the target container
	pid := "1"

	// Test if nsenter is available and works
	testNsenterCmd := []string{
		"-n", namespace, "exec", pod,
		"-c", debugName,
		"--",
		"sh", "-c",
		fmt.Sprintf("nsenter -t %s -m -u -i -p -- pwd 2>&1", pid),
	}
	nsenterOut, _, nsenterErr := runKubectl(testNsenterCmd...)

	if nsenterErr == nil && strings.TrimSpace(string(nsenterOut)) != "" {
		// nsenter works! Use it
		return debugName, fmt.Sprintf("NSENTER:%s", pid), nil
	}

	// Fallback to /proc/<pid>/root if nsenter doesn't work
	targetRoot := fmt.Sprintf("/proc/%s/root", pid)
	return debugName, targetRoot, nil
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

func execInDebugContainer(namespace, pod, debugContainer, targetRoot, cmdline, currentDir string) (string, string, error) {
	// Check if we should use nsenter (targetRoot starts with NSENTER:)
	if strings.HasPrefix(targetRoot, "NSENTER:") {
		pid := strings.TrimPrefix(targetRoot, "NSENTER:")

		// Build command to run in target namespace using nsenter
		targetCmd := cmdline
		if currentDir != "" && currentDir != "~" {
			targetCmd = fmt.Sprintf("cd %s && %s", currentDir, cmdline)
		}

		// Escape the command for shell
		escapedCmd := strings.ReplaceAll(targetCmd, "'", "'\"'\"'")
		fullCmd := fmt.Sprintf("nsenter -t %s -m -u -i -p -- sh -c '%s'", pid, escapedCmd)

		out, errb, err := runKubectl("-n", namespace, "exec", pod, "-c", debugContainer, "--", "sh", "-c", fullCmd)
		return string(out), string(errb), err
	}

	// Fallback to /proc/<pid>/root approach
	var fullCmd string
	if currentDir != "" && currentDir != "~" {
		fullCmd = fmt.Sprintf("cd %s%s && %s", targetRoot, currentDir, cmdline)
	} else {
		// Default to root of target container
		fullCmd = fmt.Sprintf("cd %s && %s", targetRoot, cmdline)
	}

	out, errb, err := runKubectl("-n", namespace, "exec", pod, "-c", debugContainer, "--", "sh", "-c", fullCmd)
	return string(out), string(errb), err
}

// ---------- tea messages ----------

type loadMsg struct {
	step step
	err  error

	values []string
}

type debugContainerMsg struct {
	debugContainer string
	targetRoot     string
	err            error
}

type cmdResultMsg struct {
	cmd    string
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

func runCommand(ns, pod, container, cmdline, currentDir string, useDebug bool, debugContainer, targetRoot string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		var stdout, stderr string
		var err error

		if useDebug {
			stdout, stderr, err = execInDebugContainer(ns, pod, debugContainer, targetRoot, cmdline, currentDir)
		} else {
			stdout, stderr, err = execInPod(ns, pod, container, cmdline, currentDir)
		}

		return cmdResultMsg{
			cmd: cmdline, stdout: stdout, stderr: stderr, err: err, took: time.Since(start),
		}
	}
}

func createDebugContainerCmd(ns, pod, container string) tea.Cmd {
	return func() tea.Msg {
		debugName, targetRoot, err := createDebugContainer(ns, pod, container)
		return debugContainerMsg{
			debugContainer: debugName,
			targetRoot:     targetRoot,
			err:            err,
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
		return helpStyle.Render("enter=kjør  tab=autocomplete  ↑/↓=historikk  pgup/pgdn=scroll  /copy 1,10=copy  /quit=exit  ctrl+r=retarget")
	default:
		return helpStyle.Render("enter=velg  / = filter  esc=tilbake  ctrl+c=quit")
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
			m.vp.Width = msg.Width - 2   // just padding
			m.vp.Height = msg.Height - 3 // header(1) + help(1) + input(1)
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

		// Check if command failed due to missing shell (scratch/distroless container)
		if msg.err != nil && !m.useDebugContainer &&
			(strings.Contains(msg.stderr, "executable file not found") ||
				strings.Contains(msg.stderr, "OCI runtime exec failed") ||
				strings.Contains(msg.stderr, "not found")) {
			// Try to create debug container
			m.appendOutput(errStyle.Render("Container has no shell. Creating ephemeral debug container..."))
			
			// Check if namespace policy is privileged, if not change it first
			currentPolicy, err := getPodSecurityPolicy(m.namespace)
			if err != nil {
				m.appendOutput(errStyle.Render(fmt.Sprintf("Failed to get current policy: %v", err)))
				return m, nil
			}
			
			if currentPolicy != "privileged" {
				m.appendOutput(fmt.Sprintf("Namespace policy is '%s', changing to 'privileged'...", currentPolicy))
				m.originalPodSecurityPolicy = currentPolicy
				
				if err := setPodSecurityPolicy(m.namespace, "privileged"); err != nil {
					m.appendOutput(errStyle.Render(fmt.Sprintf("Failed to change policy: %v", err)))
					m.appendOutput("You may need permissions to modify namespace labels.")
					return m, nil
				}
				
				m.changedPodSecurityPolicy = true
				m.appendOutput(okStyle.Render("✓ Changed namespace policy to 'privileged'"))
				m.appendOutput("Policy will be restored to original on quit.")
				m.appendOutput("")
			}
			
			m.loading = true
			return m, tea.Batch(m.spin.Tick, createDebugContainerCmd(m.namespace, m.podName, m.container))
		}

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

	case debugContainerMsg:
		m.loading = false
		if msg.err != nil {
			m.lastErr = msg.err.Error()

			// Check if error is due to PodSecurity policy restricting root
			if strings.Contains(msg.err.Error(), "runAsNonRoot") ||
				strings.Contains(msg.err.Error(), "runAsUser=0") ||
				strings.Contains(msg.err.Error(), "PodSecurity") {
				m.appendOutput(errStyle.Render("Debug container creation blocked by restricted PodSecurity policy"))
				m.appendOutput("")
				m.appendOutput("Attempting to temporarily change namespace policy to 'privileged'...")

				// Get current policy
				currentPolicy, err := getPodSecurityPolicy(m.namespace)
				if err != nil {
					m.appendOutput(errStyle.Render(fmt.Sprintf("Failed to get current policy: %v", err)))
					return m, nil
				}
				m.originalPodSecurityPolicy = currentPolicy

				// Set to privileged (baseline doesn't have enough permissions for nsenter)
				if err := setPodSecurityPolicy(m.namespace, "privileged"); err != nil {
					m.appendOutput(errStyle.Render(fmt.Sprintf("Failed to change policy: %v", err)))
					m.appendOutput("You may need permissions to modify namespace labels.")
					return m, nil
				}

				m.changedPodSecurityPolicy = true
				if currentPolicy == "" {
					m.appendOutput(okStyle.Render("Changed namespace from no policy (unrestricted) to 'privileged'"))
				} else {
					m.appendOutput(okStyle.Render(fmt.Sprintf("Changed namespace policy from '%s' to 'privileged'", currentPolicy)))
				}
				m.appendOutput("Policy will be restored to original on quit.")
				m.appendOutput("")
				m.appendOutput("Retrying debug container creation...")
				m.loading = true
				return m, tea.Batch(m.spin.Tick, createDebugContainerCmd(m.namespace, m.podName, m.container))
			} else {
				m.appendOutput(errStyle.Render(fmt.Sprintf("Failed to create debug container: %v", msg.err)))
			}
			return m, nil
		}

		m.useDebugContainer = true
		m.debugContainer = msg.debugContainer
		m.targetRoot = msg.targetRoot
		m.currentDir = "/" // Start at root of target container
		m.appendOutput(okStyle.Render(fmt.Sprintf("Debug container '%s' created.", msg.debugContainer)))
		m.appendOutput(fmt.Sprintf("Target container filesystem: %s", msg.targetRoot))
		m.appendOutput("")
		m.appendOutput("Testing filesystem access...")

		// Test if we can actually access the target root
		testCmd := fmt.Sprintf("ls %s 2>&1 | head -5", msg.targetRoot)
		m.loading = true
		return m, tea.Batch(m.spin.Tick, runCommand(m.namespace, m.podName, m.container, testCmd, "", true, m.debugContainer, m.targetRoot))

	case tea.KeyMsg:
		k := msg.String()

		// global quit (only Ctrl+C, q is just a regular key now)
		if k == "ctrl+c" {
			// Note: Ephemeral containers cannot be removed, they persist until pod restart
			// Policy restoration happens in main() after TUI exits
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

				// Determine directory and partial filename
				var dirPath, partialFile string
				
				if strings.Contains(lastWord, "/") {
					// Path with directory component: /usr/lo or config/us
					lastSlash := strings.LastIndex(lastWord, "/")
					dirPath = lastWord[:lastSlash+1]
					partialFile = lastWord[lastSlash+1:]
				} else {
					// No slash: relative to current directory
					dirPath = "./"
					partialFile = lastWord
				}

				// Query filesystem for autocomplete
				// First list files, then check if first match is a directory
				listCmd := fmt.Sprintf(
					`for f in %s%s*; do [ -e "$f" ] && echo "$f" && break; done 2>/dev/null`,
					dirPath, partialFile,
				)

				var stdout string
				var err error

				// Use appropriate execution method based on whether we're in debug container
				if m.useDebugContainer {
					stdout, _, err = execInDebugContainer(m.namespace, m.podName, m.debugContainer, m.targetRoot, listCmd, m.currentDir)
				} else {
					stdout, _, err = execInPod(m.namespace, m.podName, m.container, listCmd, m.currentDir)
				}
				
				// If we got a match, check if it's a directory
				if err == nil && stdout != "" {
					firstMatch := strings.TrimSpace(stdout)
					// Extract just the basename
					if strings.Contains(firstMatch, "/") {
						firstMatch = firstMatch[strings.LastIndex(firstMatch, "/")+1:]
					}
					
					// Check if it's a directory
					checkDirCmd := fmt.Sprintf(`[ -d "%s%s" ] && echo "DIR" || echo "FILE"`, dirPath, firstMatch)
					var isDirOut string
					if m.useDebugContainer {
						isDirOut, _, _ = execInDebugContainer(m.namespace, m.podName, m.debugContainer, m.targetRoot, checkDirCmd, m.currentDir)
					} else {
						isDirOut, _, _ = execInPod(m.namespace, m.podName, m.container, checkDirCmd, m.currentDir)
					}
					
					isDirOut = strings.TrimSpace(isDirOut)
					
					// Append / if it's a directory
					if isDirOut == "DIR" {
						firstMatch = firstMatch + "/"
					}
					
					// Update input
					if dirPath == "./" {
						words[len(words)-1] = firstMatch
					} else {
						words[len(words)-1] = dirPath + firstMatch
					}
					
					m.input.SetValue(strings.Join(words, " "))
					m.input.CursorEnd()
					return m, nil  // Don't fall through to word-based autocomplete
				}
				
				// Fall back to word-based autocomplete from output if len >= 2
				if len(lastWord) >= 2 {
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

				// Check if command is /quit to quit application
				if cmdline == "/quit" {
					// Note: Ephemeral containers cannot be removed from running pods
					// They persist until the pod is deleted/restarted
					// Policy restoration will happen in main() after TUI exits

					return m, tea.Quit
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
				cmds = append(cmds, m.spin.Tick, runCommand(m.namespace, m.podName, m.container, cmdline, m.currentDir, m.useDebugContainer, m.debugContainer, m.targetRoot))
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
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// After TUI exits and screen is restored, do cleanup
	if m, ok := finalModel.(*model); ok {
		if m.changedPodSecurityPolicy {
			if m.originalPodSecurityPolicy == "" {
				fmt.Println("Removing PodSecurity policy label...")
			} else {
				fmt.Printf("Restoring namespace policy to '%s'...\n", m.originalPodSecurityPolicy)
			}

			if err := setPodSecurityPolicy(m.namespace, m.originalPodSecurityPolicy); err != nil {
				fmt.Printf("Failed to restore policy: %v\n", err)
			} else {
				fmt.Println("✓ Policy restored successfully")
			}

			time.Sleep(2 * time.Second)
		}

		// Offer to delete pod if ephemeral container was used
		if m.debugContainer != "" {
			fmt.Printf("\nEphemeral container '%s' was created in pod '%s'.\n", m.debugContainer, m.podName)
			fmt.Println("Ephemeral containers cannot be removed without deleting the pod.")
			fmt.Print("Delete and recreate the pod? [y/N]: ")

			var response string
			fmt.Scanln(&response)
			if strings.ToLower(strings.TrimSpace(response)) == "y" {
				fmt.Printf("Deleting pod '%s'...\n", m.podName)
				args := []string{"delete", "pod", m.podName, "-n", m.namespace}
				_, stderr, err := runKubectl(args...)
				if err != nil {
					fmt.Printf("Failed to delete pod: %v (stderr: %s)\n", err, string(stderr))
				} else {
					fmt.Println("✓ Pod deleted successfully. It will be recreated by the controller.")
				}
			}
		}
	}
}
