// Package tui is zimon's interactive catalog browser (`zimon browse`).
package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/kazie/zimon/internal/api"
	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/collections"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/ui"
)

// Backend is what the browser needs from where the library lives: here,
// or on a remote's zimon serve (see package backend).
type Backend interface {
	Info(ctx context.Context) (api.Info, error)
	Items(ctx context.Context) ([]api.Item, error)
	Search(ctx context.Context, q api.Query) ([]api.Entry, int, error)
	Facets(ctx context.Context, what string) ([]api.Facet, error)
	Collections(ctx context.Context) ([]api.Collection, error)
	Preview(ctx context.Context, kind string, req api.Request) (*api.Preview, error)
	Run(ctx context.Context, kind string, req api.Request, emit func(api.Event)) error
}

// Deps is what the browser needs from the CLI.
type Deps struct {
	Backend Backend
	Remote  string // the remote's name; "" when the library is here
}

// Run shows the browser until the user quits. Downloads still running are
// cancelled on quit, and resume next time; on a remote they carry on.
func Run(parent context.Context, d Deps) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	sh := &shared{events: make(chan tea.Msg, 64)}
	m, err := newModel(ctx, d, sh)
	if err != nil {
		return err
	}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	// Stop downloads and wait for them to checkpoint (and anything else to
	// finish its bookkeeping) before the database closes. A second Ctrl-C
	// still exits at once.
	cancel()
	sh.wg.Wait()
	if parent.Err() != nil {
		return parent.Err() // SIGTERM or similar
	}
	return err
}

// shared is state that must not be copied with the model.
type shared struct {
	events chan tea.Msg
	wg     sync.WaitGroup // background tasks; Run waits for them on quit
}

type model struct {
	ctx context.Context
	d   Deps
	sh  *shared

	width, height int
	help          help.Model // the footer's key hints; ? shows them all
	input         textinput.Model
	filtering     bool
	categories    []api.Facet // index 0 is "all"
	catIdx        int
	allLangs      bool

	entries   []api.Entry
	searchSeq int                  // the latest search asked for
	itemsSeq  int                  // the latest read of the items asked for
	langs     []string             // the library's languages (l: all of them)
	nameW     int                  // widest name in entries
	total     int                  // catalog size
	items     map[string]*api.Item // by key
	cursor    int
	offset    int

	queue    []queued
	removing map[string]bool // removals running, by key
	active   *download
	confirm  *confirmation
	status   string

	// The collections view (c).
	showCols             bool
	cols                 []api.Collection
	colCursor, colOffset int
	job                  string // the collection change running, "" when none
}

type download struct {
	key         string
	done, total int64
	note        string
}

type confirmation struct {
	question string
	onYes    func(m *model) tea.Cmd
}

// Messages.
type (
	eventMsg   api.Event
	installMsg struct {
		key string
		err error
	}
	removeMsg struct {
		key   string
		freed int64
		err   error
	}
	searchMsg struct {
		seq     int
		entries []api.Entry
		err     error
	}
	itemsMsg struct {
		seq   int
		items map[string]*api.Item
		err   error
	}
	colsMsg struct {
		cols []api.Collection
		err  error
	}
	jobMsg struct {
		label         string
		steps, failed int
		err           error
	}
)

func newModel(ctx context.Context, d Deps, sh *shared) (model, error) {
	in := textinput.New()
	in.Prompt = "/ "
	in.Placeholder = "type to filter (wikipedia, stack exchange, python…)"
	m := model{ctx: ctx, d: d, sh: sh, input: in, help: newHelp(), width: 100, height: 30, removing: map[string]bool{}}
	cats, err := d.Backend.Facets(ctx, "categories")
	if err != nil {
		return m, err
	}
	m.categories = append([]api.Facet{{}}, cats...)
	info, err := d.Backend.Info(ctx)
	if err != nil {
		return m, err
	}
	m.total, m.langs = info.Entries, info.Languages
	found := m.search()().(searchMsg)
	if found.err != nil {
		return m, found.err
	}
	m.setEntries(found.entries)
	items := m.loadItems()().(itemsMsg)
	m.items = items.items
	return m, items.err
}

func (m model) Init() tea.Cmd { return m.listen() }

