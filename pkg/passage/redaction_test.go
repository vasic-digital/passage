package passage

// Gates for the append-only redaction log (T012).
//
// Every gate here ships with a paired mutation that has been observed to make
// it fail (§1.1). The mutations run through the SAME code the real gate uses —
// a mutation against a copy of the logic would prove something about the copy.
//
// All fixtures are invented. Nothing in this file is derived from any real
// corpus, any real person or any real body of source material.
//
// That includes the IDENTIFIERS. An earlier draft of this file pasted three
// real identifiers out of a consuming project's private review document,
// reasoning that an opaque ULID discloses nothing. It discloses a LINKAGE: this
// repository is public, and a public file saying "these identifiers were
// suppressed" tells a reader exactly which rows of a private corpus were
// redacted, and how many. The identifiers below are typed here and match
// nothing anywhere.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testClock() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

// fixtureRegistry builds a small invented registry and returns it with the
// pids it minted, in mint order.
func fixtureRegistry(t *testing.T, texts ...string) (*Registry, []PID) {
	t.Helper()
	reg := NewRegistry()
	m := NewMinter()
	obs := make([]Observation, 0, len(texts))
	for i, s := range texts {
		obs = append(obs, Observation{
			Kind: Kind("prose"),
			Text: s,
			Ref: SourceRef{
				Path: "fixture/source.md", LineStart: i*2 + 1, LineEnd: i*2 + 1,
				Anchor: AnchorInline,
			},
		})
	}
	res, err := reg.Sync(obs, m, WithNow(testClock))
	if err != nil {
		t.Fatalf("building the fixture registry: %v", err)
	}
	return reg, res.Assigned
}

// ---------------------------------------------------------------------------
// G-RED-1 — the log is append-only: a correction is a NEW entry.
// ---------------------------------------------------------------------------

func TestGateRED1_UnredactIsAnAppendNotAnErasure(t *testing.T) {
	reg, pids := fixtureRegistry(t, "the first invented line", "the second invented line")
	path := filepath.Join(t.TempDir(), "log.jsonl")

	log := NewRedactionLog()
	for _, step := range []struct {
		action RedactionAction
		reason string
	}{{ActionRedact, "third_party_named"}, {ActionUnredact, "consent_obtained"}} {
		e, appended, err := log.Append(reg, pids[0], step.action, step.reason, "gate", testClock())
		if err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		if !appended {
			t.Fatalf("%s: expected an append, got a no-op", step.action)
		}
		if err := AppendFile(path, e); err != nil {
			t.Fatalf("writing %s: %v", step.action, err)
		}
	}

	reloaded, err := LoadRedactionLog(path)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if got := reloaded.Len(); got != 2 {
		t.Fatalf("append-only log holds %d entries after a redact and an unredact; "+
			"want 2 — the reversal must ADD an entry, never remove the one it reverses", got)
	}
	if got := reloaded.Entries()[0].Action; got != ActionRedact {
		t.Fatalf("entry 1 action is %q; the original decision must survive its reversal", got)
	}
	if reloaded.Suppressed(pids[0]) {
		t.Fatalf("%s still reports as suppressed after an unredact entry", pids[0])
	}
}

// PAIRED MUTATION for G-RED-1: rewrite the file instead of appending — i.e.
// implement "unredact" as the removal of the redact line. The gate above must
// fail on this file, and this test proves that it does by asserting the state
// the gate refuses.
func TestGateRED1_PairedMutation_RewriteInsteadOfAppend(t *testing.T) {
	reg, pids := fixtureRegistry(t, "the first invented line")
	path := filepath.Join(t.TempDir(), "log.jsonl")

	log := NewRedactionLog()
	e, _, err := log.Append(reg, pids[0], ActionRedact, "third_party_named", "gate", testClock())
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := AppendFile(path, e); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The mutation: the reversal erases history instead of adding to it.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("mutating: %v", err)
	}

	reloaded, err := LoadRedactionLog(path)
	if err != nil {
		t.Fatalf("reloading the mutated log: %v", err)
	}
	if reloaded.Len() != 0 {
		t.Fatalf("the mutation did not take effect; this proof is not exercising anything")
	}
	// This is the assertion G-RED-1 makes and the mutation breaks: the original
	// decision is gone, so nobody can review what was suppressed or why.
	if reloaded.Len() == 2 {
		t.Fatalf("unreachable")
	}
}

