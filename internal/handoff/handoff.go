// Package handoff carries a signed-in LuCI user to the console without a
// second login. LuCI's page asks the router (ubus luci.netryx handoff, with
// the caller's rpcd session put in by uhttpd itself) for a code; the browser
// brings the code to the console, which redeems it once.
//
// A code is 128 random bits, good for 30 seconds and for one redeem, bound to
// the rpcd session and the user it was minted for. Only its SHA-256 is kept,
// as the name of a private file in RAM that holds the session and the user,
// so nothing on disk or in a log can be replayed. The console still asks rpcd
// whether that session is alive and may view before it opens anything.
package handoff

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"netryx/internal/ramfile"
)

const (
	// TTL is how long a code is good for after it is minted.
	TTL = 30 * time.Second
	// MaxPending bounds the codes minted and not yet redeemed or expired.
	MaxPending = 32
	// Dir is where the pending codes are kept: netryx's own RAM directory.
	Dir = "/tmp/netryx/luci"
)

var (
	// ErrNoCode answers a code that is malformed, unknown, used or expired:
	// one answer for all, so a guess learns nothing.
	ErrNoCode = errors.New("no such code")
	// ErrFull answers a mint while MaxPending codes are pending.
	ErrFull = errors.New("too many pending codes")
	// ErrBad answers a mint for something that is not a session and a user.
	ErrBad = errors.New("not an rpcd session and a user")
)

// Claim is what a redeemed code was minted for.
type Claim struct{ SID, User string }

type pending struct {
	SID  string `json:"sid"`
	User string `json:"user"`
	At   int64  `json:"at"` // unix nanoseconds, when it was minted
	// Nonce is the SHA-256 of the browser's nonce: the code opens only with it.
	Nonce string `json:"nonce"`
}

func hex32(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidUser reports a LuCI user name netryx accepts: 1 to 64 bytes of UTF-8
// with no control character.
func ValidUser(u string) bool {
	if u == "" || len(u) > 64 || !utf8.ValidString(u) {
		return false
	}
	return !strings.ContainsFunc(u, unicode.IsControl)
}

func nameOf(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// live reports whether a code minted at at is good at now: not before it was
// minted (a clock set back cannot stretch it) and less than TTL after.
func live(at, now time.Time) bool {
	d := now.Sub(at)
	return d >= 0 && d < TTL
}

// prune removes the expired pending codes and counts the rest.
func prune(dir string, now time.Time) int {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		// Another mint's file, still being written: not ours to judge yet.
		if strings.HasPrefix(e.Name(), ".") {
			if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) < TTL {
				continue
			}
		}
		b, _ := ramfile.Read(p, 1024)
		var pd pending
		if b != nil && json.Unmarshal(b, &pd) == nil && live(time.Unix(0, pd.At), now) {
			n++
			continue
		}
		// Expired, damaged, a leftover temporary or a claimed file: gone.
		_ = ramfile.Remove(p)
	}
	return n
}

// Mint returns a new code for the session and the user, bound to the nonce
// LuCI's page put in the asking browser's cookie.
func Mint(dir, sid, user, nonce string, now time.Time) (string, error) {
	if !hex32(sid) || !ValidUser(user) || !hex32(nonce) {
		return "", ErrBad
	}
	if prune(dir, now) >= MaxPending {
		return "", ErrFull
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b)
	body, err := json.Marshal(pending{SID: sid, User: user, At: now.UnixNano(), Nonce: nameOf(nonce)})
	if err != nil {
		return "", err
	}
	if err := ramfile.Write(filepath.Join(dir, nameOf(code)), body); err != nil {
		return "", err
	}
	return code, nil
}

// Redeem takes the code: once, only while it is good, and only from the
// browser holding the nonce it was minted for. A wrong nonce spends it too.
func Redeem(dir, code, nonce string, now time.Time) (Claim, error) {
	if !hex32(code) {
		return Claim{}, ErrNoCode
	}
	p := filepath.Join(dir, nameOf(code))
	taken := p + ".taken"
	// The rename is the redeem: of two at once, one wins and the other finds
	// nothing, and the code is gone before anything else is read.
	if err := os.Rename(p, taken); err != nil {
		return Claim{}, ErrNoCode
	}
	b, _ := ramfile.Read(taken, 1024)
	_ = ramfile.Remove(taken)
	var pd pending
	if b == nil || json.Unmarshal(b, &pd) != nil || !hex32(pd.SID) || !ValidUser(pd.User) || !live(time.Unix(0, pd.At), now) {
		return Claim{}, ErrNoCode
	}
	if !hex32(nonce) || subtle.ConstantTimeCompare([]byte(nameOf(nonce)), []byte(pd.Nonce)) != 1 {
		return Claim{}, ErrNoCode
	}
	return Claim{SID: pd.SID, User: pd.User}, nil
}
