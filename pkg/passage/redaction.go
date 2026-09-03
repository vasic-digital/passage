package passage

// The append-only redaction log.
//
// # Why a log and not a column
//
// `Record.Redacted` is a bool. A bool is a claim with no evidence behind it:
// it says a row is suppressed and says nothing about who decided that, when,
// under which reason, or whether the decision was ever reversed. Worse, a bool
// can be flipped back to false and leave no trace that it was ever true — the
// audit trail is destroyed by the same edit that undoes the suppression.
//
// So the bool is not the mechanism. It is the MATERIALISATION of the latest
// entry in this log, which is append-only: a correction is a new entry, never
// an edit of an old one, and an un-suppression is an explicit `unredact`
// entry rather than the deletion of the `redact` entry that preceded it.
//
// # The file
//
// One compact JSON object per line, LF-terminated, in append order. `seq`
// starts at 1 and increases by exactly 1. That is not decoration: a gap means
// a line was deleted and a repeat means a line was rewritten, and both are
// refusals on load rather than a best-effort read of a tampered audit trail.
//
// The derived database's `redactions` table is a CACHE of this file, exactly
// as the derived `passages` table is a cache of the registry file. If the two
// disagree, the file wins and the table is rebuilt.
//
// # What this package refuses
//
// Three refusals, each a DETERMINED problem rather than an inability to tell:
//
//	1. suppressing an identifier that is not in the registry — nothing is
//	   being suppressed, so the entry would be an assertion about content
//	   nobody can point at;
//	2. suppressing an already-suppressed identifier under a DIFFERENT reason
//	   code — the two reasons disagree about why, and picking one silently is
//	   the confident-but-wrong outcome;
//	3. un-suppressing an identifier that is not currently suppressed — there
//	   is nothing to reverse, and appending the entry anyway would put a lie
//	   in the audit trail.
//
// A re-request under the SAME reason code is a no-op, not a refusal: running
// the same suppression twice must be safe, or every operator learns to ignore
// the failure.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RedactionAction is what one log entry does. The vocabulary is closed and
// matches the derived schema's CHECK constraint.
type RedactionAction string

const (
	// ActionRedact suppresses an identifier.
	ActionRedact RedactionAction = "redact"
	// ActionUnredact reverses a suppression. It is an APPEND, never the
	// removal of the entry it reverses.
	ActionUnredact RedactionAction = "unredact"
)

// Valid reports whether a is one of the two defined actions.
func (a RedactionAction) Valid() bool {
	return a == ActionRedact || a == ActionUnredact
}

// Errors this file returns. Every one of them is a DETERMINED problem — the
// question was asked and the answer is "no". None of them means "could not
// tell"; that state is carried by an I/O error from the load path, which a
// caller must classify separately.
var (
	// ErrPIDNotInRegistry: the identifier is not a row in the registry.
	ErrPIDNotInRegistry = errors.New("passage: identifier is not in the registry")
	// ErrReasonConflict: already suppressed under a different reason code.
	ErrReasonConflict = errors.New("passage: already suppressed under a different reason code")
	// ErrNotSuppressed: asked to reverse a suppression that is not in effect.
	ErrNotSuppressed = errors.New("passage: not currently suppressed")
	// ErrLogTampered: the log's sequence is not 1..N ascending by one, which
	// means a line was deleted, reordered or rewritten.
	ErrLogTampered = errors.New("passage: append-only log has been rewritten")
)

// RedactionEntry is one immutable line of the log.
//
// Field order is the serialisation order and is fixed, so an appended line is
// byte-stable and a diff of the file shows exactly one added line.
type RedactionEntry struct {
	Seq        int             `json:"seq"`
	PID        PID             `json:"pid"`
	Action     RedactionAction `json:"action"`
	ReasonCode string          `json:"reason_code"`
	By         string          `json:"by"`
	At         string          `json:"at"`
}

// Validate enforces the field invariants of one entry.
func (e RedactionEntry) Validate() error {
	if e.Seq < 1 {
		return fmt.Errorf("passage: redaction entry seq %d is not positive", e.Seq)
	}
	if !e.PID.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidPID, e.PID)
	}
	if !e.Action.Valid() {
		return fmt.Errorf("passage: unknown redaction action %q (want %q or %q)",
			e.Action, ActionRedact, ActionUnredact)
	}
	if strings.TrimSpace(e.ReasonCode) == "" {
		return errors.New("passage: redaction entry carries no reason_code; an unexplained " +
			"suppression cannot be reviewed, so it is refused rather than stored")
	}
	if strings.TrimSpace(e.By) == "" {
		return errors.New("passage: redaction entry carries no `by`; an unattributed " +
			"suppression has no accountable author")
	}
	if strings.TrimSpace(e.At) == "" {
		return errors.New("passage: redaction entry carries no timestamp")
	}
	if _, err := time.Parse(time.RFC3339, e.At); err != nil {
		return fmt.Errorf("passage: redaction entry timestamp %q is not RFC3339: %w", e.At, err)
	}
	return nil
}

