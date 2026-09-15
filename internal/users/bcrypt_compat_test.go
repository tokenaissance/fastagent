package users

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// These hashes were produced by golang.org/x/crypto v0.46.0 — the version this
// module shipped before the 2026-09 bump to v0.55.0 (the bump that closed the
// seven critical x/crypto advisories). They are the shape of every row already
// sitting in users.password_hash, which makes them the real acceptance test for
// the upgrade: if the bump stopped verifying these, every existing account
// would be locked out at once.
var preBumpHashes = []struct {
	name     string
	password string
	hash     string
}{
	{
		name:     "ascii",
		password: "hunter2",
		hash:     "$2a$10$h4PmfgY4yCQkMmvxq5.ARuGiZ3TlYb871sCGPKxCxwbR7EIBlY/Pi",
	},
	{
		name:     "cjk multibyte",
		password: "密码-带中文-2026",
		hash:     "$2a$10$U9GNs6MzroytSr7qNuaROu98cFt5LLxdpQwsDrQolILseSa.qc9oK",
	},
	{
		name:     "66 bytes of punctuation",
		password: "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ+-*/",
		hash:     "$2a$10$TwCxXL5HVu9GyyxdORgXO.Ooa.UnqTIZUGrSpWNmUXTpq7YvC2jzm",
	},
	{
		name:     "exactly 72 bytes",
		password: strings.Repeat("q", 72),
		hash:     "$2a$10$8Q5OrFhzd1Sv/S6bQrqfY.IgeIIYc2A8DgYDVYdLIpJe5apaXHQ4W",
	},
}

func TestPreBumpPasswordHashesStillVerify(t *testing.T) {
	for _, tc := range preBumpHashes {
		t.Run(tc.name, func(t *testing.T) {
			if err := bcrypt.CompareHashAndPassword([]byte(tc.hash), []byte(tc.password)); err != nil {
				t.Fatalf("hash written by x/crypto v0.46.0 no longer verifies: %v", err)
			}
			// The wrong password has to differ *inside* the first 72 bytes:
			// appending to a 72-byte password would still verify, because
			// bcrypt reads only the first 72 (see the boundary test below).
			wrong := "z" + tc.password[1:]
			if err := bcrypt.CompareHashAndPassword([]byte(tc.hash), []byte(wrong)); err == nil {
				t.Fatal("a wrong password verified against a pre-bump hash")
			}
		})
	}
}

// TestPasswordHashShapeIsUnchanged pins the on-disk format, so that a hash
// written by the bumped binary is still readable by the pre-bump one: rollback
// has to stay safe, not just the upgrade.
func TestPasswordHashShapeIsUnchanged(t *testing.T) {
	if bcrypt.DefaultCost != 10 {
		t.Fatalf("bcrypt.DefaultCost moved: got %d, want 10 — hashes written before the bump used cost 10", bcrypt.DefaultCost)
	}
	h, err := bcrypt.GenerateFromPassword([]byte("hunter2"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}
	if len(h) != 60 {
		t.Fatalf("hash length %d, want 60", len(h))
	}
	if prefix := string(h[:7]); prefix != "$2a$10$" {
		t.Fatalf("hash prefix %q, want %q", prefix, "$2a$10$")
	}
}

// TestBcryptLengthBoundaryAt72Bytes documents how the library treats long
// passwords, because the product inherits it verbatim: Create and SetPassword
// call GenerateFromPassword, login calls CompareHashAndPassword. Building the
// same probe against v0.46.0 and v0.55.0 produced identical results on every
// cell here, so what this test protects is drift in a *later* bump — a change
// in either direction would silently lock out (or silently admit) real users.
func TestBcryptLengthBoundaryAt72Bytes(t *testing.T) {
	// 72 bytes is inside the limit, and the limit counts bytes rather than
	// runes: 24 CJK characters are exactly 72 bytes.
	for _, pw := range []string{strings.Repeat("q", 72), strings.Repeat("密", 24)} {
		if len(pw) != 72 {
			t.Fatalf("fixture is %d bytes, want 72", len(pw))
		}
		h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
		if err != nil {
			t.Fatalf("72-byte password rejected: %v", err)
		}
		if err := bcrypt.CompareHashAndPassword(h, []byte(pw)); err != nil {
			t.Fatalf("72-byte password did not verify: %v", err)
		}
	}

	// 73 bytes: GenerateFromPassword refuses outright, which is why no account
	// can be created with an over-limit password (the API returns an error
	// instead of silently truncating).
	long := strings.Repeat("q", 73)
	if _, err := bcrypt.GenerateFromPassword([]byte(long), bcrypt.DefaultCost); !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("73-byte password: got %v, want ErrPasswordTooLong", err)
	}

	// Verify-side is the opposite: the over-limit tail is ignored rather than
	// rejected, so a submitted password authenticates on its first 72 bytes.
	h72, err := bcrypt.GenerateFromPassword([]byte(strings.Repeat("q", 72)), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword(h72, []byte(long)); err != nil {
		t.Fatalf("bcrypt no longer ignores the tail past 72 bytes on verify: %v", err)
	}

	// The same 72-byte ceiling applies to multibyte input: 25 CJK characters
	// are 75 bytes and are refused.
	if _, err := bcrypt.GenerateFromPassword([]byte(strings.Repeat("密", 25)), bcrypt.DefaultCost); !errors.Is(err, bcrypt.ErrPasswordTooLong) {
		t.Fatalf("75-byte CJK password: got %v, want ErrPasswordTooLong", err)
	}
}
