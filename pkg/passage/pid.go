// Package passage is the identity layer for citable spans of text.
//
// # The one rule
//
// A pid is a ULID MINTED ONCE at ingest. It is not derived from content and it
// is not derived from position. Nothing else is ever a citable identity.
//
// This is not a style preference. Both alternatives were MEASURED, and both
// fail, in opposite directions:
//
//	candidate                      measured behaviour
//	-----------------------------  ------------------------------------------
//	content-derived chunk id       fixing ONE typo, line numbers unchanged,
//	                               changed 4b98e295c112a429 -> 610e608a50f273a6
//	positional key (file + lines)  prepending a section shifted every later
//	                               range; all ids held, all ranges moved
//
// Under either, a corrected passage loses 100% of its citations WHILE THE UI
// KEEPS RENDERING LINKS that now point somewhere else — a failure with no
// visible symptom. A minted identifier is the only form that survives both
// operations, which is why it is the root of this package.
//
// Everything here exists to keep that true, and the two survival guarantees
// are proven by gates with paired mutations in pid_test.go:
//
//	G1  a text correction does not change the pid
//	G2  a line-position shift does not change the pid
//
// # Scope
//
// This package is about identity for spans of text in files. It knows nothing
// about chapters, transcripts, recordings, curricula or any other application
// concept — those belong to consumers, which map their own content onto
// Observation and read Record back. It is a separate Go module precisely so
// that boundary is enforced by the compiler rather than by review.
package passage

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"
)

// PID is a passage identifier: a 26-character Crockford base32 ULID, canonical
// UPPERCASE.
//
// Deliberately absent: any accessor for the embedded timestamp. The ULID
// timestamp component is the ingest wall clock and carries NO semantic
// meaning; sorting passages by pid, inferring recording order from a pid, or
// reading a date out of one is a defect. Offering a Time() method would invite
// exactly that, so none exists.
type PID string

// PIDLength is the exact character length of a pid.
const PIDLength = 26

// crockford is the Crockford base32 alphabet: I, L, O and U are excluded
// because they are the characters humans mis-transcribe.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// pidPattern is the contract's validation regex, verbatim. The leading digit
// is capped at 7 because 26 base32 characters carry 130 bits and a ULID is
// 128, so the first character encodes only two significant bits.
var pidPattern = regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

// ErrInvalidPID is returned for anything that is not a canonical pid.
var ErrInvalidPID = errors.New("passage: not a valid pid")

// ValidPID reports whether s is a canonical pid.
func ValidPID(s string) bool { return pidPattern.MatchString(s) }

// ParsePID validates and canonicalises. Lowercase input is accepted at every
// boundary and canonicalised to uppercase (contract §2.1); surrounding
// whitespace is trimmed. NOTHING else is normalised — in particular no
// "similar-looking" character is folded, because two pids are equal iff their
// canonical forms are byte-equal.
func ParsePID(s string) (PID, error) {
	up := strings.ToUpper(strings.TrimSpace(s))
	if !ValidPID(up) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPID, s)
	}
	return PID(up), nil
}

// String returns the pid as a bare string. Never wrapped, never prefixed,
// never namespaced.
func (p PID) String() string { return string(p) }

// Valid reports whether p is canonical.
func (p PID) Valid() bool { return ValidPID(string(p)) }

// Minter mints pids. It is the ONLY thing that may create one: not the API,
// not the indexer, not the cross-referencer, not the frontend (M2).
//
// Both of its inputs are seams so tests are reproducible and so a caller can
// supply a clock in an environment where time.Now is not what it seems. There
// is no frozen host assumption here: the defaults are time.Now and
// crypto/rand.Reader, and either can be replaced.
type Minter struct {
	mu      sync.Mutex
	clock   func() time.Time
	entropy io.Reader

	lastMS  int64
	lastEnt [10]byte
	started bool
}

// MinterOption configures a Minter.
type MinterOption func(*Minter)

// WithClock replaces the wall clock.
func WithClock(fn func() time.Time) MinterOption {
	return func(m *Minter) { m.clock = fn }
}