// RedactionLog is the append-only log, in file order.
//
// It is a value, not a handle: loading it reads the whole file, and appending
// writes exactly one line to the end. Nothing here ever truncates, rewrites or
// reorders the file, and there is deliberately no API that could.
type RedactionLog struct {
	entries []RedactionEntry
}

// NewRedactionLog returns an empty log.
func NewRedactionLog() *RedactionLog { return &RedactionLog{} }

// Len is the number of entries.
func (l *RedactionLog) Len() int {
	if l == nil {
		return 0
	}
	return len(l.entries)
}

// Entries returns a copy of the log in append order. The copy is deliberate:
// a caller must not be able to mutate the audit trail through the slice it was
// handed.
func (l *RedactionLog) Entries() []RedactionEntry {
	if l == nil {
		return nil
	}
	out := make([]RedactionEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// LoadRedactionLog reads a log file.
//
// A file that does not exist yields an EMPTY log and no error. That is a
// DETERMINED state and not an inability to tell: no suppression has ever been
// appended. Any other read failure is returned, and a caller must classify it
// as could-not-determine rather than as "nothing is suppressed".
func LoadRedactionLog(path string) (*RedactionLog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewRedactionLog(), nil
		}
		return nil, err
	}
	log := NewRedactionLog()
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		var e RedactionEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w; a key this package does not define is refused "+
				"rather than dropped — an audit trail that silently loses a field is not one",
				path, i+1, err)
		}
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		want := len(log.entries) + 1
		if e.Seq != want {
			return nil, fmt.Errorf("%s:%d: %w: seq %d where %d was required; the log is "+
				"append-only, so a gap means a line was deleted and a repeat means a line "+
				"was rewritten", path, i+1, ErrLogTampered, e.Seq, want)
		}
		log.entries = append(log.entries, e)
	}
	return log, nil
}

// State returns the latest entry for every identifier the log mentions,
// keyed by identifier. An identifier whose latest entry is an unredact is
// present in the map with that entry — the caller reads Action, so "was
// suppressed and then released" is distinguishable from "never mentioned".
func (l *RedactionLog) State() map[PID]RedactionEntry {
	out := map[PID]RedactionEntry{}
	if l == nil {
		return out
	}
	for _, e := range l.entries {
		out[e.PID] = e
	}
	return out
}

// Suppressed reports whether the log's latest entry for p suppresses it.
func (l *RedactionLog) Suppressed(p PID) bool {
	if l == nil {
		return false
	}
	e, ok := l.State()[p]
	return ok && e.Action == ActionRedact
}

