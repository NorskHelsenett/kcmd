package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kui/internal/kubectl"
	"kui/internal/types"
)

func loadStep(s types.Step, m *Model) tea.Cmd {
	return func() tea.Msg {
		var vals []string
		var err error

		switch s {
		case types.StepPickNS:
			vals, err = kubectl.GetNamespaces()
		case types.StepPickType:
			for _, t := range m.TypeList {
				vals = append(vals, string(t))
			}
		case types.StepPickOwnerOrPod:
			if m.Rtype == types.RtPod {
				vals, err = kubectl.GetNamesInNS(m.Namespace, types.RtPod)
			} else {
				vals, err = kubectl.GetNamesInNS(m.Namespace, m.Rtype)
			}
		case types.StepPickPodFromOwner:
			selector, e := kubectl.GetSelectorForWorkload(m.Namespace, m.Rtype, m.OwnerName)
			if e != nil {
				err = e
				break
			}
			vals, err = kubectl.GetPodsBySelector(m.Namespace, selector)
		case types.StepPickContainer:
			vals, err = kubectl.GetContainers(m.Namespace, m.PodName)
		default:
			err = nil
		}
		return LoadMsg{Step: s, Err: err, Values: vals}
	}
}

func runCommand(ns, pod, container, cmdline, currentDir string, useDebug bool, debugContainer, targetRoot string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		var stdout, stderr string
		var err error

		if useDebug {
			stdout, stderr, err = kubectl.ExecInDebugContainer(ns, pod, debugContainer, targetRoot, cmdline, currentDir)
		} else {
			stdout, stderr, err = kubectl.ExecInPod(ns, pod, container, cmdline, currentDir)
		}

		return CmdResultMsg{
			Cmd: cmdline, Stdout: stdout, Stderr: stderr, Err: err, Took: time.Since(start),
		}
	}
}

func createDebugContainerCmd(ns, pod, container string) tea.Cmd {
	return func() tea.Msg {
		debugName, targetRoot, err := kubectl.CreateDebugContainer(ns, pod, container)
		return DebugContainerMsg{
			DebugContainer: debugName,
			TargetRoot:     targetRoot,
			Err:            err,
		}
	}
}
