package store

import (
	"context"

	"github.com/kazie/zimon/internal/catalog"
	"github.com/kazie/zimon/internal/store/gen"
)

// File states.
const (
	StateActive     = "active"
	StateSuperseded = "superseded"
)

// File is a ZIM on disk.
type File struct {
	UUID         string // catalog UUID
	ZimUUID      string // UUID in the file header
	Name         string
	Flavour      string
	Path         string // relative to the library dir
	Size         int64
	SHA256       string
	Date         string
	Title        string
	Summary      string
	Language     string
	Category     string
	Tags         string
	ArticleCount int64
	MediaCount   int64
	Creator      string
	Publisher    string
	State        string
}

// Key is the file's name and flavour.
func (f File) Key() catalog.Key { return catalog.Key{Name: f.Name, Flavour: f.Flavour} }

// FileFromEntry copies catalog metadata into a File.
func FileFromEntry(e catalog.Entry) File {
	return File{
		UUID: e.UUID, Name: e.Name, Flavour: e.Flavour, Date: e.Date, Title: e.Title,
		Summary: e.Summary, Language: e.Language, Category: e.Category, Tags: e.Tags,
		ArticleCount: e.ArticleCount, MediaCount: e.MediaCount, Creator: e.Creator, Publisher: e.Publisher,
	}
}

// InstallFile records a newly verified file as the active version of its key
// and marks any previous active version superseded, in one transaction. Rows
// of other files at the same path are dropped: the bytes there are now f's.
func (s *Store) InstallFile(ctx context.Context, f File) error {
	return s.tx(ctx, func(q *gen.Queries) error {
		if err := q.DropFilesAt(ctx, gen.DropFilesAtParams{Path: f.Path, UUID: f.UUID}); err != nil {
			return err
		}
		if err := q.SupersedeKey(ctx, gen.SupersedeKeyParams{Superseded: StateSuperseded, Active: StateActive, Name: f.Name, Flavour: f.Flavour, UUID: f.UUID}); err != nil {
			return err
		}
		ts := now()
		return q.PutFile(ctx, gen.PutFileParams{UUID: f.UUID, ZimUUID: f.ZimUUID, Name: f.Name, Flavour: f.Flavour,
			Path: f.Path, Size: f.Size, SHA256: f.SHA256, Date: f.Date, Title: f.Title, Summary: f.Summary,
			Language: f.Language, Category: f.Category, Tags: f.Tags, ArticleCount: f.ArticleCount,
			MediaCount: f.MediaCount, Creator: f.Creator, Publisher: f.Publisher, State: StateActive,
			AddedAt: ts, VerifiedAt: ts})
	})
}

// Files returns all files, newest first within each key.
func (s *Store) Files(ctx context.Context) ([]File, error) { return each(file)(s.q.Files(ctx)) }

// FileByUUID returns the file with this catalog UUID, or nil.
func (s *Store) FileByUUID(ctx context.Context, uuid string) (*File, error) {
	return one(each(file)(s.q.FileByUUID(ctx, uuid)))
}

// FileOfRelease returns a file of the release uuid (its catalog UUID or
// the one in its header), or nil.
func (s *Store) FileOfRelease(ctx context.Context, uuid string) (*File, error) {
	return one(each(file)(s.q.FileOfRelease(ctx, uuid)))
}

// FilesByKey returns the files for k, newest first.
func (s *Store) FilesByKey(ctx context.Context, k catalog.Key) ([]File, error) {
	return each(file)(s.q.FilesByKey(ctx, gen.FilesByKeyParams{Name: k.Name, Flavour: k.Flavour}))
}

// ActiveFiles returns the active file of every key.
func (s *Store) ActiveFiles(ctx context.Context) ([]File, error) {
	return each(file)(s.q.ActiveFiles(ctx, StateActive))
}

// ActiveFile returns the active file for k, or nil.
func (s *Store) ActiveFile(ctx context.Context, k catalog.Key) (*File, error) {
	return one(each(file)(s.q.ActiveFile(ctx, gen.ActiveFileParams{Name: k.Name, Flavour: k.Flavour, Active: StateActive})))
}

// SetFilePath records that a file was renamed.
func (s *Store) SetFilePath(ctx context.Context, uuid, path string) error {
	return s.q.SetFilePath(ctx, gen.SetFilePathParams{UUID: uuid, Path: path})
}

