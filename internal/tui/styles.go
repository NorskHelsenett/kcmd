package tui

import "github.com/charmbracelet/lipgloss"

var (
	TitleStyle  = lipgloss.NewStyle().Bold(true)
	BorderStyle = lipgloss.NewStyle().Padding(0, 1)
	HelpStyle   = lipgloss.NewStyle().Faint(true)
	ErrStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	OkStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
)
