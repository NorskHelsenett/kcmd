package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"kui/internal/types"
)

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.Spin.Tick, loadStep(types.StepPickNS, m))
}
