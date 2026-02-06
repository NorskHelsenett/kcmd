package tui

import (
	"fmt"
	"strings"

	"kui/internal/types"
)

func (m Model) View() string {
	head := m.header()
	help := m.help()

	errLine := ""
	if m.LastErr != "" {
		errLine = ErrStyle.Render(m.LastErr)
	}

	if m.Step == types.StepShell {
		loading := ""
		if m.Loading {
			loading = " " + m.Spin.View() + " kjører…"
		}

		body := BorderStyle.Render(m.Vp.View())
		foot := BorderStyle.Render(m.Input.View() + loading)

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
	if m.Loading {
		loading = " " + m.Spin.View()
	}
	top := fmt.Sprintf("%s%s\n%s", head, loading, help)
	if errLine != "" {
		top += "\n" + errLine
	}
	return top + "\n" + BorderStyle.Width(m.Width-2).Render(m.Lst.View())
}

func (m Model) header() string {
	target := fmt.Sprintf("ns=%s type=%s pod=%s container=%s", m.Namespace, m.Rtype, m.PodName, m.Container)
	switch m.Step {
	case types.StepPickNS:
		return TitleStyle.Render("KCMD — Velg namespace")
	case types.StepPickType:
		return TitleStyle.Render("KCMD — Velg type")
	case types.StepPickOwnerOrPod:
		if m.Rtype == types.RtPod {
			return TitleStyle.Render("KCMD — Velg pod") + "  " + HelpStyle.Render(target)
		}
		return TitleStyle.Render("KCMD — Velg workload (deployment/statefulset)") + "  " + HelpStyle.Render(target)
	case types.StepPickPodFromOwner:
		return TitleStyle.Render("KCMD — Velg pod fra workload") + "  " + HelpStyle.Render(target)
	case types.StepPickContainer:
		return TitleStyle.Render("KCMD — Velg container") + "  " + HelpStyle.Render(target)
	case types.StepShell:
		return TitleStyle.Render("KCMD — Shell") + "  " + HelpStyle.Render(target)
	default:
		return TitleStyle.Render("KCMD")
	}
}

func (m Model) help() string {
	switch m.Step {
	case types.StepShell:
		return HelpStyle.Render("enter=kjør  tab=autocomplete  ↑/↓=historikk  pgup/pgdn=scroll  /copy 1,10=copy  /quit=exit  ctrl+r=retarget")
	default:
		return HelpStyle.Render("enter=velg  / = filter  esc=tilbake  ctrl+c=quit")
	}
}