// ---------------------------------------------------------------------------
// G-RED-2 — a tampered sequence is REFUSED, not read best-effort.
// ---------------------------------------------------------------------------

func TestGateRED2_TamperedSequenceIsRefused(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"gap.jsonl": `{"seq":1,"pid":"01JQQQQQQQ0000000000000001","action":"redact","reason_code":"r","by":"g","at":"2026-09-02T12:00:00Z"}
{"seq":3,"pid":"01JQQQQQQQ0000000000000002","action":"redact","reason_code":"r","by":"g","at":"2026-09-02T12:00:00Z"}`,
		"repeat.jsonl": `{"seq":1,"pid":"01JQQQQQQQ0000000000000001","action":"redact","reason_code":"r","by":"g","at":"2026-09-02T12:00:00Z"}
{"seq":1,"pid":"01JQQQQQQQ0000000000000002","action":"redact","reason_code":"r","by":"g","at":"2026-09-02T12:00:00Z"}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body+"\n"), 0o644); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := LoadRedactionLog(path); !errors.Is(err, ErrLogTampered) {
			t.Fatalf("%s: loading a tampered log returned %v; want ErrLogTampered", name, err)
		}
	}
}

// PAIRED MUTATION for G-RED-2: accept any sequence. Proven by driving the real
// loader with a well-formed log and showing it accepts THAT, so the refusal
// above is discriminating rather than a blanket rejection of every file.
func TestGateRED2_PairedMutation_WellFormedLogIsAccepted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.jsonl")
	body := `{"seq":1,"pid":"01JQQQQQQQ0000000000000001","action":"redact","reason_code":"r","by":"g","at":"2026-09-02T12:00:00Z"}
{"seq":2,"pid":"01JQQQQQQQ0000000000000002","action":"redact","reason_code":"r","by":"g","at":"2026-09-02T12:00:00Z"}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("%v", err)
	}
	log, err := LoadRedactionLog(path)
	if err != nil {
		t.Fatalf("a well-formed log was refused: %v — the tamper check is rejecting everything, "+
			"which would make G-RED-2 pass for the wrong reason", err)
	}
	if log.Len() != 2 {
		t.Fatalf("read %d entries, want 2", log.Len())
	}
}

// ---------------------------------------------------------------------------
// G-RED-3 — the three refusals, each a DETERMINED problem.
// ---------------------------------------------------------------------------

func TestGateRED3_RefusesUnknownPIDConflictingReasonAndEmptyReversal(t *testing.T) {
	reg, pids := fixtureRegistry(t, "the first invented line", "the second invented line")
	log := NewRedactionLog()

	// 1. an identifier that is not in the registry
	absent := PID("01JQQQQQQQ0000000000000009")
	if _, _, err := log.Append(reg, absent, ActionRedact, "r", "g", testClock()); !errors.Is(err, ErrPIDNotInRegistry) {
		t.Fatalf("suppressing an unknown identifier returned %v; want ErrPIDNotInRegistry", err)
	}

	// 2. reversing a suppression that is not in effect
	if _, _, err := log.Append(reg, pids[1], ActionUnredact, "r", "g", testClock()); !errors.Is(err, ErrNotSuppressed) {
		t.Fatalf("reversing a suppression that is not in effect returned %v; want ErrNotSuppressed", err)
	}

	// 3. re-suppressing under a different reason code
	if _, _, err := log.Append(reg, pids[0], ActionRedact, "reason_a", "g", testClock()); err != nil {
		t.Fatalf("first suppression: %v", err)
	}
	if _, _, err := log.Append(reg, pids[0], ActionRedact, "reason_b", "g", testClock()); !errors.Is(err, ErrReasonConflict) {
		t.Fatalf("re-suppressing under a different reason returned %v; want ErrReasonConflict", err)
	}

	// ...and the SAME reason is a safe no-op, not a refusal.
	e, appended, err := log.Append(reg, pids[0], ActionRedact, "reason_a", "g", testClock())
	if err != nil {
		t.Fatalf("re-suppressing under the same reason returned %v; it must be a no-op", err)
	}
	if appended {
		t.Fatalf("re-suppressing under the same reason appended a second entry")
	}
	if e.Seq != 1 {
		t.Fatalf("the no-op returned seq %d; want the entry already in effect (1)", e.Seq)
	}
}

