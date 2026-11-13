package library

import (
	"bytes"
	"context"
	"slices"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/setup"
)

// OpenSetup opens library.toml for editing. When there is none yet, it
// starts from what is on disk (files and partial downloads): a library that
// has no library.toml, or lost it, is kept as it is, never read as "want
// nothing". The first edit then writes those ZIMs into [items].
func (m *Manager) OpenSetup(ctx context.Context) (*setup.Doc, error) {
	doc, err := setup.Open(m.SetupPath)
	if err != nil || doc.Exists() {
		return doc, err
	}
	downloads, err := m.Store.Downloads(ctx)
	if err != nil {
		return nil, err
	}
	files, err := m.Store.ActiveFiles(ctx)
	if err != nil {
		return nil, err
	}
	var keys []catalog.Key // files last, so a file's flavour wins over a partial's
	for _, d := range downloads {
		keys = append(keys, d.Key())
	}
	for _, f := range files {
		keys = append(keys, f.Key())
	}
	choices, err := m.choices(ctx, keys)
	if err != nil {
		return nil, err
	}
	for i, k := range keys {
		doc.SetItem(k.Name, choices[i])
	}
	return doc, nil
}

// LoadSetup reads library.toml (see OpenSetup for when there is none).
func (m *Manager) LoadSetup(ctx context.Context) (*setup.Setup, error) {
	doc, err := m.OpenSetup(ctx)
	if err != nil {
		return nil, err
	}
	return doc.Setup()
}

// Edit changes library.toml with mutate and saves it, reporting whether it
// did: an edit that changes nothing writes nothing (so it also works on a
// read-only file), except that a missing library.toml is written with what
// is on disk. Call it under the state lock, so two zimons never overwrite
// each other's edits.
func (m *Manager) Edit(ctx context.Context, mutate func(*setup.Doc) error) (saved bool, err error) {
	doc, err := m.OpenSetup(ctx)
	if err != nil {
		return false, err
	}
	if err := doc.Editable(); err != nil { // refuse before anything is tried
		return false, err
	}
	if changed, err := edit(doc, mutate); err != nil || !changed {
		return false, err
	}
	return true, doc.Save()
}

// edit applies mutate to doc and reports whether saving would change
// library.toml: the text changed, or the file doesn't exist yet (the first
// edit writes what is on disk).
func edit(doc *setup.Doc, mutate func(*setup.Doc) error) (bool, error) {
	before := doc.Bytes()
	if err := mutate(doc); err != nil {
		return false, err
	}
	return !doc.Exists() || !bytes.Equal(before, doc.Bytes()), nil
}

// Choice is how [items] asks for exactly k: its flavour, "on" for a ZIM
// that comes in one variant only, or "default" for the variant without a
// flavour when there are others.
func (m *Manager) Choice(ctx context.Context, k catalog.Key) (string, error) {
	c, err := m.choices(ctx, []catalog.Key{k})
	if err != nil {
		return "", err
	}
	return c[0], nil
}

// choices is Choice for several keys, with one catalog query.
func (m *Manager) choices(ctx context.Context, keys []catalog.Key) ([]string, error) {
	var plain []string // names asked for without a flavour
	for _, k := range keys {
		if k.Flavour == "" && !slices.Contains(plain, k.Name) {
			plain = append(plain, k.Name)
		}
	}
	fams, err := m.Store.Families(ctx, plain)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		switch {
		case k.Flavour != "":
			out[i] = k.Flavour
		case len(fams[k.Name]) > 1:
			out[i] = setup.Default
		default:
			out[i] = setup.On
		}
	}
	return out, nil
}

// Resolver resolves setups against the catalog. Use one per operation: it
// remembers what it looked up.
func (m *Manager) Resolver() *setup.Resolver { return m.resolver(nil) }

// resolver is Resolver that also knows the releases of sources a change
// adds but has not recorded yet.
func (m *Manager) resolver(pending []*Found) *setup.Resolver {
	var cat setup.Families = m.Store
	if len(pending) > 0 {
		cat = withPending{store: m.Store, pending: pending}
	}
	return &setup.Resolver{Collections: m.Collections, Languages: m.Cfg.Languages, Catalog: cat}
}

// withPending looks names up in the store, and in releases not recorded yet.
type withPending struct {
	store   setup.Families
	pending []*Found
}

func (w withPending) Families(ctx context.Context, names []string) (map[string][]catalog.Entry, error) {
	out, err := w.store.Families(ctx, names)
	if err != nil {
		return nil, err
	}
	for _, f := range w.pending {
		if slices.Contains(names, f.Entry.Name) {
			out[f.Entry.Name] = []catalog.Entry{f.Entry}
		}
	}
	return out, nil
}

// Wanted works out what s asks for.
func (m *Manager) Wanted(ctx context.Context, s *setup.Setup) (*setup.Wanted, error) {
	return m.Resolver().Resolve(ctx, s)
}

// unwantIn stops library.toml from asking for name: it drops the [items]
// line, or sets it "off" while a collection still wants the ZIM. Collections
// in leaving are on their way out and don't count.
func unwantIn(d *setup.Doc, want *setup.Want, name string, leaving []string) {
	if want != nil && slices.ContainsFunc(want.Via, func(v string) bool { return v != setup.ViaItems && !slices.Contains(leaving, v) }) {
		d.SetItem(name, setup.Off)
	} else {
		d.DeleteItem(name)
	}
}