// WithEntropy replaces the randomness source.
func WithEntropy(r io.Reader) MinterOption {
	return func(m *Minter) { m.entropy = r }
}

// NewMinter returns a Minter using time.Now and crypto/rand by default.
func NewMinter(opts ...MinterOption) *Minter {
	m := &Minter{clock: time.Now, entropy: rand.Reader}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Mint produces a new pid.
//
// Monotonicity within a single process is guaranteed — including when the
// clock stalls (minting 1,400 passages takes far less than a millisecond
// each) and when it goes backwards (NTP step, suspend/resume, container clock
// skew). That monotonicity is an implementation convenience for stable diffs,
// NOT a contract: nothing may infer ordering from a pid.
func (m *Minter) Mint() (PID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ms := m.clock().UnixMilli()
	if ms < 0 {
		return "", errors.New("passage: clock is before the unix epoch")
	}
	if ms > 1<<48-1 {
		return "", errors.New("passage: timestamp exceeds the 48 bits a ULID carries")
	}

	var ent [10]byte
	switch {
	case m.started && ms <= m.lastMS:
		// Same millisecond, or a clock that moved backwards. Keep the last
		// timestamp and increment the entropy so the pid still increases.
		ms = m.lastMS
		ent = m.lastEnt
		if !incrementEntropy(&ent) {
			return "", errors.New("passage: entropy exhausted within one millisecond")
		}
	default:
		if _, err := io.ReadFull(m.entropy, ent[:]); err != nil {
			return "", fmt.Errorf("passage: reading entropy: %w", err)
		}
	}

	var raw [16]byte
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	copy(raw[6:], ent[:])

	m.lastMS, m.lastEnt, m.started = ms, ent, true
	return PID(encodeULID(raw)), nil
}

// incrementEntropy adds one with carry, returning false on overflow.
func incrementEntropy(e *[10]byte) bool {
	for i := len(e) - 1; i >= 0; i-- {
		e[i]++
		if e[i] != 0 {
			return true
		}
	}
	return false
}

// encodeULID renders 16 bytes as 26 Crockford base32 characters, most
// significant first.
func encodeULID(b [16]byte) string {
	var d [PIDLength]byte
	d[0] = crockford[(b[0]&224)>>5]
	d[1] = crockford[b[0]&31]
	d[2] = crockford[(b[1]&248)>>3]
	d[3] = crockford[((b[1]&7)<<2)|((b[2]&192)>>6)]
	d[4] = crockford[(b[2]&62)>>1]
	d[5] = crockford[((b[2]&1)<<4)|((b[3]&240)>>4)]
	d[6] = crockford[((b[3]&15)<<1)|((b[4]&128)>>7)]
	d[7] = crockford[(b[4]&124)>>2]
	d[8] = crockford[((b[4]&3)<<3)|((b[5]&224)>>5)]
	d[9] = crockford[b[5]&31]
	d[10] = crockford[(b[6]&248)>>3]
	d[11] = crockford[((b[6]&7)<<2)|((b[7]&192)>>6)]
	d[12] = crockford[(b[7]&62)>>1]
	d[13] = crockford[((b[7]&1)<<4)|((b[8]&240)>>4)]
	d[14] = crockford[((b[8]&15)<<1)|((b[9]&128)>>7)]
	d[15] = crockford[(b[9]&124)>>2]
	d[16] = crockford[((b[9]&3)<<3)|((b[10]&224)>>5)]
	d[17] = crockford[b[10]&31]
	d[18] = crockford[(b[11]&248)>>3]
	d[19] = crockford[((b[11]&7)<<2)|((b[12]&192)>>6)]
	d[20] = crockford[(b[12]&62)>>1]
	d[21] = crockford[((b[12]&1)<<4)|((b[13]&240)>>4)]
	d[22] = crockford[((b[13]&15)<<1)|((b[14]&128)>>7)]
	d[23] = crockford[(b[14]&124)>>2]
	d[24] = crockford[((b[14]&3)<<3)|((b[15]&224)>>5)]
	d[25] = crockford[b[15]&31]
	return string(d[:])
}