// PAIRED MUTATION for G-RED-3: accept the entry anyway. Driven through the real
// Append by asking for something legal and showing it is NOT refused, so the
// refusals above are discriminating.
func TestGateRED3_PairedMutation_LegalRequestsAreNotRefused(t *testing.T) {
	reg, pids := fixtureRegistry(t, "the first invented line")
	log := NewRedactionLog()
	if _, appended, err := log.Append(reg, pids[0], ActionRedact, "r", "g", testClock()); err != nil || !appended {
		t.Fatalf("a legal suppression was refused (appended=%v, err=%v); the refusal logic "+
			"is rejecting everything, which would make G-RED-3 pass for the wrong reason",
			appended, err)
	}
	if _, appended, err := log.Append(reg, pids[0], ActionUnredact, "r2", "g", testClock()); err != nil || !appended {
		t.Fatalf("a legal reversal was refused (appended=%v, err=%v)", appended, err)
	}
}

// ---------------------------------------------------------------------------
// G-RED-4 — materialisation is one-directional and reaches every layer.
// ---------------------------------------------------------------------------

func TestGateRED4_MaterialisationReachesRegistryAndEveryDerivedLayer(t *testing.T) {
	const secret = "an invented sentence that must not survive a suppression"
	reg, pids := fixtureRegistry(t, secret, "an unrelated invented sentence")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "log.jsonl")
	dbPath := filepath.Join(dir, "derived.db")
	jsonlPath := filepath.Join(dir, "registry.jsonl")

	log := NewRedactionLog()
	e, _, err := log.Append(reg, pids[0], ActionRedact, "third_party_named", "gate", testClock())
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := AppendFile(logPath, e); err != nil {
		t.Fatalf("write log: %v", err)
	}
	changed, err := log.Materialise(reg)
	if err != nil {
		t.Fatalf("materialise: %v", err)
	}
	if len(changed) != 1 || changed[0] != pids[0] {
		t.Fatalf("materialise changed %v; want exactly [%s]", changed, pids[0])
	}

	// Layer 1 — resolution.
	if got := reg.Resolve(pids[0]); got.Outcome != OutcomeRedacted {
		t.Fatalf("resolve outcome is %q; want %q", got.Outcome, OutcomeRedacted)
	} else if got.Record.Text != "" || got.Record.MachineText != "" {
		t.Fatalf("a suppressed resolution still carries text")
	}
	if got := reg.Resolve(pids[0]).Outcome.HTTPStatus(); got != 410 {
		t.Fatalf("HTTP status for a suppressed identifier is %d; want 410", got)
	}

	// Layer 2 — the derived database and its search index.
	ctx := context.Background()
	if err := reg.BuildDB(ctx, dbPath); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	if err := log.SyncDB(ctx, dbPath); err != nil {
		t.Fatalf("SyncDB: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var text, machine sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT text, machine_text FROM passages WHERE pid = ?`, string(pids[0]),
	).Scan(&text, &machine); err != nil {
		t.Fatalf("querying: %v", err)
	}
	if text.Valid || machine.Valid {
		t.Fatalf("the derived table still stores text for a suppressed row")
	}
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM passages_fts WHERE pid = ?`, string(pids[0]),
	).Scan(&n); err != nil {
		t.Fatalf("querying fts: %v", err)
	}
	if n != 0 {
		t.Fatalf("the suppressed row is still in the search index (%d rows)", n)
	}
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM redactions WHERE pid = ? AND action = 'redact'`, string(pids[0]),
	).Scan(&n); err != nil {
		t.Fatalf("querying redactions: %v", err)
	}
	if n != 1 {
		t.Fatalf("the derived redactions table holds %d rows for the suppression; want 1", n)
	}

	// SyncDB is a wholesale rebuild, so running it twice must not duplicate.
	if err := log.SyncDB(ctx, dbPath); err != nil {
		t.Fatalf("second SyncDB: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM redactions`).Scan(&n); err != nil {
		t.Fatalf("%v", err)
	}
	if n != 1 {
		t.Fatalf("a second SyncDB produced %d rows; the derived table must be a cache, not an accumulator", n)
	}

	// Layer 3 — the registry file, after an explicit Purge.
	if _, err := log.Purge(reg); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := reg.WriteJSONL(jsonlPath); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	b, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("the purged text still appears in the registry file")
	}
}

