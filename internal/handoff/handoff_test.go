package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 22, 21, 0, 0, 0, time.UTC)

const sid = "0123456789abcdef0123456789abcdef"

func dir(t *testing.T) string { return filepath.Join(t.TempDir(), "luci") }

func TestACodeIsGoodOnce(t *testing.T) {
	d := dir(t)
	code, err := Mint(d, sid, "root", t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 32 {
		t.Fatalf("code %d characters, want 32 hex (128 bits)", len(code))
	}
	c, err := Redeem(d, code, t0.Add(time.Second))
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if c.SID != sid || c.User != "root" {
		t.Fatalf("claim %+v, want the session and the user it was minted for", c)
	}
	if _, err := Redeem(d, code, t0.Add(2*time.Second)); !errors.Is(err, ErrNoCode) {
		t.Fatalf("second redeem: %v, want ErrNoCode", err)
	}
}

func TestEveryCodeIsItsOwn(t *testing.T) {
	d := dir(t)
	a, _ := Mint(d, sid, "root", t0)
	b, _ := Mint(d, sid, "root", t0)
	if a == b {
		t.Fatal("two mints gave the same code")
	}
	if _, err := Redeem(d, a, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := Redeem(d, b, t0); err != nil {
		t.Fatalf("redeeming one code used up the other: %v", err)
	}
}

func TestACodeExpiresAfterThirtySeconds(t *testing.T) {
	d := dir(t)
	late, _ := Mint(d, sid, "root", t0)
	if _, err := Redeem(d, late, t0.Add(TTL)); !errors.Is(err, ErrNoCode) {
		t.Fatalf("redeemed at the TTL: %v, want ErrNoCode", err)
	}
	inTime, _ := Mint(d, sid, "root", t0)
	if _, err := Redeem(d, inTime, t0.Add(TTL-time.Second)); err != nil {
		t.Fatalf("redeemed a second before the TTL: %v", err)
	}
	if TTL != 30*time.Second {
		t.Fatalf("TTL %v, want 30s", TTL)
	}
}

// A clock set back between mint and redeem cannot stretch a code's life.
func TestACodeFromTheFutureIsNoCode(t *testing.T) {
	d := dir(t)
	code, _ := Mint(d, sid, "root", t0.Add(time.Hour))
	if _, err := Redeem(d, code, t0); !errors.Is(err, ErrNoCode) {
		t.Fatalf("a code minted an hour ahead of the clock: %v, want ErrNoCode", err)
	}
}

func TestAMalformedCodeIsNoCode(t *testing.T) {
	d := dir(t)
	code, _ := Mint(d, sid, "root", t0)
	for _, bad := range []string{"", strings.ToUpper(code), code[:31], code + "0", "../" + code[3:], strings.Repeat("g", 32)} {
		if _, err := Redeem(d, bad, t0); !errors.Is(err, ErrNoCode) {
			t.Errorf("%q: %v, want ErrNoCode", bad, err)
		}
	}
	if _, err := Redeem(d, code, t0); err != nil {
		t.Fatalf("the good code after the bad ones: %v", err)
	}
}

func TestTheCodeIsNeverWrittenDown(t *testing.T) {
	d := dir(t)
	code, _ := Mint(d, sid, "root", t0)
	ents, err := os.ReadDir(d)
	if err != nil || len(ents) != 1 {
		t.Fatalf("%d files (%v), want one pending code", len(ents), err)
	}
	b, _ := os.ReadFile(filepath.Join(d, ents[0].Name()))
	if strings.Contains(ents[0].Name(), code) || strings.Contains(string(b), code) {
		t.Fatal("the code itself is on disk; only its hash may be")
	}
	fi, _ := os.Stat(filepath.Join(d, ents[0].Name()))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("pending code file mode %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(d); di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v, want 0700", di.Mode().Perm())
	}
}

func TestPendingCodesAreBounded(t *testing.T) {
	d := dir(t)
	for i := 0; i < MaxPending; i++ {
		if _, err := Mint(d, sid, "root", t0); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, err := Mint(d, sid, "root", t0); !errors.Is(err, ErrFull) {
		t.Fatalf("mint past %d: %v, want ErrFull", MaxPending, err)
	}
	// Expired codes make room, and are taken away.
	if _, err := Mint(d, sid, "root", t0.Add(TTL)); err != nil {
		t.Fatalf("mint once the others expired: %v", err)
	}
	if ents, _ := os.ReadDir(d); len(ents) != 1 {
		t.Fatalf("%d files after the expired ones were pruned, want 1", len(ents))
	}
}

func TestAMintRefusesWhatIsNotASessionOrAUser(t *testing.T) {
	d := dir(t)
	for _, c := range []struct{ sid, user string }{
		{"", "root"},
		{"0123", "root"},
		{strings.ToUpper(sid), "root"},
		{sid, ""},
		{sid, strings.Repeat("u", 65)},
		{sid, "ro\not"},
		{sid, "\xff"},
	} {
		if _, err := Mint(d, c.sid, c.user, t0); err == nil {
			t.Errorf("minted for %q %q", c.sid, c.user)
		}
	}
	if ents, _ := os.ReadDir(d); len(ents) != 0 {
		t.Fatalf("%d files written for refused mints", len(ents))
	}
}

// A pending file anyone else could have written or read is no code.
func TestAPendingFileOthersCouldTouchIsNoCode(t *testing.T) {
	d := dir(t)
	code, _ := Mint(d, sid, "root", t0)
	ents, _ := os.ReadDir(d)
	if err := os.Chmod(filepath.Join(d, ents[0].Name()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Redeem(d, code, t0); !errors.Is(err, ErrNoCode) {
		t.Fatalf("a world-readable pending file: %v, want ErrNoCode", err)
	}
}