// listen waits for the next event from a running download.
func (m model) listen() tea.Cmd {
	return func() tea.Msg {
		select {
		case msg := <-m.sh.events:
			return msg
		case <-m.ctx.Done():
			return nil
		}
	}
}

func (m *model) languages() []string {
	if m.allLangs {
		return nil
	}
	return m.langs
}

// search runs the catalog query for the current filter and facets in the
// background (on a remote, it is a request); only the latest one's answer
// is shown.
func (m *model) search() tea.Cmd {
	m.searchSeq++
	seq, ctx, be := m.searchSeq, m.ctx, m.d.Backend
	q := api.Query{Text: m.input.Value(), Languages: m.languages(), Category: m.categories[m.catIdx].Value, NoState: true} // m.items has it
	return func() tea.Msg {
		entries, _, err := be.Search(ctx, q)
		return searchMsg{seq: seq, entries: entries, err: err}
	}
}

// setEntries shows a search's answer.
func (m *model) setEntries(entries []api.Entry) {
	m.entries, m.nameW = entries, 12
	for _, e := range entries {
		m.nameW = max(m.nameW, len(e.Key)) // names are ASCII
	}
	m.cursor = min(m.cursor, max(len(m.entries)-1, 0))
	m.clampOffset()
}

// loadItems reads what is installed, tracked or downloading, in the
// background.
func (m *model) loadItems() tea.Cmd {
	m.itemsSeq++
	seq, ctx, be := m.itemsSeq, m.ctx, m.d.Backend
	return func() tea.Msg {
		items, err := be.Items(ctx)
		return itemsMsg{seq: seq, items: api.ItemsByKey(items), err: err}
	}
}

// refilter jumps to the top of a new search after the filter or facets change.
func (m *model) refilter() tea.Cmd {
	m.cursor, m.offset = 0, 0
	return m.search()
}

