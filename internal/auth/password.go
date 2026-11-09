package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters: RFC 9106's second recommended option (64 MiB, 3 passes), 4 lanes. A login costs about 50-100
// ms of CPU, which together with the throttle makes guessing slow.
type argonParams struct {
	memory  uint32 // KiB
	time    uint32
	threads uint8
	keyLen  uint32
}

var defaultArgon = argonParams{memory: 64 * 1024, time: 3, threads: 4, keyLen: 32}

// testArgon is used by tests (SetFastHashing) so the suite stays quick.
var testArgon = argonParams{memory: 64, time: 1, threads: 1, keyLen: 32}

var activeArgon = defaultArgon

// SetFastHashing switches new password hashes to tiny argon2id parameters. Tests only: hashes made this way are
// still verified correctly later, they are just weak.
func SetFastHashing() { activeArgon = testArgon }

// MinPasswordLen is the shortest password reset-password accepts.
const MinPasswordLen = 12

var b64 = base64.RawStdEncoding

// HashPassword returns a PHC-format argon2id hash: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	p := activeArgon
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memory, p.time, p.threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

var errBadHash = errors.New("auth: malformed password hash")

// VerifyPassword reports whether password matches a hash from HashPassword, in constant time.
func VerifyPassword(hash, password string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errBadHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return false, errBadHash
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 || p.memory > 1<<21 || p.time > 64 {
		return false, errBadHash
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// RandomPassword returns a random password of 24 URL-safe characters (144 bits).
func RandomPassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
