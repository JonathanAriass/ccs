package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap is the single source of truth for both key behavior and legend
// text — nothing else lives in this file.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Cycle   key.Binding
	Focus   key.Binding
	Refresh key.Binding
	Rename  key.Binding
	Layout  key.Binding
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
		Cycle: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("⇥", "pane"),
		),
		Focus: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("⏎", "focus tab"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Rename: key.NewBinding(
			key.WithKeys("n"),
			key.WithHelp("n", "rename"),
		),
		Layout: key.NewBinding(
			key.WithKeys("v"),
			key.WithHelp("v", "layout"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// Quit comes before Layout deliberately: bubbles' help.ShortHelpView drops
// TRAILING bindings first when the legend does not fit (see
// TestViewLegendElidesGracefullyWhenItDoesNotFit), so whichever binding is
// last is the first one an elided legend loses. Quit is the one binding this
// program cannot afford to make undiscoverable at a narrow width — losing it
// silently would leave ctrl+c as the only way out, on the exact terminals
// (narrow enough to elide) most likely to be a scrollback-limited SSH
// session. Layout can afford to be that casualty: it degrades to "not
// advertised" rather than "no visible way to quit", and auto already covers
// the common narrow case without the user ever needing to reach for `v`.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Cycle, k.Focus, k.Refresh, k.Rename, k.Quit, k.Layout}
}

// shortHelpFor is ShortHelp for the layout actually on screen: below the
// layout's PreviewShown threshold (layoutGeom) there is no second pane, so
// handleKey makes Tab a deliberate no-op — and a legend still advertising "⇥
// switch pane" there promises a key that does nothing. On the short terminal
// where this happens the legend is being elided for width anyway, so the
// binding is also the one costing room the others could use.
//
// Rename joins Cycle here for the same reason, one step worse: the rename
// input is drawn inside the preview pane's metadata block (renderPreviewMetadata),
// which View does not call at all below this threshold — so advertising "n
// rename" there would not name a no-op, it would name an INVISIBLE modal that
// still captures every keystroke (including q) into a field nothing on screen
// shows. See handleKey's own previewVisible() guard on the Rename case.
//
// previewVisible is passed in rather than recomputed so this cannot disagree
// with what View drew or with how handleKey routed the key — the same
// single-predicate discipline previewVisible's own doc comment describes.
//
// The receiver is a value, so SetEnabled mutates this copy only and the
// model's own bindings are untouched; disabled bindings are what
// help.Model.ShortHelpView skips.
func (k keyMap) shortHelpFor(previewVisible bool) []key.Binding {
	if !previewVisible {
		k.Cycle.SetEnabled(false)
		k.Rename.SetEnabled(false)
	}
	return k.ShortHelp()
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Up, k.Down}, {k.Cycle, k.Focus, k.Refresh, k.Rename, k.Layout}, {k.Quit}}
}