func (m *model) selected() *api.Entry {
	if m.cursor < 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return &m.entries[m.cursor]
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.SetWidth(max(10, msg.Width-4))
		m.clampOffset()
		return m, nil

	case eventMsg:
		m.event(api.Event(msg))
		return m, m.listen()

	case installMsg:
		m.active = nil
		switch {
		case msg.err == nil:
			m.status = ui.OK(ui.Bold.Render(msg.key) + " downloaded and verified")
		case m.ctx.Err() != nil:
		default:
			m.status = ui.Failure(msg.key + ": " + msg.err.Error())
		}
		return m, tea.Batch(m.loadItems(), m.next())

	case jobMsg:
		m.job, m.active = "", nil
		switch {
		case msg.err == nil:
			m.status = ui.OK(ui.Bold.Render(msg.label) + fmt.Sprintf(": %d change%s done", msg.steps, ui.Plural(msg.steps)))
		case m.ctx.Err() != nil:
		case msg.failed > 0:
			m.status = ui.Failure(fmt.Sprintf("%s: %d of %d changes failed (%v); zimon apply retries", msg.label, msg.failed, msg.steps, msg.err))
		default:
			m.status = ui.Failure(msg.label + ": " + msg.err.Error())
		}
		return m, tea.Batch(m.loadItems(), m.loadCollections(), m.next())

	case searchMsg:
		switch {
		case msg.seq != m.searchSeq: // the filter changed since
		case msg.err != nil:
			m.status = ui.Failure(msg.err.Error())
		default:
			m.setEntries(msg.entries)
		}
		return m, nil

	case colsMsg:
		if msg.err != nil {
			m.status = ui.Failure(msg.err.Error())
			return m, nil
		}
		m.cols = msg.cols
		m.colCursor = min(m.colCursor, max(len(m.cols)-1, 0))
		return m, nil

	case removeMsg:
		delete(m.removing, msg.key)
		if msg.err != nil {
			m.status = ui.Failure(msg.key + ": " + msg.err.Error())
		} else {
			m.status = ui.OK("Removed " + ui.Bold.Render(msg.key) + ui.Freed(msg.freed))
		}
		return m, m.loadItems()

	case itemsMsg:
		switch {
		case msg.seq != m.itemsSeq: // a newer read is on its way
		case msg.err != nil:
			m.status = ui.Failure(msg.err.Error())
		default:
			m.items = msg.items
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.key(msg)
	}

	if m.filtering {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// event shows how a download or change in the background goes.
func (m *model) event(ev api.Event) {
	switch {
	case ev.Type == api.EvWaiting:
		m.status = ui.Dim.Render("Waiting for " + ev.Line + " to finish…")
	case ev.Type == api.EvStep:
		for _, old := range ev.Step.OldVersions {
			m.status = ui.OK("Removed old version " + old.Path)
		}
	case ev.Type == api.EvProgress && ev.Phase == api.PhaseStart && m.job != "":
		m.active = &download{key: ev.Key} // the job's next step
	}
	if !m.downloading(ev.Key) {
		return
	}
	switch {
	case ev.Type == api.EvNote:
		m.status = ui.Warning(ev.Line)
	case ev.Type != api.EvProgress:
	case ev.Phase == api.PhaseVerify:
		m.active.note = "verifying…"
	case ev.Phase == "" || ev.Phase == api.PhaseStart:
		m.active.done, m.active.total = ev.Done, ev.Total
	}
}

func (m model) key(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := msg.String()
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if k == "y" || k == "Y" {
			cmd := c.onYes(&m)
			return m, cmd
		}
		m.status = ui.Dim.Render("Cancelled.")
		return m, nil
	}

	switch {
	case key.Matches(msg, keyForceQ):
		return m.quit()
	case key.Matches(msg, keyUp):
		m.moveAny(-1)
		return m, nil
	case key.Matches(msg, keyDown):
		m.moveAny(1)
		return m, nil
	case key.Matches(msg, keyPageUp):
		m.moveAny(-m.listHeight())
		return m, nil
	case key.Matches(msg, keyPageDown):
		m.moveAny(m.listHeight())
		return m, nil
	case key.Matches(msg, keyNextCat, keyPrevCat):
		if m.showCols {
			return m, nil
		}
		if key.Matches(msg, keyNextCat) {
			m.catIdx = (m.catIdx + 1) % len(m.categories)
		} else {
			m.catIdx = (m.catIdx + len(m.categories) - 1) % len(m.categories)
		}
		return m, m.refilter()
	}

	if m.filtering {
		if key.Matches(msg, keyFilterDone) {
			m.filtering = false
			m.input.Blur()
			return m, nil
		}
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			cmd = tea.Batch(cmd, m.refilter())
		}
		return m, cmd
	}

	switch {
	case key.Matches(msg, keyQuit):
		return m.quit()
	case key.Matches(msg, keyHelp):
		m.help.ShowAll = !m.help.ShowAll
		if m.showCols {
			m.moveCol(0)
		} else {
			m.clampOffset()
		}
		return m, nil
	}
	if m.showCols {
		return m.colKey(msg)
	}

	switch {
	case key.Matches(msg, keyFilter):
		m.filtering = true
		cmd := m.input.Focus()
		return m, cmd
	case key.Matches(msg, keyClear):
		if m.input.Value() != "" {
			m.input.SetValue("")
			return m, m.refilter()
		}
	case key.Matches(msg, keyTop):
		m.cursor, m.offset = 0, 0
	case key.Matches(msg, keyBottom):
		m.cursor = max(len(m.entries)-1, 0)
		m.clampOffset()
	case key.Matches(msg, keyVimUp):
		m.move(-1)
	case key.Matches(msg, keyVimDown):
		m.move(1)
	case key.Matches(msg, keyLanguages):
		m.allLangs = !m.allLangs
		return m, m.refilter()
	case key.Matches(msg, keyAdd): // u too: the same thing, fetch the latest release
		return m.add()
	case key.Matches(msg, keyDelete):
		return m.remove()
	case key.Matches(msg, keyCollections):
		m.showCols = true
		return m, m.loadCollections()
	}
	return m, nil
}

// colKey handles keys in the collections view.
func (m model) colKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keyCatalog):
		m.showCols = false
	case key.Matches(msg, keyVimUp):
		m.moveCol(-1)
	case key.Matches(msg, keyVimDown):
		m.moveCol(1)
	case key.Matches(msg, keyTop):
		m.colCursor, m.colOffset = 0, 0
	case key.Matches(msg, keyBottom):
		m.moveCol(len(m.cols))
	case key.Matches(msg, keyLight):
		return m.setLevel(collections.Light)
	case key.Matches(msg, keyStandard):
		return m.setLevel(collections.Standard)
	case key.Matches(msg, keyDeep):
		return m.setLevel(collections.Deep)
	case key.Matches(msg, keyDrop):
		return m.dropCollection()
	}
	return m, nil
}

