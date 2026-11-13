package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
)

// tokenPrefix marks zimon's tokens, so one pasted in the wrong place is
// recognisable.
const tokenPrefix = "zimon_"

// NewToken makes an API token and the hash to store: the token itself is
// shown once and never kept.
func NewToken() (token, hash string) {
	token = tokenPrefix + randomID()
	return token, hashToken(token)
}

// randomID is 32 random bytes, URL-safe.
func randomID() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // never fails (crypto/rand)
	return base64.RawURLEncoding.EncodeToString(b)
}

// hashToken is how a token is stored and looked up.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// bearer is the token in an "Authorization: Bearer …" header, or "".
func bearer(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
