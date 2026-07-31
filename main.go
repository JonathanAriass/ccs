package main

import (
	"fmt"
	"os"

	"github.com/JonathanAriass/ccs/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	if _, err := os.UserHomeDir(); err != nil {
		fmt.Fprintln(os.Stderr, "ccs: cannot determine home directory")
		os.Exit(1)
	}
	p := tea.NewProgram(ui.New(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "ccs:", err)
		os.Exit(1)
	}
}