// loadCollections reads the collections, their sizes and your levels in
// the background; the view shows the ones read before until then.
func (m *model) loadCollections() tea.Cmd {
	ctx, be := m.ctx, m.d.Backend
	return func() tea.Msg {
		cols, err := be.Collections(ctx)
		return colsMsg{cols: cols, err: err}
	}
}

func (m *model) moveCol(delta int) {
	m.colCursor = max(0, min(len(m.cols)-1, m.colCursor+delta))
	h := m.listHeight()
	m.colOffset = max(0, min(m.colOffset, m.colCursor), m.colCursor-h+1)
}

// busyAll reports whether a download or collection change is running or
// queued; collection changes wait for a quiet moment.
func (m *model) busyAll() bool {
	return m.active != nil || m.job != "" || len(m.queue) > 0 || len(m.removing) > 0
}

// setLevel asks to put the selected collection at l, showing what it takes.
func (m model) setLevel(l collections.Level) (tea.Model, tea.Cmd) {
	if m.colCursor >= len(m.cols) {
		return m, nil
	}
	row := m.cols[m.colCursor]
	if row.Mine && row.Level == l.String() {
		m.status = ui.Dim.Render("@" + row.ID + " is already " + l.String())
		return m, nil
	}
	return m.askChange("@"+row.ID+" → "+l.String(), "level", api.Request{Target: "@" + row.ID, Choice: l.String()})
}

// dropCollection asks to remove the selected collection from library.toml.
func (m model) dropCollection() (tea.Model, tea.Cmd) {
	if m.colCursor >= len(m.cols) {
		return m, nil
	}
	row := m.cols[m.colCursor]
	if !row.Mine {
		m.status = ui.Dim.Render("@" + row.ID + " is not in your library")
		return m, nil
	}
	return m.askChange("remove @"+row.ID, "remove", api.Request{Targets: []string{"@" + row.ID}})
}

// askChange previews the change and asks before running it in the
// background.
func (m model) askChange(label, kind string, req api.Request) (tea.Model, tea.Cmd) {
	if m.busyAll() {
		m.status = ui.Warning(waitForDownload)
		return m, nil
	}
	p, err := m.d.Backend.Preview(m.ctx, kind, req)
	if err != nil {
		m.status = ui.Failure(err.Error())
		return m, nil
	}
	q := label + ": " + summary(&p.Plan) + "?"
	if len(p.Plan.Problems) > 0 {
		q = p.Plan.Problems[0] + ". " + q
	}
	req.Plan = p.Plan.ID
	m.confirm = &confirmation{question: q, onYes: func(m *model) tea.Cmd {
		if m.busyAll() { // something started while we were asking
			m.status = ui.Warning(waitForDownload)
			return nil
		}
		return m.startJob(label, kind, req)
	}}
	return m, nil
}

func summary(p *api.Plan) string {
	if parts := ui.PlanParts(p.Download, p.Freed); len(parts) > 0 {
		return strings.Join(parts, ", ")
	}
	return "nothing to download or delete; save it"
}

// startJob runs a change in the background.
func (m *model) startJob(label, kind string, req api.Request) tea.Cmd {
	m.job = label
	m.status = ui.Cyan.Render("⇄ ") + label
	return m.run(kind, req, func(steps []api.StepResult, err error) tea.Msg {
		// A job spans many keys, so the error names the step's (no key is "").
		msg := jobMsg{label: label, steps: len(steps), err: cmp.Or(firstErr(steps, ""), err)}
		for _, r := range steps {
			if r.Error != "" {
				msg.failed++
			}
		}
		return msg
	})
}

// moveAny moves the cursor of whichever view is showing.
func (m *model) moveAny(delta int) {
	if m.showCols {
		m.moveCol(delta)
	} else {
		m.move(delta)
	}
}

func (m *model) move(delta int) {
	m.cursor = max(0, min(len(m.entries)-1, m.cursor+delta))
	m.clampOffset()
}

func (m *model) clampOffset() {
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	m.offset = max(0, min(m.offset, max(len(m.entries)-h, 0)))
}

