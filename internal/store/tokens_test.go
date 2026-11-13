package store

import (
	"context"
	"errors"
	"testing"
)

func TestTokens(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.AddToken(ctx, "laptop", "aaa"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddToken(ctx, "laptop", "bbb"); !errors.Is(err, ErrTokenExists) {
		t.Errorf("same name twice: %v", err)
	}
	if err := s.AddToken(ctx, "phone", "bbb"); err != nil {
		t.Fatal(err)
	}
	if tok, err := s.UseToken(ctx, "nope"); tok != nil || err != nil {
		t.Errorf("unknown token: %+v, %v", tok, err)
	}
	tok, err := s.UseToken(ctx, "aaa")
	if err != nil || tok == nil || tok.Name != "laptop" || tok.LastUsed.IsZero() || tok.Created.IsZero() {
		t.Fatalf("use: %+v, %v", tok, err)
	}
	toks, err := s.Tokens(ctx)
	if err != nil || len(toks) != 2 || toks[0].Name != "laptop" || toks[1].Name != "phone" || !toks[1].LastUsed.IsZero() {
		t.Errorf("tokens: %+v, %v", toks, err)
	}
	if ok, err := s.RemoveToken(ctx, "laptop"); !ok || err != nil {
		t.Errorf("remove: %v, %v", ok, err)
	}
	if ok, _ := s.RemoveToken(ctx, "laptop"); ok {
		t.Error("removed a token twice")
	}
	if tok, _ := s.UseToken(ctx, "aaa"); tok != nil {
		t.Error("a removed token still works")
	}
}