// SuppressedPIDs is every identifier currently suppressed, sorted ascending so
// iteration and reports are deterministic.
func (l *RedactionLog) SuppressedPIDs() []PID {
	var out []PID
	for p, e := range l.State() {
		if e.Action == ActionRedact {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Append validates a new entry against the registry and the log's current
// state and, if it is both legal and not already in effect, adds it.
//
// It returns the entry and whether it was actually appended. `false, nil`
// means the request was already satisfied — the same identifier, the same
// action, the same reason code — which is a success, not a failure. Running
// the same suppression twice must be safe.
//
// The registry is REQUIRED. Appending an entry for an identifier nobody can
// resolve would put an assertion about non-existent content into an audit
// trail, so this refuses instead.
func (l *RedactionLog) Append(reg *Registry, p PID, action RedactionAction,
	reasonCode, by string, at time.Time) (RedactionEntry, bool, error) {

	if l == nil {
		return RedactionEntry{}, false, errors.New("passage: nil redaction log")
	}
	if reg == nil {
		return RedactionEntry{}, false, errors.New("passage: no registry to validate against")
	}
	if reg.unavailable != nil {
		return RedactionEntry{}, false, fmt.Errorf("passage: registry unavailable: %w",
			reg.unavailable)
	}
	if _, ok := reg.Lookup(p); !ok {
		return RedactionEntry{}, false, fmt.Errorf("%w: %s", ErrPIDNotInRegistry, p)
	}

	e := RedactionEntry{
		Seq:        len(l.entries) + 1,
		PID:        p,
		Action:     action,
		ReasonCode: reasonCode,
		By:         by,
		At:         at.UTC().Format(time.RFC3339),
	}
	if err := e.Validate(); err != nil {
		return RedactionEntry{}, false, err
	}

	prev, seen := l.State()[p]
	switch action {
	case ActionRedact:
		if seen && prev.Action == ActionRedact {
			if prev.ReasonCode != reasonCode {
				return RedactionEntry{}, false, fmt.Errorf(
					"%w: %s is suppressed under %q and was requested under %q; the two "+
						"reasons disagree and this package will not pick one",
					ErrReasonConflict, p, prev.ReasonCode, reasonCode)
			}
			return prev, false, nil // already in effect under the same reason
		}
	case ActionUnredact:
		if !seen || prev.Action != ActionRedact {
			return RedactionEntry{}, false, fmt.Errorf(
				"%w: %s; there is nothing to reverse, and appending the entry anyway "+
					"would put a false statement in the audit trail", ErrNotSuppressed, p)
		}
	}

	l.entries = append(l.entries, e)
	return e, true, nil
}

// AppendFile writes ONE entry to the end of path and never touches anything
// already in the file.
//
// The file is opened O_APPEND|O_CREATE|O_WRONLY. There is no truncating open
// anywhere in this package, which is what makes "append-only" a property of
// the code rather than a promise in a comment.
func AppendFile(path string, e RedactionEntry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Materialise applies the log's latest state to the registry's Redacted
// field and reports which identifiers changed, sorted ascending.
//
// This is the ONE direction the two artifacts may be reconciled in. The log is
// the decision and the column is its shadow; nothing reads the column to
// decide what the log should say.
//
// An identifier in the log that is absent from the registry is a DETERMINED
// problem, not a row to skip: the log asserts something about content the
// registry does not carry, and the two must be reconciled by a human.
func (l *RedactionLog) Materialise(reg *Registry) ([]PID, error) {
	if l == nil {
		return nil, errors.New("passage: nil redaction log")
	}
	if reg == nil || reg.unavailable != nil {
		return nil, errors.New("passage: registry unavailable")
	}
	state := l.State()
	pids := make([]PID, 0, len(state))
	for p := range state {
		pids = append(pids, p)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })

	var changed []PID
	for _, p := range pids {
		rec, ok := reg.Lookup(p)
		if !ok {
			return changed, fmt.Errorf("%w: %s is in the log at seq %d but not in the registry",
				ErrPIDNotInRegistry, p, state[p].Seq)
		}
		want := state[p].Action == ActionRedact
		if rec.Redacted == want {
			continue
		}
		rec.Redacted = want
		if err := reg.putForce(rec); err != nil {
			return changed, err
		}
		changed = append(changed, p)
	}
	return changed, nil
}

// Purge blanks Text and MachineText on every currently suppressed row IN THE
// REGISTRY ITSELF, and reports which rows changed.
//
// # This is irreversible, and the tension is real rather than glossed
//
// R6 says un-suppression is an explicit, reviewable append; that guarantee is
// only meaningful while the text still exists to be released. F6 says a
// suppressed row carries no text at any layer. A registry file held in a
// private store can honour both — the row keeps its text, and every
// serialisation that LEAVES the registry drops it, which is what Public()
// already does and what BuildDB already writes.
//
// A caller who needs the text gone from the registry file too — because the
// file itself is about to cross a boundary — calls this, and gives up R6 for
// those rows. The log still shows what was suppressed, by whom and why; what
// it can no longer do is put the text back. Nothing here decides that
// trade-off; it is exposed as a separate call precisely so that a caller has
// to make it deliberately.
func (l *RedactionLog) Purge(reg *Registry) ([]PID, error) {
	if l == nil {
		return nil, errors.New("passage: nil redaction log")
	}
	if reg == nil || reg.unavailable != nil {
		return nil, errors.New("passage: registry unavailable")
	}
	var changed []PID
	for _, p := range l.SuppressedPIDs() {
		rec, ok := reg.Lookup(p)
		if !ok {
			return changed, fmt.Errorf("%w: %s", ErrPIDNotInRegistry, p)
		}
		if rec.Text == "" && rec.MachineText == "" {
			continue
		}
		rec.Text = ""
		rec.MachineText = ""
		rec.Redacted = true
		if err := reg.putForce(rec); err != nil {
			return changed, err
		}
		changed = append(changed, p)
	}
	return changed, nil
}

// SyncDB rebuilds the derived `redactions` table from this log.
//
// The table is a CACHE of the log file, exactly as the derived `passages`
// table is a cache of the registry file, so it is rebuilt wholesale rather
// than appended to — running this twice is safe and produces the same table.
// The FILE is the append-only artifact; this is its index.
//
// Call it AFTER Registry.BuildDB: the table declares a foreign key onto
// passages(pid), and rebuilding passages wholesale is what makes that
// reference resolvable.
func (l *RedactionLog) SyncDB(ctx context.Context, path string) error {
	if l == nil {
		return errors.New("passage: nil redaction log")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureSchema(ctx, db, path); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM redactions`); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO redactions (seq,pid,action,reason_code,by,at) VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, e := range l.entries {
		if _, err := ins.ExecContext(ctx, e.Seq, string(e.PID), string(e.Action),
			e.ReasonCode, e.By, e.At); err != nil {
			return fmt.Errorf("passage: writing redaction seq %d: %w", e.Seq, err)
		}
	}
	return tx.Commit()
}