// DeleteFile forgets a file. It does not touch the disk.
func (s *Store) DeleteFile(ctx context.Context, uuid string) error { return s.q.DeleteFile(ctx, uuid) }

// MarkVerified records a successful re-hash.
func (s *Store) MarkVerified(ctx context.Context, uuid string) error {
	return s.q.MarkVerified(ctx, gen.MarkVerifiedParams{UUID: uuid, VerifiedAt: now()})
}

func file(r gen.File) File {
	return File{UUID: r.UUID, ZimUUID: r.ZimUUID, Name: r.Name, Flavour: r.Flavour, Path: r.Path, Size: r.Size,
		SHA256: r.SHA256, Date: r.Date, Title: r.Title, Summary: r.Summary, Language: r.Language,
		Category: r.Category, Tags: r.Tags, ArticleCount: r.ArticleCount, MediaCount: r.MediaCount,
		Creator: r.Creator, Publisher: r.Publisher, State: r.State}
}

// Download is an in-progress (or interrupted) download.
type Download struct {
	UUID          string
	Name          string
	Flavour       string
	Filename      string // the ZIM's file name
	Size          int64
	VerifiedBytes int64
	Error         string
	Pulled        bool   // from another zimon (zimon pull), not Kiwix
	HashState     []byte // only loaded by DownloadByUUID
	Meta4         []byte // only loaded by DownloadByUUID
}

// Key is the download's name and flavour.
func (d Download) Key() catalog.Key { return catalog.Key{Name: d.Name, Flavour: d.Flavour} }

// StartDownload records a new download, or returns the existing row for the same UUID.
func (s *Store) StartDownload(ctx context.Context, d Download) (*Download, error) {
	if err := s.q.StartDownload(ctx, gen.StartDownloadParams{UUID: d.UUID, Name: d.Name, Flavour: d.Flavour,
		Filename: d.Filename, Size: d.Size, Pulled: d.Pulled, Meta4: d.Meta4, Now: now()}); err != nil {
		return nil, err
	}
	return s.DownloadByUUID(ctx, d.UUID)
}

// Checkpoint records verified progress. Call only after the bytes are fsynced.
func (s *Store) Checkpoint(ctx context.Context, uuid string, verified int64, hashState []byte) error {
	return s.q.Checkpoint(ctx, gen.CheckpointParams{UUID: uuid, VerifiedBytes: verified, HashState: hashState, Now: now()})
}

// SetDownloadSource records where a download carries on from: another
// zimon (pulled), or Kiwix; and the release's .meta4, if known.
func (s *Store) SetDownloadSource(ctx context.Context, uuid string, pulled bool, meta4 []byte) error {
	return s.q.SetDownloadSource(ctx, gen.SetDownloadSourceParams{UUID: uuid, Pulled: pulled, Meta4: meta4, Now: now()})
}

// SetDownloadError records why a download stopped.
func (s *Store) SetDownloadError(ctx context.Context, uuid, errMsg string) error {
	return s.q.SetDownloadError(ctx, gen.SetDownloadErrorParams{UUID: uuid, Error: errMsg, Now: now()})
}

// DeleteDownload forgets a download. It does not touch the disk.
func (s *Store) DeleteDownload(ctx context.Context, uuid string) error {
	return s.q.DeleteDownload(ctx, uuid)
}

// DownloadByUUID returns the download with the given UUID, including its
// .meta4 and hash state, or nil.
func (s *Store) DownloadByUUID(ctx context.Context, uuid string) (*Download, error) {
	return one(each(func(r gen.Download) Download {
		return Download{UUID: r.UUID, Name: r.Name, Flavour: r.Flavour, Filename: r.Filename, Size: r.Size,
			VerifiedBytes: r.VerifiedBytes, Error: r.Error, Pulled: r.Pulled, HashState: r.HashState, Meta4: r.Meta4}
	})(s.q.DownloadByUUID(ctx, uuid)))
}

// Downloads returns every download, without the .meta4 and hash state.
func (s *Store) Downloads(ctx context.Context) ([]Download, error) {
	return each(func(r gen.DownloadsRow) Download {
		return Download{UUID: r.UUID, Name: r.Name, Flavour: r.Flavour, Filename: r.Filename, Size: r.Size,
			VerifiedBytes: r.VerifiedBytes, Error: r.Error, Pulled: r.Pulled}
	})(s.q.Downloads(ctx))
}
