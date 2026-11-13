package store

import (
	"context"
	"errors"
	"time"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/kazie/zimon/internal/store/gen"
)

// Token is an API token, known by name. Only its sha-256 is stored.
type Token struct {
	Name     string
	Created  time.Time
	LastUsed time.Time // zero if never used
}

// ErrTokenExists means a token with that name already exists.
var ErrTokenExists = errors.New("a token with that name already exists")

// AddToken stores a token's hash under name.
func (s *Store) AddToken(ctx context.Context, name, sha256 string) error {
	err := s.q.AddToken(ctx, gen.AddTokenParams{Name: name, SHA256: sha256, Now: now()})
	if isCode(err, sqlite3.SQLITE_CONSTRAINT) { // the name; the sha-256 of 32 random bytes is new
		return ErrTokenExists
	}
	return err
}

// Tokens lists the tokens by name.
func (s *Store) Tokens(ctx context.Context) ([]Token, error) {
	return each(token)(s.q.Tokens(ctx))
}

// RemoveToken deletes a token; it reports whether there was one.
func (s *Store) RemoveToken(ctx context.Context, name string) (bool, error) {
	n, err := s.q.RemoveToken(ctx, name)
	return n > 0, err
}

// usedEvery is how often a token's last use is written: not on every
// request, so reading the API doesn't write to the database.
const usedEvery = time.Minute

// UseToken finds the token with this hash and records that it was used
// now (at most once a minute). It returns nil when there is none.
func (s *Store) UseToken(ctx context.Context, sha256 string) (*Token, error) {
	t, err := s.TokenByHash(ctx, sha256)
	if err != nil || t == nil || time.Since(t.LastUsed) < usedEvery {
		return t, err
	}
	t.LastUsed = time.Now().UTC().Truncate(time.Second)
	return t, s.q.TokenUsed(ctx, gen.TokenUsedParams{LastUsedAt: t.LastUsed.Format(time.RFC3339), SHA256: sha256})
}

// TokenByHash finds the token with this hash, or nil.
func (s *Store) TokenByHash(ctx context.Context, sha256 string) (*Token, error) {
	return one(each(token)(s.q.TokenByHash(ctx, sha256)))
}

func token(r gen.Token) Token {
	return Token{Name: r.Name, Created: parseTime(r.CreatedAt), LastUsed: parseTime(r.LastUsedAt)}
}
