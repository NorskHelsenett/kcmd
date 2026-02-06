package types

type Step int

const (
	StepPickNS Step = iota
	StepPickType
	StepPickOwnerOrPod
	StepPickPodFromOwner
	StepPickContainer
	StepShell
)

type ResType string

const (
	RtPod         ResType = "pod"
	RtDeployment  ResType = "deployment"
	RtStatefulSet ResType = "statefulset"
)

type ListItem struct {
	title string
	desc  string
}

func (i ListItem) Title() string       { return i.title }
func (i ListItem) Description() string { return i.desc }
func (i ListItem) FilterValue() string { return i.title }

func NewListItem(title, desc string) ListItem {
	return ListItem{title: title, desc: desc}
}
