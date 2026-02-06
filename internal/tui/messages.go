package tui

import (
	"time"

	"kui/internal/types"
)

type LoadMsg struct {
	Step   types.Step
	Err    error
	Values []string
}

type DebugContainerMsg struct {
	DebugContainer string
	TargetRoot     string
	Err            error
}

type CmdResultMsg struct {
	Cmd    string
	Stdout string
	Stderr string
	Err    error
	Took   time.Duration
}
