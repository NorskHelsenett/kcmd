package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"

	"kui/internal/types"
)

func (m *Model) SetList(title string, values []string) {
	items := make([]list.Item, 0, len(values))
	for _, v := range values {
		items = append(items, types.NewListItem(v, ""))
	}
	m.Lst.SetItems(items)
	m.Lst.Title = title
	m.Lst.ResetSelected()
}

func (m *Model) AppendOutput(s string) {
	if s == "" {
		return
	}

	lines := strings.Split(s, "\n")
	var numbered strings.Builder

	for _, line := range lines {
		if line == "" && !strings.HasSuffix(s, "\n") {
			continue
		}

		m.OutputLines = append(m.OutputLines, line)

		words := strings.Fields(line)
		for _, word := range words {
			if len(word) > 2 && !strings.HasPrefix(word, "/") {
				m.AutocompleteWords[word] = true
			}
		}

		lineNum := len(m.OutputLines)
		if line != "" {
			fmt.Fprintf(&numbered, "%4d │ %s\n", lineNum, line)
		} else {
			fmt.Fprintf(&numbered, "%4d │ \n", lineNum)
		}
	}

	m.Output.WriteString(numbered.String())
	content := m.Output.String()
	m.Vp.SetContent(content)
	m.Vp.GotoBottom()
}
