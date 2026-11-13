package api

import (
	"path/filepath"
	"slices"
	"strconv"

	"github.com/kazie/zimon/internal/config"
	"github.com/kazie/zimon/internal/library"
	"github.com/kazie/zimon/internal/setup"
)

// Request is the body of POST /api/v1/KIND: what a change or task asks for.
// Which fields count depends on the kind.
type Request struct {
	Keys        []string          `json:"keys,omitempty"`        // add: ZIMs to hand-pick, "name" or "name:flavour" ("name:" for none)
	Sources     []Source          `json:"sources,omitempty"`     // add: ZIMs that come from a URL, or a file on the machine itself
	Collections map[string]string `json:"collections,omitempty"` // add: collection id → level ("" for standard)
	Target      string            `json:"target,omitempty"`      // level: "@collection" or a ZIM name
	Choice      string            `json:"choice,omitempty"`      // level: a level; for a ZIM also a flavour, on, off or default
	Targets     []string          `json:"targets,omitempty"`     // remove: "@collection", "name" or "name:flavour"
	KeepFiles   bool              `json:"keep_files,omitempty"`  // remove: forget the files, leave them on disk
	Names       []string          `json:"names,omitempty"`       // update, verify: only these ZIMs; hold, unhold: these; "name" or "name:flavour"
	Budget      *int64            `json:"budget,omitempty"`      // add, budget: the size budget to set, in bytes (0: none)
	NoDownload  bool              `json:"no_download,omitempty"` // add: only edit library.toml
	Paths       []string          `json:"paths,omitempty"`       // prune: only these files, from GET /api/v1/prune
	Plan        string            `json:"plan,omitempty"`        // the plan's id from a preview: the job fails if the plan has changed since
}

// Source is a ZIM to add from a URL, or from a file's absolute path.
type Source struct {
	From   string `json:"from"`
	Name   string `json:"name,omitempty"`   // the ZIM's name in the library; default: a file's own name, a URL's file name
	SHA256 string `json:"sha256,omitempty"` // the download must have it
}

// Preview is a change's plan, from ?preview=1.
type Preview struct {
	Plan  Plan `json:"plan"`
	Edits bool `json:"edits"` // carrying it out rewrites library.toml
}

// Plan is what a change would do.
type Plan struct {
	ID       string   `json:"id"` // send it back with the change: the job runs only this plan
	Steps    []Step   `json:"steps"`
	Deferred []Step   `json:"deferred"`
	Problems []string `json:"problems"`
	Gone     []string `json:"gone"` // wanted, gone upstream, copy kept
	Current  int64    `json:"current"`
	After    int64    `json:"after"`
	Download int64    `json:"download"`
	Freed    int64    `json:"freed"`
	Budget   int64    `json:"budget"`
	Free     int64    `json:"free"` // 0: unknown
}

// Step is one thing a plan does.
type Step struct {
	Kind     string   `json:"kind"` // add, update, switch or remove
	Key      string   `json:"key"`
	Date     string   `json:"date,omitempty"`      // of the release to install
	From     []string `json:"from,omitempty"`      // keys replaced or deleted
	FromDate string   `json:"from_date,omitempty"` // of the first file replaced
	FromSize int64    `json:"from_size,omitempty"` // of the first file replaced
	Bytes    int64    `json:"bytes"`               // to download, approximately
	Freed    int64    `json:"freed"`
	Partial  int64    `json:"partial,omitempty"` // of Freed, partial downloads (remove)
	Via      []string `json:"via,omitempty"`
	Reason   string   `json:"reason,omitempty"` // for deferred steps: apply, budget, ask, problem or held
}

// Step kinds and the reasons a step waits.
const (
	StepAdd    = "add"
	StepUpdate = "update"
	StepSwitch = "switch"
	StepRemove = "remove"

	WaitApply   = "apply"
	WaitBudget  = "budget"
	WaitAsk     = "ask"
	WaitProblem = "problem"
	WaitHeld    = "held"
)

