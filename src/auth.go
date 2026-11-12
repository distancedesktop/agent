package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// minTokenLen rejects tokens short enough to brute-force over the network.
const minTokenLen = 16

// loadOrCreateToken returns the viewer token stored at path, generating and
// saving a random one (mode 0600) when the file does not exist yet.
func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(b))
		if len(tok) < minTokenLen {
			return "", fmt.Errorf("token file %s: token must be at least %d characters", path, minTokenLen)
		}
		if runtime.GOOS != "windows" {
			fi, err := os.Stat(path)
			if err != nil {
				return "", err
			}
			if fi.Mode().Perm()&0o077 != 0 {
				return "", fmt.Errorf("token file %s: mode %04o is readable by other users; chmod 600 it", path, fi.Mode().Perm())
			}
		}
		return tok, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		return "", err
	}
	return tok, f.Close()
}

// requestToken extracts the viewer token from a WebTransport upgrade request.
// Browsers cannot set headers on a WebTransport CONNECT, so the token travels
// as the `token` query parameter; non-browser clients may instead send
// `Authorization: Bearer <token>`.
func requestToken(r *http.Request) string {
	if t := r.URL.Query().Get("token"); t != "" {
		return t
	}
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authorized reports whether r carries the expected viewer token.
func authorized(r *http.Request, want string) bool {
	got := requestToken(r)
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
