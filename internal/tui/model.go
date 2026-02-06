package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"

	"kui/internal/types"
)

type Model struct {
	Step types.Step

	// selections
	Namespace string
	Rtype     types.ResType

	OwnerName string
	PodName   string
	Container string

	// ui components
	Lst     list.Model
	Input   textinput.Model
	Vp      viewport.Model
	Spin    spinner.Model
	Loading bool

	// data cache
	NsList        []string
	TypeList      []types.ResType
	OwnerList     []string
	PodList       []string
	ContainerList []string

	// repl
	Output            strings.Builder
	OutputLines       []string
	AutocompleteWords map[string]bool
	History           []string
	HistIdx           int
	LastErr           string
	Width             int
	Height            int
	CurrentDir        string

	// debug container support
	UseDebugContainer         bool
	DebugContainer            string
	TargetRoot                string
	OriginalPodSecurityPolicy string
	ChangedPodSecurityPolicy  bool

	// quit handling
	Quitting bool
}

func InitialModel() *Model {
	delegate := list.NewDefaultDelegate()
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	l := list.New([]list.Item{}, delegate, 0, 0)
	l.Title = "Velg namespace"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)

	in := textinput.New()
	in.Placeholder = "skriv kommando… (clear / ctrl+r / q)"
	in.Focus()
	in.CharLimit = 4096
	in.Width = 60

	vp := viewport.New(0, 0)
	vp.SetContent("")

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return &Model{
		Step:              types.StepPickNS,
		Lst:               l,
		Input:             in,
		Vp:                vp,
		Spin:              sp,
		TypeList:          []types.ResType{types.RtPod, types.RtDeployment, types.RtStatefulSet},
		HistIdx:           -1,
		AutocompleteWords: make(map[string]bool),
	}
}
