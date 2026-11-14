package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kazie/zimon/internal/store/gen"
)

// Session is a sign-in to the admin page. Sessions are kept in kv (as
// "session:" and the hash of the cookie, never the cookie itself), so a
// restart or an update doesn't sign anyone out; kv, not a table of their
// own, so a released database needs no new schema.
type Session struct {
	Name      string `json:"name"`            // the token's name, or "local" for a one-time link
	TokenHash string `json:"token,omitempty"` // the token's: removing the token ends the session
	Expires   int64  `json:"expires"`         // Unix seconds; DeleteExpiredSessions reads it in SQL
}

const sessionKey = "session:"

// PutSession keeps a session under the hash of its cookie, and forgets the
// sessions that have run out.
func (s *Store) PutSession(ctx context.Context, idHash string, ss Session) error {
	b, err := json.Marshal(ss)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(q *gen.Queries) error {
		if err := deleteExpiredSessions(ctx, q); err != nil {
			return err
		}
		return q.SetKV(ctx, gen.SetKVParams{Key: sessionKey + idHash, Value: string(b)})
	})
}

// Session returns the session kept under idHash, or nil when there is none,
// it has expired, or its token has been removed (zimon token remove).
func (s *Store) Session(ctx context.Context, idHash string) (*Session, error) {
	v, err := s.Get(ctx, sessionKey+idHash)
	if err != nil || v == "" {
		return nil, err
	}
	var ss Session
	if err := json.Unmarshal([]byte(v), &ss); err != nil {
		return nil, err
	}
	if time.Now().Unix() > ss.Expires {
		return nil, nil
	}
	if ss.TokenHash != "" {
		if tok, err := s.TokenByHash(ctx, ss.TokenHash); err != nil || tok == nil {
			return nil, err
		}
	}
	return &ss, nil
}

// DeleteSession ends the session kept under idHash.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	return s.q.DeleteKV(ctx, sessionKey+idHash)
}

func deleteExpiredSessions(ctx context.Context, q *gen.Queries) error {
	return q.DeleteExpiredSessions(ctx, gen.DeleteExpiredSessionsParams{Pattern: sessionKey + "*", Now: time.Now().Unix()})
}