func (m model) quit() (tea.Model, tea.Cmd) {
	if len(m.removing) > 0 {
		m.status = ui.Dim.Render("Finishing a removal first…")
		return m, nil
	}
	if m.active == nil && len(m.queue) == 0 && m.job == "" {
		return m, tea.Quit
	}
	q := "Downloads are running; they will resume next time. Quit?"
	switch {
	case m.d.Remote != "":
		q = "Downloads are running on " + m.d.Remote + "; they carry on there (see zimon jobs). Quit?"
	case m.job != "":
		q = m.job + " is running; zimon apply finishes it later. Quit?"
	}
	m.confirm = &confirmation{question: q, onYes: func(*model) tea.Cmd { return tea.Quit }}
	return m, nil
}

func (m model) add() (tea.Model, tea.Cmd) {
	e := m.selected()
	if e == nil {
		return m, nil
	}
	it := m.items[e.Key]
	if it != nil && it.State == library.UpToDate && it.Wanted {
		m.status = ui.Dim.Render(e.Key + " is already up to date")
		return m, nil
	}
	if it != nil && it.State == library.Held {
		m.status = ui.Dim.Render(e.Key + " is held at its release (zimon unhold " + e.Name + ")")
		return m, nil
	}
	if m.busy(e.Key) {
		return m, nil
	}
	// The same path as `zimon add <name>`: hand-pick it in library.toml,
	// then make the disk match for this name. Ask first when that deletes
	// something (another flavour, switched out once the new one is in).
	req := api.Request{Keys: []string{e.ExactKey()}}
	p, err := m.d.Backend.Preview(m.ctx, "add", req)
	if err != nil {
		m.status = ui.Failure(err.Error())
		return m, nil
	}
	req.Plan = p.Plan.ID
	q := queued{entry: *e, req: req}
	if p.Plan.Deletes() {
		m.confirm = &confirmation{
			question: "Add " + e.Key + "? " + strings.Join(p.Plan.Deleted(), ", ") + " is deleted once it is verified (" + ui.Size(p.Plan.Freed) + ").",
			onYes:    func(m *model) tea.Cmd { return m.enqueue(q) },
		}
		return m, nil
	}
	cmd := m.enqueue(q)
	return m, cmd
}

// queued is a download waiting its turn, planned when it was asked for.
type queued struct {
	entry api.Entry
	req   api.Request // with the plan shown
}

// enqueue queues a download and starts it if nothing else is running.
func (m *model) enqueue(q queued) tea.Cmd {
	m.queue = append(m.queue, q)
	if m.active != nil || m.job != "" {
		m.status = ui.Dim.Render("Queued " + q.entry.Key)
	}
	return m.next()
}

// downloading reports whether k is the download in progress.
func (m *model) downloading(k string) bool { return m.active != nil && m.active.key == k }

// busy reports whether an add or remove of k is queued or running. Only one
// operation per key at a time: the browser's tasks share one lock, so the
// lock alone does not keep them apart.
func (m *model) busy(k string) bool {
	return m.downloading(k) || m.removing[k] || m.queuedAt(k) >= 0
}

// waitsFor reports whether k is being downloaded or removed right now, and
// if so says to wait.
func (m *model) waitsFor(k string) bool {
	switch {
	case m.downloading(k):
		m.status = ui.Warning(waitForDownload)
	case m.removing[k]:
		m.status = ui.Dim.Render("Removing " + k + "…")
	default:
		return false
	}
	return true
}

// queuedAt is k's place in the download queue, or -1.
func (m *model) queuedAt(k string) int {
	return slices.IndexFunc(m.queue, func(q queued) bool { return q.entry.Key == k })
}

const waitForDownload = "Wait for the download to finish (or quit; it resumes next time)"

// run carries out a change in the background, passing its events on, and
// turns how it went into the message done makes.
func (m *model) run(kind string, req api.Request, done func(steps []api.StepResult, err error) tea.Msg) tea.Cmd {
	ctx, d, sh := m.ctx, m.d, m.sh
	sh.wg.Add(1)
	return func() tea.Msg {
		defer sh.wg.Done()
		var steps []api.StepResult
		err := d.Backend.Run(ctx, kind, req, func(ev api.Event) {
			if ev.Type == api.EvStep {
				steps = append(steps, *ev.Step)
			}
			send(ctx, sh, eventMsg(ev))
		})
		return done(steps, err)
	}
}

