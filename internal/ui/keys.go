package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap is the single source of truth for both key behavior and legend
// text — nothing else lives in this file.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Focus   key.Binding
	Refresh key.Binding
	Quit    key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Focus: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("⏎", "focus tab"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Focus, k.Refresh, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Focus, k.Refresh}, {k.Quit}}
}
