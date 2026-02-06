package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"kui/internal/kubectl"
	"kui/internal/tui"
)

func main() {
	if _, err := exec.LookPath("kubectl"); err != nil {
		fmt.Fprintln(os.Stderr, "Fant ikke kubectl i PATH.")
		os.Exit(1)
	}

	p := tea.NewProgram(tui.InitialModel(), tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if m, ok := finalModel.(*tui.Model); ok {
		if m.ChangedPodSecurityPolicy {
			if m.OriginalPodSecurityPolicy == "" {
				fmt.Println("Removing PodSecurity policy label...")
			} else {
				fmt.Printf("Restoring namespace policy to '%s'...\n", m.OriginalPodSecurityPolicy)
			}

			if err := kubectl.SetPodSecurityPolicy(m.Namespace, m.OriginalPodSecurityPolicy); err != nil {
				fmt.Printf("Failed to restore policy: %v\n", err)
			} else {
				fmt.Println("✓ Policy restored successfully")
			}

			time.Sleep(2 * time.Second)
		}

		if m.DebugContainer != "" {
			fmt.Printf("\nEphemeral container '%s' was created in pod '%s'.\n", m.DebugContainer, m.PodName)
			fmt.Println("Ephemeral containers cannot be removed without deleting the pod.")
			fmt.Print("Delete and recreate the pod? [y/N]: ")

			var response string
			fmt.Scanln(&response)
			if strings.ToLower(strings.TrimSpace(response)) == "y" {
				fmt.Printf("Deleting pod '%s'...\n", m.PodName)
				args := []string{"delete", "pod", m.PodName, "-n", m.Namespace}
				_, stderr, err := kubectl.Run(args...)
				if err != nil {
					fmt.Printf("Failed to delete pod: %v (stderr: %s)\n", err, string(stderr))
				} else {
					fmt.Println("✓ Pod deleted successfully. It will be recreated by the controller.")
				}
			}
		}
	}
}