// next starts the next queued download, if none is running.
func (m *model) next() tea.Cmd {
	if m.active != nil || m.job != "" || len(m.queue) == 0 || m.ctx.Err() != nil {
		return nil
	}
	q := m.queue[0]
	m.queue = m.queue[1:]
	k := q.entry.Key
	m.active = &download{key: k, total: q.entry.Size}
	m.status = ui.Cyan.Render("↓ ") + "Downloading " + ui.Bold.Render(k)
	return m.run("add", q.req, func(steps []api.StepResult, err error) tea.Msg {
		return installMsg{key: k, err: cmp.Or(firstErr(steps, k), err)}
	})
}

// firstErr is the first failed step's error, if any. Callers show it after
// k, so it names the step's key only when that is another one (a flavour
// switched out, say).
func firstErr(steps []api.StepResult, k string) error {
	for _, r := range steps {
		switch {
		case r.Error == "":
		case r.Key == k:
			return errors.New(r.Error)
		default:
			return fmt.Errorf("%s: %s", r.Key, r.Error)
		}
	}
	return nil
}

func send(ctx context.Context, sh *shared, msg tea.Msg) {
	select {
	case sh.events <- msg:
	case <-ctx.Done():
	}
}

func (m model) remove() (tea.Model, tea.Cmd) {
	e := m.selected()
	if e == nil {
		return m, nil
	}
	k := e.Key
	if m.waitsFor(k) {
		return m, nil
	}
	it := m.items[k]
	// A download waiting its turn (an add or an update) is cancelled first.
	if i := m.queuedAt(k); i >= 0 {
		m.queue = slices.Delete(m.queue, i, i+1)
		msg := "Dropped " + k + " from the queue"
		if it != nil {
			msg += "; d again deletes it"
		}
		m.status = ui.Dim.Render(msg)
		return m, nil
	}
	if it == nil {
		m.status = ui.Dim.Render(k + " is not in your library")
		return m, nil
	}
	// The same path as `zimon remove`: a wanted key leaves library.toml (or
	// is switched off while a collection wants it); an unwanted one (an old
	// flavour, say) only loses its files.
	req := api.Request{Targets: []string{e.ExactKey()}}
	p, err := m.d.Backend.Preview(m.ctx, "remove", req)
	if err != nil {
		m.status = ui.Failure(err.Error())
		return m, nil
	}
	req.Plan = p.Plan.ID
	what := k
	if keys := p.Plan.Deleted(); len(keys) > 0 {
		what = strings.Join(keys, ", ")
	}
	name := e.Name
	m.confirm = &confirmation{question: ui.RemoveQuestion(what, p.Plan.Freed), onYes: func(m *model) tea.Cmd {
		if m.waitsFor(k) { // it started while we were asking
			return nil
		}
		m.queue = slices.DeleteFunc(m.queue, func(q queued) bool { return q.entry.Name == name })
		m.removing[k] = true
		return m.run("remove", req, func(steps []api.StepResult, err error) tea.Msg {
			msg := removeMsg{key: k, err: cmp.Or(firstErr(steps, k), err)}
			for _, r := range steps {
				msg.freed += api.Total(r.Removed)
			}
			return msg
		})
	}}
	return m, nil
}

// Rows around the list besides the help: header, filter, facets, column
// header, rule, detail (3), status.
const chrome = 9

func (m model) listHeight() int {
	return max(3, m.height-chrome-lipgloss.Height(m.helpView(m.width)))
}

// helpView is the key help of the view on screen: one line, or all keys
// after ?.
func (m model) helpView(w int) string {
	h := m.help
	h.SetWidth(w)
	if m.showCols {
		return h.View(collectionsHelp{})
	}
	return h.View(catalogHelp{})
}

var (
	titleBar  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Black).Background(lipgloss.Cyan).Padding(0, 1)
	selected  = lipgloss.NewStyle().Background(lipgloss.BrightBlack).Foreground(lipgloss.BrightWhite)
	facetOn   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Cyan)
	keyStyle  = lipgloss.NewStyle().Bold(true)
	ruleStyle = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
)