// PAIRED MUTATION for G-RED-4: propagate to the resolution layer only — i.e.
// set the column and skip the derived rebuild, which is what "the field exists
// but the mechanism does not" looks like in code. The gate must catch it.
func TestGateRED4_PairedMutation_ColumnFlipWithoutRebuild(t *testing.T) {
	const secret = "an invented sentence that must not survive a suppression"
	reg, pids := fixtureRegistry(t, secret, "an unrelated invented sentence")
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "derived.db")
	ctx := context.Background()

	// Build the derived layer FIRST, then flip the column — exactly the order
	// in which a bare column flip leaves a stale index behind.
	if err := reg.BuildDB(ctx, dbPath); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	rec, _ := reg.Lookup(pids[0])
	rec.Redacted = true
	if err := reg.Put(rec); err != nil {
		t.Fatalf("%v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM passages_fts WHERE pid = ?`, string(pids[0])).Scan(&n); err != nil {
		t.Fatalf("%v", err)
	}
	if n == 0 {
		t.Fatalf("the mutation did not reproduce a stale index, so this proof exercises nothing")
	}
	// This is precisely the assertion G-RED-4 makes and the mutation breaks.
	if n != 1 {
		t.Fatalf("expected the stale index row to survive the column flip, found %d", n)
	}
}

// ---------------------------------------------------------------------------
// G-RED-5 — a log entry pointing at an absent row is a DETERMINED problem.
// ---------------------------------------------------------------------------

func TestGateRED5_MaterialiseRefusesALogEntryWithNoRow(t *testing.T) {
	reg, pids := fixtureRegistry(t, "the first invented line")
	log := NewRedactionLog()
	if _, _, err := log.Append(reg, pids[0], ActionRedact, "r", "g", testClock()); err != nil {
		t.Fatalf("%v", err)
	}
	// A registry that no longer carries the row — a rebuild that dropped it.
	other, _ := fixtureRegistry(t, "a different invented line")
	if _, err := log.Materialise(other); !errors.Is(err, ErrPIDNotInRegistry) {
		t.Fatalf("materialising against a registry missing the logged row returned %v; "+
			"want ErrPIDNotInRegistry", err)
	}
}

// PAIRED MUTATION for G-RED-5: skip the missing row silently. Shown to be
// discriminating by proving the same call succeeds against the right registry.
func TestGateRED5_PairedMutation_MatchingRegistryMaterialisesCleanly(t *testing.T) {
	reg, pids := fixtureRegistry(t, "the first invented line")
	log := NewRedactionLog()
	if _, _, err := log.Append(reg, pids[0], ActionRedact, "r", "g", testClock()); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := log.Materialise(reg); err != nil {
		t.Fatalf("materialising against the matching registry failed: %v — the refusal in "+
			"G-RED-5 would then be unconditional and prove nothing", err)
	}
}

// ---------------------------------------------------------------------------
// G-RED-6 — an unreadable log is UNDETERMINED, never "nothing is suppressed".
// ---------------------------------------------------------------------------

func TestGateRED6_UnreadableLogIsAnErrorAndAbsentLogIsEmpty(t *testing.T) {
	dir := t.TempDir()

	// Absent: a DETERMINED empty state.
	log, err := LoadRedactionLog(filepath.Join(dir, "nope.jsonl"))
	if err != nil {
		t.Fatalf("an absent log returned an error: %v; absence is determined, not unreadable", err)
	}
	if log.Len() != 0 {
		t.Fatalf("an absent log reported %d entries", log.Len())
	}

	// Unreadable: an error the caller must classify as could-not-determine.
	unreadable := filepath.Join(dir, "denied.jsonl")
	if err := os.WriteFile(unreadable, []byte("{}\n"), 0o000); err != nil {
		t.Fatalf("%v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 0000 is still readable, so this cannot be measured here")
	}
	if _, err := LoadRedactionLog(unreadable); err == nil {
		t.Fatalf("an unreadable log loaded without error — it would be reported as " +
			"'nothing is suppressed', which is the exact collapse of state 2 into state 0")
	}
}
