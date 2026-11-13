package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"

	"github.com/kazie/zimon/internal/ui"
)

// Keys for both views. The arrows, page keys, tab and ctrl+c also work while
// typing a filter.
var (
	keyUp       = key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp("↑/k", "up"))
	keyDown     = key.NewBinding(key.WithKeys("down", "ctrl+n"), key.WithHelp("↓/j", "down"))
	keyPageUp   = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up"))
	keyPageDown = key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down"))
	keyNextCat  = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next category"))
	keyPrevCat  = key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "previous category"))
	keyForceQ   = key.NewBinding(key.WithKeys("ctrl+c"))

	keyVimUp   = key.NewBinding(key.WithKeys("k"))
	keyVimDown = key.NewBinding(key.WithKeys("j"))
	keyTop     = key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g/home", "top"))
	keyBottom  = key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G/end", "bottom"))
	keyHelp    = key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "more"))
	keyQuit    = key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit"))

	// move stands for the four above in the short help.
	keyMove = key.NewBinding(key.WithKeys("up", "down", "k", "j"), key.WithHelp("↑↓", "move"))
)

// The catalog view.
var (
	keyFilter      = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter"))
	keyFilterDone  = key.NewBinding(key.WithKeys("esc", "enter"))
	keyClear       = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear the filter"))
	keyLanguages   = key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "languages"))
	keyAdd         = key.NewBinding(key.WithKeys("a", "enter", "u"), key.WithHelp("a", "add"))
	keyDelete      = key.NewBinding(key.WithKeys("d", "delete"), key.WithHelp("d", "delete"))
	keyCollections = key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "collections"))
)

// The collections view.
var (
	keyLight    = key.NewBinding(key.WithKeys("1"), key.WithHelp("1", "light"))
	keyStandard = key.NewBinding(key.WithKeys("2"), key.WithHelp("2", "standard"))
	keyDeep     = key.NewBinding(key.WithKeys("3"), key.WithHelp("3", "deep"))
	keyDrop     = key.NewBinding(key.WithKeys("x", "d", "delete"), key.WithHelp("x", "remove"))
	keyCatalog  = key.NewBinding(key.WithKeys("c", "esc"), key.WithHelp("c", "catalog"))
)

// catalogHelp and collectionsHelp are the footers of the two views.
type (
	catalogHelp     struct{}
	collectionsHelp struct{}
)

func (catalogHelp) ShortHelp() []key.Binding {
	// tab is left out so the line fits 80 columns; the Category ‹ › line hints at it.
	return []key.Binding{keyMove, keyFilter, keyLanguages, keyAdd, keyDelete, keyCollections, keyHelp, keyQuit}
}

func (catalogHelp) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{keyUp, keyDown, keyPageUp, keyPageDown, keyTop, keyBottom},
		{keyFilter, keyClear, keyNextCat, keyPrevCat, keyLanguages},
		{
			withHelp(keyAdd, "a/enter", "add or update"),
			withHelp(keyDelete, "d/del", "delete or cancel"),
			keyCollections, keyLess, keyQuit,
		},
	}
}

func (collectionsHelp) ShortHelp() []key.Binding {
	return []key.Binding{keyMove, keyLight, keyStandard, keyDeep, keyDrop, keyCatalog, keyHelp, keyQuit}
}

func (collectionsHelp) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{keyUp, keyDown, keyPageUp, keyPageDown, keyTop, keyBottom},
		// The view's header says what the levels mean.
		{keyLight, keyStandard, keyDeep, withHelp(keyDrop, "x/d", "remove")},
		{withHelp(keyCatalog, "c/esc", "back to the catalog"), keyLess, keyQuit},
	}
}

// keyLess is ? in the full help, where it hides it again.
var keyLess = withHelp(keyHelp, "?", "fewer keys")

// withHelp is b with other help text.
func withHelp(b key.Binding, keys, desc string) key.Binding {
	b.SetHelp(keys, desc)
	return b
}

// newHelp is the footer's help, in the same look as zimon's other output.
func newHelp() help.Model {
	h := help.New()
	h.ShortSeparator = "  "
	h.FullSeparator = "    "
	h.Styles = help.Styles{
		Ellipsis:       ui.Dim,
		ShortKey:       keyStyle,
		ShortDesc:      ui.Dim,
		ShortSeparator: lipgloss.NewStyle(),
		FullKey:        keyStyle,
		FullDesc:       ui.Dim,
		FullSeparator:  lipgloss.NewStyle(),
	}
	return h
}