func (m model) View() tea.View {
	if m.showCols {
		return m.screen(m.viewCollections(m.width))
	}
	var b strings.Builder
	w := m.width

	// Header.
	langs := "all languages"
	if l := m.languages(); len(l) > 0 {
		langs = strings.Join(l, ", ")
	}
	count := ui.Num(int64(len(m.entries))) + " ZIMs"
	if len(m.entries) < m.total {
		count = ui.Num(int64(len(m.entries))) + " of " + ui.Num(int64(m.total)) + " ZIMs"
	}
	head := titleBar.Render("zimon browse") + " " + ui.Dim.Render(count+" · "+langs)
	b.WriteString(fit(head, w) + "\n")

	// Filter.
	if m.filtering || m.input.Value() != "" {
		b.WriteString(fit(m.input.View(), w) + "\n")
	} else {
		b.WriteString(ui.Dim.Render(fit("/ to filter", w)) + "\n")
	}

	// Facets.
	catText := m.categories[m.catIdx].Value
	if catText == "" {
		catText = "all"
	}
	facets := ui.Dim.Render("Category ") + facetOn.Render("‹ "+catText+" ›") + ui.Dim.Render("   Languages ") + facetOn.Render(langs)
	b.WriteString(fit(facets, w) + "\n")

	// Column header and list.
	nameW := min(m.nameW, max(20, w/3))
	fixed := 2 + nameW + 2 + 9 + 2 + 10 + 2
	titleW := max(8, w-fixed-1)
	row := func(marker, name, title, size, date string) string {
		return marker + " " + ui.Cell(name, nameW, false) + "  " + ui.Cell(title, titleW, false) + "  " +
			ui.Cell(size, 9, true) + "  " + ui.Cell(date, 10, false)
	}
	b.WriteString(ui.Header.Render(fit(row(" ", "NAME", "TITLE", "SIZE", "DATE"), w)) + "\n")
	h := m.listHeight()
	for i := m.offset; i < m.offset+h; i++ {
		if i == 0 && len(m.entries) == 0 {
			b.WriteString(fit(ui.Dim.Render("  No matches. Try another filter, tab for categories, or l for all languages."), w) + "\n")
			continue
		}
		if i >= len(m.entries) {
			b.WriteString("\n")
			continue
		}
		e := m.entries[i]
		mark := ui.Marker(m.items[e.Key])
		if m.busy(e.Key) {
			mark = ui.Cyan.Render("↓")
		}
		line := row(mark, e.Key, e.Title, ui.Size(e.Size), e.Date)
		if i == m.cursor {
			line = selected.Render(fit(ansi.Strip(line), w))
		} else {
			line = fit(line, w)
		}
		b.WriteString(line + "\n")
	}

	// Detail of the selected entry.
	b.WriteString(ruleStyle.Render(strings.Repeat("─", max(w, 1))) + "\n")
	b.WriteString(m.detail(w))

	b.WriteString(m.footer(w))
	return m.screen(b.String())
}

// footer is the key help and the status (or question) line.
func (m model) footer(w int) string {
	var out strings.Builder
	for line := range strings.SplitSeq(m.helpView(w), "\n") {
		out.WriteString(fit(line, w) + "\n")
	}
	if m.confirm != nil {
		return out.String() + fit(ui.Bold.Render(m.confirm.question)+ui.Dim.Render(" [y/N]"), w)
	}
	return out.String() + fit(m.status, w)
}

func (m model) screen(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.WindowTitle = "zimon browse"
	return v
}