var (
	stepKinds    = [...]string{library.StepRemove: StepRemove, library.StepUpdate: StepUpdate, library.StepSwitch: StepSwitch, library.StepAdd: StepAdd}
	deferReasons = [...]string{library.DeferApply: WaitApply, library.DeferBudget: WaitBudget, library.DeferAsk: WaitAsk, library.DeferProblem: WaitProblem, library.DeferHeld: WaitHeld}
)

// StepKind is k's name.
func StepKind(k library.StepKind) string { return stepKinds[k] }

// WaitReason is r's name.
func WaitReason(r library.DeferReason) string { return deferReasons[r] }

// StepOf is st as a Step.
func StepOf(st library.Step) Step {
	j := Step{Kind: stepKinds[st.Kind], Key: st.Key.String(), Bytes: st.Bytes, Freed: st.Freed, Partial: st.Partial, Via: st.Via}
	if st.To != nil {
		j.Date = st.To.Date
	}
	for _, f := range st.From {
		j.From = append(j.From, f.Key().String())
	}
	if len(st.From) > 0 {
		j.FromDate, j.FromSize = st.From[0].Date, st.From[0].Size
	}
	return j
}

// PlanOf is p as a Plan, with free bytes of disk.
func PlanOf(p *library.Plan, free int64) Plan {
	j := Plan{ID: p.ID(), Steps: []Step{}, Deferred: []Step{}, Problems: setup.Summarize(p.Problems), Gone: p.Gone,
		Current: p.Current, After: p.After, Download: p.Download(), Freed: p.Freed(), Budget: p.Budget, Free: free}
	if j.Problems == nil {
		j.Problems = []string{}
	}
	if j.Gone == nil {
		j.Gone = []string{}
	}
	for _, st := range p.Steps {
		j.Steps = append(j.Steps, StepOf(st))
	}
	for _, d := range p.Deferred {
		sj := StepOf(d.Step)
		sj.Reason = deferReasons[d.Reason]
		j.Deferred = append(j.Deferred, sj)
	}
	return j
}

// Held reports whether deletions wait for a problem in library.toml to be
// fixed.
func (p *Plan) Held() bool {
	return slices.ContainsFunc(p.Deferred, func(s Step) bool { return s.Reason == WaitProblem })
}

// Deleted lists the keys the plan deletes, which the user may want to
// confirm: removals and the old side of flavour switches.
func (p *Plan) Deleted() []string {
	var out []string
	for _, st := range p.Steps {
		switch st.Kind {
		case StepRemove:
			out = append(out, st.Key)
		case StepSwitch:
			out = append(out, st.From...)
		}
	}
	return out
}

// Deletes reports whether the plan deletes anything.
func (p *Plan) Deletes() bool { return len(p.Deleted()) > 0 }

// Removed is a file deleted, or to delete.
type Removed struct {
	Key    string `json:"key,omitempty"`
	Path   string `json:"path"` // absolute
	Size   int64  `json:"size"`
	Reason string `json:"reason,omitempty"`
}

// RemovedOf is rs as Removeds.
func RemovedOf(rs []library.Removed) []Removed {
	out := []Removed{}
	for _, r := range rs {
		out = append(out, Removed{Key: r.Key.String(), Path: r.Path, Size: r.Size, Reason: r.Reason})
	}
	return out
}

// Suggestion is GET /api/v1/suggest: levels for collections that fit a
// budget.
type Suggestion struct {
	Levels map[string]string `json:"levels"` // collection id → level
	Budget int64             `json:"budget"` // the budget they fit
	Total  int64             `json:"total"`  // the library's size then, approximately
	Fits   bool              `json:"fits"`   // false: even light is over the budget (Total is at light)
}

// Event is one message on GET /api/v1/events, and how a change tells the
// CLI how it goes. A job's log gets each event's LogLine.
type Event struct {
	Type   string      `json:"type"`             // see the Ev constants
	Job    int64       `json:"job"`              // 0 outside zimon serve
	State  string      `json:"state,omitempty"`  // finished: done, failed, canceled or interrupted; catalog: refreshing, changed or unchanged; saved: saved or unchanged
	Line   string      `json:"line,omitempty"`   // log, note, waiting (who holds the lock), finished (the error), saved (library.toml), pruned (the file)
	Key    string      `json:"key,omitempty"`    // progress, note, verified
	Phase  string      `json:"phase,omitempty"`  // progress: start, verify or done; empty while bytes arrive
	Reason string      `json:"reason,omitempty"` // pruned: why
	Error  string      `json:"error,omitempty"`  // verified: why the file does not match
	Done   int64       `json:"done,omitempty"`
	Total  int64       `json:"total,omitempty"` // progress: the size; pruned: bytes freed; catalog: entries
	Step   *StepResult `json:"step,omitempty"`  // step
}

