package ui

import (
	"os"
	"time"

	"github.com/JonathanAriass/ccs/internal/session"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// focusArea is which pane j/k act on. Tab toggles it.
//
// Enter, r and q are deliberately NOT affected: Enter is the tool's only
// action, and making it conditional on focus introduces the one mode error
// that costs either a wrong-tab jump or a silent no-op.
type focusArea int

const (
	focusList focusArea = iota
	focusPreview
)

// pollInterval is how often the registry is re-read. The registry is small
// (tens of files) and a complete poll costs ~281us, so polling is simpler and
// cheaper than watching the filesystem.
const pollInterval = 2 * time.Second

type sessionsMsg struct {
	views []session.View
	err   error
}

type tickMsg time.Time

// costMsg carries one session's lazily-computed token/cost totals back from
// costCmd. It is keyed by SessionID (not by list index) because the cursor
// can move on while the lookup is still in flight — see the guard in
// Update's costMsg case.
type costMsg struct {
	sessionID string
	tokens    int64
	cost      float64
}

// Model is the whole TUI state.
type Model struct {
	views  []session.View
	cursor int
	width  int
	height int
	status string // transient one-line message, e.g. a failed focus
	err    error
	keys   keyMap

	focus   focusArea      // which pane j/k move
	preview viewport.Model // scrolls the last exchange only; metadata stays pinned
	layout  layoutMode     // v cycles auto -> stacked -> side-by-side; auto follows width

	names     map[string]string // sessionId -> user override; ccs's own file, never the registry
	namesFile string
	renaming  bool
	renameFor string // sessionId captured at n-press; a mid-edit re-sort must not retarget
	nameInput textinput.Model
}

func New() Model {
	ni := textinput.New()
	ni.CharLimit = 64
	ni.Prompt = ""
	return Model{
		keys:      defaultKeys(),
		names:     loadNames(namesPath()),
		namesFile: namesPath(),
		nameInput: ni,
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(pollCmd(), tickCmd())
}

// previewVisible reports whether the preview pane is rendered at all at the
// model's current layout, terminal width, and terminal height.
//
// This is the SINGLE shared source View and handleKey both consult — View to
// decide whether to draw a second pane at all, handleKey to decide whether
// j/k move the list or scroll the preview and whether Tab has anything to
// switch to. Two independently-computed conditions could drift out of
// agreement (e.g. a resize that leaves m.focus pointed at focusPreview while
// the pane it names is no longer on screen); consulting one predicate from
// both places makes that impossible rather than merely unlikely. See
// layoutGeom for the geometry itself.
func (m Model) previewVisible() bool {
	return layoutGeom(m.layout, len(m.views), m.width, m.height).PreviewShown
}

// selected returns the highlighted session, or nil when the list is empty.
func (m *Model) selected() *session.View {
	if m.cursor < 0 || m.cursor >= len(m.views) {
		return nil
	}
	return &m.views[m.cursor]
}

// exitRenameIfInputCannotBeDrawn closes an open rename the moment the input
// it lives in can no longer be shown, and must be called after every state
// change that can move either half of that condition — a resize
// (previewVisible) or a poll (selected()).
//
// handleKey's Rename case refuses to ENTER the mode when !previewVisible(),
// but entry is a door, not an invariant: m.renaming survives every message
// after it, and two different messages can invalidate the surface the input
// is drawn on without ever touching m.renaming themselves.
//   - tea.WindowSizeMsg: shrinking below the layout's PreviewShown threshold
//     mid-edit reaches renderPreview's "no second pane" branch (view.go),
//     which never calls renderPreviewMetadata — the same invisible modal
//     the entry guard exists to prevent, just reached through a different
//     door. `q` then types into a field nothing on screen shows instead of
//     quitting, while the footer legend (correctly) stops advertising
//     `n rename` — leaving it lying about what `q` currently does.
//   - sessionsMsg: every session the poll knows about can exit while one is
//     mid-rename (reconcile has nothing left to anchor the cursor to), which
//     hits renderPreview's `v == nil` branch — same defect, rarer trigger.
//
// Both collapse to one predicate: the input is drawn only when
// previewVisible() AND a session is selected (renderPreviewMetadata needs a
// non-nil *session.View). Closing the mode from a single, shared call site
// after both message types keeps that in agreement with the render side by
// construction, the same discipline previewVisible's own doc comment
// describes for View and handleKey.
func (m *Model) exitRenameIfInputCannotBeDrawn() {
	if m.renaming && (!m.previewVisible() || m.selected() == nil) {
		m.renaming = false
		m.nameInput.Blur()
	}
}

// overrideFor returns the user-set name for v, or "" when none was set.
func (m Model) overrideFor(v *session.View) string {
	return m.names[v.SessionID]
}

// costCmdForSelected requests the token/cost totals for whatever row is
// currently selected. It is a no-op (nil cmd) on an empty list — callers do
// not need to check selected() themselves before using it.
func (m Model) costCmdForSelected() tea.Cmd {
	v := m.selected()
	if v == nil {
		return nil
	}
	return costCmd(*v)
}

func pollCmd() tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return sessionsMsg{err: err}
		}
		v, err := session.Collect(session.Dir(), home)
		return sessionsMsg{views: v, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(pollInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// costCmd looks up one session's token total and cost off the render thread.
//
// This MUST be a tea.Cmd, not a direct call inside Update: CostFor is a full
// transcript scan on its FIRST call for a given file (508ms / 160MB peak heap
// on the real 88MB transcript), and Update runs on bubbletea's single
// event-loop goroutine — calling it synchronously there would freeze the whole
// TUI for the length of the scan. Later calls resume from the cached offset and
// fold in only the newly appended records, which is what makes firing this on
// every poll affordable. Running it as a Cmd also means the 2-second poll's
// own costCmd (for whatever row is selected when it fires) can be in flight
// at the same time as a cost lookup from a fresh cursor move; both call into
// session.CostFor's shared costCache concurrently, which is exactly the race
// internal/session's costMu and per-path lock guard against.
func costCmd(v session.View) tea.Cmd {
	return func() tea.Msg {
		home, err := os.UserHomeDir()
		if err != nil {
			return costMsg{sessionID: v.SessionID}
		}
		tokens, cost := session.CostFor(home, v)
		return costMsg{sessionID: v.SessionID, tokens: tokens, cost: cost}
	}
}