// viewCollections draws the collections view: the same frame as the
// catalog, with collections and their sizes per level in the list.
func (m model) viewCollections(w int) string {
	var b strings.Builder
	mine := 0
	for _, r := range m.cols {
		if r.Mine {
			mine++
		}
	}
	b.WriteString(fit(titleBar.Render("zimon browse")+" "+ui.Dim.Render(fmt.Sprintf("collections · %d of %d in your library", mine, len(m.cols))), w) + "\n")
	b.WriteString(fit(ui.Dim.Render("Curated sets of ZIMs. Pick how deep each goes:"), w) + "\n")
	b.WriteString(fit(keyStyle.Render("1")+ui.Dim.Render(" light: the core, smallest   ")+keyStyle.Render("2")+ui.Dim.Render(" standard: more, text-only   ")+
		keyStyle.Render("3")+ui.Dim.Render(" deep: everything, with pictures"), w) + "\n")

	idW := 12
	for _, r := range m.cols {
		idW = max(idW, len(r.ID)+1)
	}
	titleW := max(8, w-(1+1+idW+2+2+9+3*11)-1) // marker, space, id, gaps, level, three sizes
	row := func(marker, id, title, level string, sizes [3]string) string {
		return marker + " " + ui.Cell(id, idW, false) + "  " + ui.Cell(title, titleW, false) + "  " + ui.Cell(level, 9, false) +
			ui.Cell(sizes[0], 11, true) + ui.Cell(sizes[1], 11, true) + ui.Cell(sizes[2], 11, true)
	}
	b.WriteString(ui.Header.Render(fit(row(" ", "NAME", "TITLE", "YOURS", [3]string{"LIGHT", "STANDARD", "DEEP"}), w)) + "\n")
	h := m.listHeight()
	for i := m.colOffset; i < m.colOffset+h; i++ {
		if i >= len(m.cols) {
			b.WriteString("\n")
			continue
		}
		r := m.cols[i]
		var sizes [3]string
		for l, level := range collections.Levels {
			sizes[l] = ui.SizeOrDash(r.Sizes[level.String()])
			if r.Mine && level.String() == r.Level {
				sizes[l] = ui.Green.Render(sizes[l])
			}
		}
		mark, level := " ", ""
		if r.Mine {
			mark, level = ui.Green.Render("✓"), r.Level
		}
		line := row(mark, "@"+r.ID, r.Title, level, sizes)
		if i == m.colCursor {
			line = selected.Render(fit(ansi.Strip(line), w))
		} else {
			line = fit(line, w)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString(ruleStyle.Render(strings.Repeat("─", max(w, 1))) + "\n")
	if m.colCursor < len(m.cols) {
		c := m.cols[m.colCursor]
		b.WriteString(fit(ui.Bold.Render(c.Title)+ui.Dim.Render(" · ")+c.Blurb, w) + "\n")
		where := ui.Dim.Render("Not in your library")
		if c.Mine {
			where = ui.Green.Render("In your library at " + c.Level)
		}
		b.WriteString(fit(where+ui.Dim.Render(fmt.Sprintf(" · %d items · zimon collections show %s", c.Items, c.ID)), w) + "\n")
	} else {
		b.WriteString("\n\n")
	}
	switch {
	case m.active != nil:
		b.WriteString(fit(progressLine(m.active, w), w) + "\n")
	case m.job != "":
		b.WriteString(fit(ui.Cyan.Render("⇄ ")+m.job, w) + "\n")
	default:
		b.WriteString("\n")
	}
	b.WriteString(m.footer(w))
	return b.String()
}

func (m model) detail(w int) string {
	e := m.selected()
	if e == nil {
		return "\n\n\n"
	}
	line1 := ui.Bold.Render(e.Title)
	if e.Summary != "" {
		line1 += ui.Dim.Render(" · ") + e.Summary
	}
	feats, _ := catalog.Features(e.Tags)
	bits := append([]string{}, feats...)
	if e.ArticleCount > 0 {
		bits = append(bits, ui.Num(e.ArticleCount)+" articles")
	}
	if e.Language != "" {
		bits = append(bits, e.Language)
	}
	if e.Creator != "" {
		bits = append(bits, e.Creator)
	}
	line2 := ui.Dim.Render(strings.Join(bits, " · "))

	line3 := ""
	it := m.items[e.Key]
	switch {
	case m.downloading(e.Key):
		line3 = progressLine(m.active, w)
	case m.busy(e.Key):
		line3 = ui.Cyan.Render("↓ ") + ui.Dim.Render("queued")
	case it != nil && it.State != library.NotInLibrary:
		line3 = ui.StateText(it)
		if it.Path != "" {
			line3 += ui.Dim.Render(" · " + filepath.Base(it.Path))
		}
		if it.State == library.UpdateAvailable {
			line3 += ui.Dim.Render(" · press u to update")
		}
	default:
		line3 = ui.Dim.Render("press a to download " + e.Key)
	}
	return fit(line1, w) + "\n" + fit(line2, w) + "\n" + fit(line3, w) + "\n"
}

func progressLine(d *download, w int) string {
	stats := ui.Stats(d.done, d.total)
	if d.note != "" {
		stats += "  " + d.note
	}
	return ui.Cyan.Render("↓ ") + ui.Bar(max(10, min(40, w-ansi.StringWidth(stats)-4)), d.done, d.total) + stats
}

func fit(s string, w int) string {
	if w <= 0 {
		return s
	}
	return ansi.Truncate(s, w, "…")
}