// Event types.
const (
	EvQueued   = "queued"
	EvStarted  = "started"
	EvWaiting  = "waiting" // for the state lock
	EvFinished = "finished"
	EvLog      = "log"      // a line for the job's log only
	EvNote     = "note"     // something to warn about: a mirror failed, the catalog is stale
	EvProgress = "progress" // a download or a check of a file
	EvStep     = "step"     // a step of a plan finished
	EvSaved    = "saved"    // library.toml was saved, or already said so
	EvPruned   = "pruned"   // an old version or partial download was deleted
	EvVerified = "verified" // a file was checked against its sha-256
	EvCatalog  = "catalog"  // the catalog was fetched
)

// The states of an EvSaved.
const (
	Saved          = "saved"
	SavedUnchanged = "unchanged" // library.toml already said so
)

// The states of an EvCatalog.
const (
	CatalogRefreshing = "refreshing" // a fetch starts
	CatalogChanged    = "changed"
	CatalogUnchanged  = "unchanged"
)

// Progress phases.
const (
	PhaseStart  = "start"
	PhaseVerify = "verify"
	PhaseDone   = "done"
)

// StepResult is how a step went.
type StepResult struct {
	Kind        string    `json:"kind"` // add, update, switch or remove
	Key         string    `json:"key"`
	Error       string    `json:"error,omitempty"`
	Canceled    bool      `json:"canceled,omitempty"`
	Path        string    `json:"path,omitempty"` // the installed file, absolute
	Size        int64     `json:"size,omitempty"`
	Verified    string    `json:"verified,omitempty"`     // how the new file was checked
	Removed     []Removed `json:"removed,omitempty"`      // the step's files, or a switch's old flavour
	OldVersions []Removed `json:"old_versions,omitempty"` // older releases deleted beyond keep_versions (Path is the file's name)
	KeptFiles   bool      `json:"kept_files,omitempty"`   // remove: the files stay on disk
}

// LogLine is ev as a line of its job's log, or "" for none.
func (ev Event) LogLine() string {
	switch ev.Type {
	case EvLog, EvNote:
		return ev.Line
	case EvSaved:
		if ev.State == SavedUnchanged {
			return "library.toml already says so"
		}
		return "Saved library.toml"
	case EvPruned:
		return "Removed " + ev.Reason + " " + ev.Line
	case EvVerified:
		if ev.Error != "" {
			return ev.Key + ": " + ev.Error
		}
	case EvCatalog:
		switch ev.State {
		case CatalogChanged:
			return "Catalog refreshed: " + strconv.FormatInt(ev.Total, 10) + " entries"
		case CatalogUnchanged:
			return "Catalog unchanged: " + strconv.FormatInt(ev.Total, 10) + " entries"
		}
	case EvStep:
		r := ev.Step
		switch {
		case r.Error != "":
			return r.Key + ": " + r.Error
		case r.Kind == StepRemove && r.KeptFiles:
			return "Forgot " + r.Key + "; its files stay on disk"
		case r.Kind == StepRemove:
			return "Removed " + r.Key + ", freeing " + config.FormatBytes(Total(r.Removed))
		default:
			line := r.Key + ": " + filepath.Base(r.Path) + " (" + config.FormatBytes(r.Size) + ")"
			if n := Total(r.Removed) + Total(r.OldVersions); n > 0 {
				line += ", freeing " + config.FormatBytes(n)
			}
			return line
		}
	}
	return ""
}

// Total is how many bytes rs take.
func Total(rs []Removed) (n int64) {
	for _, r := range rs {
		n += r.Size
	}
	return n
}
