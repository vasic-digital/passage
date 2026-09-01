package passage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ===========================================================================
// The pid itself.
// ===========================================================================

func TestMintedPIDMatchesTheContractFormat(t *testing.T) {
	m := newTestMinter()
	seen := map[PID]bool{}
	for i := 0; i < 2000; i++ {
		p, err := m.Mint()
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		if len(p) != PIDLength {
			t.Fatalf("mint %d: length %d, want %d (%q)", i, len(p), PIDLength, p)
		}
		if !ValidPID(string(p)) {
			t.Fatalf("mint %d: %q fails the contract regex ^[0-7][0-9A-HJKMNP-TV-Z]{25}$", i, p)
		}
		if strings.ContainsAny(string(p), "ILOU") {
			t.Fatalf("mint %d: %q contains an excluded Crockford letter", i, p)
		}
		if seen[p] {
			t.Fatalf("mint %d: %q was minted twice", i, p)
		}
		seen[p] = true
	}
}

func TestParsePIDCanonicalisesCaseAndRejectsEverythingElse(t *testing.T) {
	const canonical = "01JBX7QK3M8V2ZC4YT5N6RWDPA"
	for _, in := range []string{canonical, strings.ToLower(canonical), " " + canonical + "\n"} {
		got, err := ParsePID(in)
		if err != nil {
			t.Fatalf("ParsePID(%q): %v", in, err)
		}
		if string(got) != canonical {
			t.Errorf("ParsePID(%q) = %q, want %q", in, got, canonical)
		}
	}
	bad := []string{
		"",
		"01JBX7QK3M8V2ZC4YT5N6RWDP",   // 25 chars
		"01JBX7QK3M8V2ZC4YT5N6RWDPAB", // 27 chars
		"81JBX7QK3M8V2ZC4YT5N6RWDPA",  // first char > 7: overflows 128 bits
		"01JBX7QK3M8V2ZC4YT5N6RWDPI",  // excluded letter I
		"01JBX7QK3M8V2ZC4YT5N6RWDPL",  // excluded letter L
		"01JBX7QK3M8V2ZC4YT5N6RWDPO",  // excluded letter O
		"01JBX7QK3M8V2ZC4YT5N6RWDPU",  // excluded letter U
		"pid:01JBX7QK3M8V2ZC4YT5N6RW", // prefixed / namespaced
		"4b98e295c112a429",            // a Lumen chunk id
	}
	for _, in := range bad {
		if _, err := ParsePID(in); err == nil {
			t.Errorf("ParsePID(%q) accepted an invalid pid", in)
		}
	}
}

// M5: the timestamp component carries no semantic meaning. The API must not
// offer a way to read one out, or callers will sort by it and infer order.
func TestPIDExposesNoTimeAccessor(t *testing.T) {
	var p any = PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")
	if _, ok := p.(interface{ Time() time.Time }); ok {
		t.Error("PID exposes Time() — M5 forbids reading a date out of a pid")
	}
	if _, ok := p.(interface{ Timestamp() int64 }); ok {
		t.Error("PID exposes Timestamp() — M5 forbids reading a date out of a pid")
	}
}

func TestMinterIsMonotonicWithinAProcessEvenWhenTheClockStalls(t *testing.T) {
	// A stalled clock is the realistic case: minting 1,400 passages in one
	// ingest takes far less than a millisecond per passage.
	m := NewMinter(WithClock(func() time.Time { return time.Unix(1756000000, 0).UTC() }),
		WithEntropy(newDeterministicEntropy(7)))
	var prev PID
	for i := 0; i < 500; i++ {
		p, err := m.Mint()
		if err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
		if prev != "" && p <= prev {
			t.Fatalf("mint %d: %q is not greater than %q under a stalled clock", i, p, prev)
		}
		prev = p
	}
}

func TestMinterDoesNotGoBackwardsWhenTheClockDoes(t *testing.T) {
	now := time.Unix(1756000000, 0).UTC()
	m := NewMinter(WithClock(func() time.Time { return now }), WithEntropy(newDeterministicEntropy(11)))
	first, err := m.Mint()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(-time.Hour) // NTP step, suspend/resume, container clock skew
	second, err := m.Mint()
	if err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("clock went backwards and so did the pid: %q then %q", first, second)
	}
}

// ===========================================================================
// T011 — THE TWO SURVIVAL GUARANTEES.
//
// These are the deliverable of Phase 2. Both alternatives to a minted
// identifier were MEASURED to fail (contract §1, D-SEARCH-1):
//
//   Lumen chunk id (content-derived)  a one-character typo fix changed
//                                     4b98e295c112a429 -> 610e608a50f273a6
//   positional key (file + lines)     prepending a section moved every later
//                                     range
//
// Under either, SC-016 scores 0% on corrected passages while the UI keeps
// rendering links that now point elsewhere — a failure with no visible symptom.
//
// Every guarantee below is checked by a function returning an error, so the
// SAME function can be re-run against a deliberately broken key strategy. That
// is the paired mutation proof (FR-032, SC-012, §1.1): a proof that runs zero
// mutations proves nothing, and three gates in this repository shipped exactly
// that.
//
// All of it runs through the REAL entry point, Registry.SyncFile, against a
// real file on a real filesystem. Nothing here greps for a string.
// ===========================================================================

const (
	// A one-character typo, corrected. Deliberately the same shape as the
	// measured Lumen experiment: text changes, line numbers do not.
	typoText      = "[00:08:39] the reciever must verify the hash before it trusts the part"
	correctedText = "[00:08:39] the receiver must verify the hash before it trusts the part"

	// fixtureKind is the kind these fixtures pass to SyncFile. It is a token
	// invented for the tests, which is the whole point: this package declares
	// no kinds, so a caller — including a test — has to supply its own.
	fixtureKind Kind = "prose_block"
)

func fixtureDoc() string {
	return strings.Join([]string{
		"[00:08:32] we always cut inside a measured silence, never mid-word",
		"",
		typoText,
		"",
		"[00:08:47] a passage identifier is minted once and never recycled",
		"",
	}, "\n")
}

// checkG1 proves: correcting a passage's text does not change its pid, and the
// citation to it keeps resolving to THAT passage — with its corrected text.
func checkG1(dir string, strategy KeyStrategy) error {
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte(fixtureDoc()), 0o644); err != nil {
		return err
	}
	reg := NewRegistry()
	m := newTestMinter()

	first, err := reg.SyncFile(path, fixtureKind, m, WithKeyStrategy(strategy))
	if err != nil {
		return fmt.Errorf("first sync: %w", err)
	}
	if len(first.Minted) != 3 {
		return fmt.Errorf("first sync minted %d pids, want 3", len(first.Minted))
	}

	// The citation. A citation is a pid and nothing else (contract §7.2 C1).
	cited, err := pidOfTextContaining(reg, "reciever")
	if err != nil {
		return err
	}
	before, _ := reg.Lookup(cited)
	beforeMachine := before.MachineText
	beforeHash := before.ContentHash
	beforeLine := before.SourceRef.LineStart
	sizeBefore := reg.Len()

	// A human fixes the typo in the source file. Line numbers do not move.
	if err := replaceInFile(path, typoText, correctedText); err != nil {
		return err
	}

	second, err := reg.SyncFile(path, fixtureKind, m, WithKeyStrategy(strategy))
	if err != nil {
		return fmt.Errorf("second sync: %w", err)
	}

	// --- the guarantee ----------------------------------------------------
	if n := len(second.Minted); n != 0 {
		return fmt.Errorf("G1 VIOLATED: a text correction minted %d new pid(s) (%v); "+
			"M4/M6 require zero — this is the measured Lumen failure", n, second.Minted)
	}
	if reg.Len() != sizeBefore {
		return fmt.Errorf("G1 VIOLATED: registry grew from %d to %d rows on a correction",
			sizeBefore, reg.Len())
	}
	res := reg.Resolve(cited)
	if res.Outcome != OutcomeFound {
		return fmt.Errorf("G1 VIOLATED: the citation resolved %s after correction, want found", res.Outcome)
	}
	if res.Record.Text != correctedText {
		return fmt.Errorf("G1 VIOLATED: the citation still resolves, but to STALE text %q "+
			"instead of the corrected %q — the link renders fine and points at the wrong words",
			res.Record.Text, correctedText)
	}
	if res.Record.MachineText != beforeMachine {
		return fmt.Errorf("F1 VIOLATED: machine_text mutated from %q to %q; "+
			"the machine layer is evidence and must survive correction",
			beforeMachine, res.Record.MachineText)
	}
	if res.Record.Provenance != ProvenanceHumanCorrected {
		return fmt.Errorf("F2 VIOLATED: provenance is %q after a correction, want %q",
			res.Record.Provenance, ProvenanceHumanCorrected)
	}
	if res.Record.ContentHash == beforeHash {
		return fmt.Errorf("content_hash did not change on a text change (%s) — "+
			"change detection is its one job", beforeHash)
	}
	if res.Record.SourceRef.LineStart != beforeLine {
		return fmt.Errorf("line number moved on a same-length correction: %d -> %d",
			beforeLine, res.Record.SourceRef.LineStart)
	}
	return refsPointAtTheirText(reg, dir)
}

// checkG2 proves: inserting content ABOVE a passage does not change its pid.
// Reproduces the measured D-SEARCH-1 run 3 — prepend a section, everything
// below it moves.
func checkG2(dir string, strategy KeyStrategy) error {
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte(fixtureDoc()), 0o644); err != nil {
		return err
	}
	reg := NewRegistry()
	m := newTestMinter()

	if _, err := reg.SyncFile(path, fixtureKind, m, WithKeyStrategy(strategy)); err != nil {
		return fmt.Errorf("first sync: %w", err)
	}

	// Snapshot every pid and where it sits.
	type place struct {
		line int
		text string
	}
	before := map[PID]place{}
	for _, rec := range reg.All() {
		before[rec.PID] = place{rec.SourceRef.LineStart, rec.Text}
	}
	if len(before) != 3 {
		return fmt.Errorf("expected 3 passages before the shift, got %d", len(before))
	}

	// Prepend a section. Four lines, so everything below moves.
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	preface := "[00:00:02] preface, first line\n" +
		"[00:00:03] preface, second line\n" +
		"[00:00:04] preface, third line\n" +
		"\n"
	if err := os.WriteFile(path, append([]byte(preface), old...), 0o644); err != nil {
		return err
	}

	second, err := reg.SyncFile(path, fixtureKind, m, WithKeyStrategy(strategy))
	if err != nil {
		return fmt.Errorf("second sync: %w", err)
	}

	// --- the guarantee ----------------------------------------------------
	if n := len(second.Minted); n != 1 {
		return fmt.Errorf("G2 VIOLATED: a line shift minted %d pid(s), want exactly 1 "+
			"(the prepended section itself) — this is the measured positional-key failure", n)
	}
	if reg.Len() != 4 {
		return fmt.Errorf("G2 VIOLATED: registry holds %d rows after the shift, want 4", reg.Len())
	}
	var delta int
	for pid, was := range before {
		res := reg.Resolve(pid)
		if res.Outcome != OutcomeFound {
			return fmt.Errorf("G2 VIOLATED: pid %s resolved %s after a line shift", pid, res.Outcome)
		}
		if res.Record.Text != was.text {
			return fmt.Errorf("G2 VIOLATED: pid %s now carries different text after a pure "+
				"line shift:\n  was %q\n  now %q", pid, was.text, res.Record.Text)
		}
		moved := res.Record.SourceRef.LineStart - was.line
		if moved <= 0 {
			return fmt.Errorf("G2 VIOLATED: pid %s did not move (line %d -> %d) although "+
				"content was prepended — source_ref is a cache and MUST be re-derived",
				pid, was.line, res.Record.SourceRef.LineStart)
		}
		if delta == 0 {
			delta = moved
		} else if moved != delta {
			return fmt.Errorf("G2 VIOLATED: passages moved by different amounts (%d vs %d) "+
				"under a uniform prepend", delta, moved)
		}
	}
	return refsPointAtTheirText(reg, dir)
}

// refsPointAtTheirText is the assertion that makes source_ref meaningful: for
// every row, the file on disk must actually contain that row's text at the
// recorded line, immediately below that row's anchor. A stale ref that still
// "resolves" is precisely the silent failure this contract exists to prevent.
func refsPointAtTheirText(reg *Registry, dir string) error {
	cache := map[string][]string{}
	for _, rec := range reg.All() {
		abs := filepath.Join(dir, filepath.Base(rec.SourceRef.Path))
		lines, ok := cache[abs]
		if !ok {
			b, err := os.ReadFile(abs)
			if err != nil {
				return err
			}
			lines = strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
			cache[abs] = lines
		}
		i := rec.SourceRef.LineStart - 1
		if i < 0 || i >= len(lines) {
			return fmt.Errorf("pid %s: source_ref line %d is outside the file (%d lines)",
				rec.PID, rec.SourceRef.LineStart, len(lines))
		}
		wantFirst := strings.SplitN(rec.Text, "\n", 2)[0]
		if lines[i] != wantFirst {
			return fmt.Errorf("pid %s: source_ref points at line %d which reads %q, "+
				"but the row's text starts %q — the reference is stale and would render "+
				"as a working link to the wrong passage",
				rec.PID, rec.SourceRef.LineStart, lines[i], wantFirst)
		}
		if i == 0 {
			return fmt.Errorf("pid %s: no anchor line above its body", rec.PID)
		}
		got, ok := ParseAnchorLine(lines[i-1])
		if !ok || got != rec.PID {
			return fmt.Errorf("pid %s: the line above its body is %q, not its own anchor",
				rec.PID, lines[i-1])
		}
	}
	return nil
}

// --- the real gates --------------------------------------------------------

func TestG1_PIDSurvivesTextCorrection(t *testing.T) {
	if err := checkG1(t.TempDir(), AnchorKey()); err != nil {
		t.Fatalf("G1 failed under the real (anchor) key strategy: %v", err)
	}
	t.Log("G1 HOLDS: a one-character text correction left the pid, the citation and " +
		"machine_text intact, and changed only content_hash")
}

func TestG2_PIDSurvivesLinePositionShift(t *testing.T) {
	if err := checkG2(t.TempDir(), AnchorKey()); err != nil {
		t.Fatalf("G2 failed under the real (anchor) key strategy: %v", err)
	}
	t.Log("G2 HOLDS: prepending a section left every prior pid intact and re-derived " +
		"every source_ref to its new position")
}

// --- the paired mutations --------------------------------------------------
//
// Each mutant is injected into the REAL Registry.SyncFile through the REAL
// WithKeyStrategy option. Nothing is re-implemented for the proof.

// mutantContentHashKey reproduces the MEASURED Lumen behaviour: identity keyed
// on content, so a typo fix re-mints.
type mutantContentHashKey struct{}

func (mutantContentHashKey) Name() string { return "MUTANT:content_hash" }
func (mutantContentHashKey) keyStrategy() {}
func (mutantContentHashKey) Match(r *Registry, obs Observation) (PID, bool, error) {
	h := ContentHash(obs.Text)
	for _, rec := range r.All() {
		if rec.ContentHash == h {
			return rec.PID, true, nil
		}
	}
	return "", false, nil
}

// mutantPositionKey reproduces the MEASURED positional-key behaviour: identity
// keyed on (path, line), so anything inserted above re-mints.
type mutantPositionKey struct{}

func (mutantPositionKey) Name() string { return "MUTANT:position" }
func (mutantPositionKey) keyStrategy() {}
func (mutantPositionKey) Match(r *Registry, obs Observation) (PID, bool, error) {
	for _, rec := range r.All() {
		if rec.SourceRef.Path == obs.Ref.Path && rec.SourceRef.LineStart == obs.Ref.LineStart {
			return rec.PID, true, nil
		}
	}
	return "", false, nil
}

func TestG1_PairedMutation_ContentDerivedIdentityIsCaught(t *testing.T) {
	if err := checkG1(t.TempDir(), AnchorKey()); err != nil {
		t.Fatalf("CONTROL FAILED: G1 does not pass unmutated (%v), so the mutation below "+
			"would prove nothing", err)
	}
	err := checkG1(t.TempDir(), mutantContentHashKey{})
	if err == nil {
		t.Fatal("MUTATION SURVIVED: content-derived identity did not break G1. " +
			"The gate is inoperative — it would not catch the exact defect it exists for.")
	}
	t.Logf("mutation caught: %v", err)
}

func TestG2_PairedMutation_PositionalIdentityIsCaught(t *testing.T) {
	if err := checkG2(t.TempDir(), AnchorKey()); err != nil {
		t.Fatalf("CONTROL FAILED: G2 does not pass unmutated (%v), so the mutation below "+
			"would prove nothing", err)
	}
	err := checkG2(t.TempDir(), mutantPositionKey{})
	if err == nil {
		t.Fatal("MUTATION SURVIVED: positional identity did not break G2. " +
			"The gate is inoperative — it would not catch the exact defect it exists for.")
	}
	t.Logf("mutation caught: %v", err)
}

// The measured asymmetry (contract §1): each rejected candidate fails exactly
// ONE of the two operations. Asserting that each gate catches only its own
// mutant proves the two gates are independent and discriminating — a single
// gate that failed on everything would look just as green while testing less.
func TestMeasuredAsymmetry_EachCandidateFailsExactlyOneGuarantee(t *testing.T) {
	// Lumen-style content-derived ids survived LINE MOVEMENT in the measured
	// run ("all ids unchanged, all line ranges moved").
	if err := checkG2(t.TempDir(), mutantContentHashKey{}); err != nil {
		t.Errorf("content-derived identity was expected to SURVIVE a line shift "+
			"(the measured result), but G2 reported: %v", err)
	}
	// A positional key survived TEXT CORRECTION — the typo fix left the line
	// range alone.
	if err := checkG1(t.TempDir(), mutantPositionKey{}); err != nil {
		t.Errorf("positional identity was expected to SURVIVE a text correction "+
			"(the measured result), but G1 reported: %v", err)
	}
	t.Log("asymmetry confirmed: content-derived ids break on CORRECTION only; " +
		"positional keys break on MOVEMENT only; a minted pid survives both")
}

// ===========================================================================
// G-PID-3 — content_hash is change detection, never identity.
// ===========================================================================

func TestContentHashIsChangeDetectionNotIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte(fixtureDoc()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	m := newTestMinter()
	if _, err := reg.SyncFile(path, fixtureKind, m); err != nil {
		t.Fatal(err)
	}
	cited, err := pidOfTextContaining(reg, "reciever")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := reg.Lookup(cited)
	oldHash := rec.ContentHash

	if err := replaceInFile(path, typoText, correctedText); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SyncFile(path, fixtureKind, m); err != nil {
		t.Fatal(err)
	}

	// The real resolution path: by pid. Still works.
	if res := reg.Resolve(cited); res.Outcome != OutcomeFound || res.Record.Text != correctedText {
		t.Fatalf("pid resolution broke after a correction: %s / %q", res.Outcome, res.Record.Text)
	}
	// The forbidden path: by content_hash. A citation keyed on the hash
	// recorded when the citation was made now finds NOTHING — which is what
	// forbidden use 1 ("as a citation target") would have produced.
	if found := lookupByContentHash(reg, oldHash); found != "" {
		t.Fatalf("a stored content_hash still resolved to %s — the fixture did not "+
			"actually change the hash, so this test proves nothing", found)
	}
	t.Log("content_hash changed on correction (its job) while the pid did not (its job); " +
		"a citation keyed on content_hash would have dangled here")
}

func TestPairedMutation_ContentHashAsCitationKeyIsCaught(t *testing.T) {
	// CONTROL: the real citation key survives a correction.
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte(fixtureDoc()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	m := newTestMinter()
	if _, err := reg.SyncFile(path, fixtureKind, m); err != nil {
		t.Fatal(err)
	}
	cited, err := pidOfTextContaining(reg, "reciever")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := reg.Lookup(cited)
	citationByHash := rec.ContentHash // the MUTATION: cite by hash instead of pid

	if err := replaceInFile(path, typoText, correctedText); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.SyncFile(path, fixtureKind, m); err != nil {
		t.Fatal(err)
	}

	if res := reg.Resolve(cited); res.Outcome != OutcomeFound {
		t.Fatalf("CONTROL FAILED: the real pid-keyed citation broke (%s)", res.Outcome)
	}
	if found := lookupByContentHash(reg, citationByHash); found != "" {
		t.Fatal("MUTATION SURVIVED: a content_hash-keyed citation still resolved after a " +
			"correction, so this gate would not catch forbidden use 1")
	}
	t.Log("mutation caught: the content_hash-keyed citation dangled while the pid-keyed one held")
}

// ===========================================================================
// Idempotency (contract §8.1, FR-027).
// ===========================================================================

func TestSecondSyncMintsNothingAndLeavesTheFileByteIdentical(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte(fixtureDoc()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	m := newTestMinter()
	if _, err := reg.SyncFile(path, fixtureKind, m); err != nil {
		t.Fatal(err)
	}
	afterFirst, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	jsonlA := filepath.Join(dir, "a.jsonl")
	if err := reg.WriteJSONL(jsonlA); err != nil {
		t.Fatal(err)
	}
	firstJSONL, err := os.ReadFile(jsonlA)
	if err != nil {
		t.Fatal(err)
	}

	res, err := reg.SyncFile(path, fixtureKind, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Minted) != 0 { // I1
		t.Errorf("I1: second sync minted %d pids, want 0", len(res.Minted))
	}
	if len(res.Corrected) != 0 {
		t.Errorf("second sync reported %d corrections on unchanged input", len(res.Corrected))
	}
	afterSecond, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterFirst, afterSecond) { // I2 + I4
		t.Errorf("I4: the source file changed on a second sync:\n--- first ---\n%s\n--- second ---\n%s",
			afterFirst, afterSecond)
	}
	jsonlB := filepath.Join(dir, "b.jsonl")
	if err := reg.WriteJSONL(jsonlB); err != nil {
		t.Fatal(err)
	}
	secondJSONL, err := os.ReadFile(jsonlB)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSONL, secondJSONL) { // I3
		t.Error("I3: passages.jsonl is not byte-identical after a second sync")
	}
}

func TestPairedMutation_UnconditionalMintingBreaksIdempotency(t *testing.T) {
	// The mutation named by the contract: make R5 unconditional — mint
	// whenever no anchor was READ THIS RUN. mutantNeverMatch is exactly that.
	dir := t.TempDir()
	path := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(path, []byte(fixtureDoc()), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	m := newTestMinter()
	if _, err := reg.SyncFile(path, fixtureKind, m, WithKeyStrategy(mutantNeverMatch{})); err != nil {
		t.Fatal(err)
	}
	sizeAfterFirst := reg.Len()
	res, err := reg.SyncFile(path, fixtureKind, m, WithKeyStrategy(mutantNeverMatch{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Minted) == 0 && reg.Len() == sizeAfterFirst {
		t.Fatal("MUTATION SURVIVED: unconditional minting did not break idempotency")
	}
	t.Logf("mutation caught: second sync minted %d more pids (registry %d -> %d)",
		len(res.Minted), sizeAfterFirst, reg.Len())
}

type mutantNeverMatch struct{}

func (mutantNeverMatch) Name() string { return "MUTANT:always-mint" }
func (mutantNeverMatch) keyStrategy() {}
func (mutantNeverMatch) Match(*Registry, Observation) (PID, bool, error) {
	return "", false, nil
}

// ===========================================================================
// R1c — a foreign anchor is an error, never an invitation to mint.
// ===========================================================================

func TestForeignAnchorIsAnErrorNotAMint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pasted.md")
	// A block copy-pasted from somewhere else, carrying an anchor this
	// registry has never seen.
	doc := "<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->\nsome pasted paragraph\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	if _, err := reg.SyncFile(path, fixtureKind, newTestMinter()); err == nil {
		t.Fatal("a foreign anchor was silently adopted or re-minted; a copy-pasted block " +
			"must never hijack an identity (R1c)")
	}
	if reg.Len() != 0 {
		t.Errorf("registry grew to %d rows while rejecting a foreign anchor", reg.Len())
	}
}

// ===========================================================================
// The reusability claim, asserted mechanically.
// ===========================================================================

// TestNoConsumerShapedDependencies checks the DEPENDENCY MANIFEST and the
// package's PLACEMENT. Its name is exact and its corpus is narrow, and that
// narrowness is worth writing down, because for the module's first two
// releases this test WAS the whole of the reusability claim.
//
// What it reads is go.mod and the package's own directory. What it therefore
// proves is "this module does not IMPORT a consumer" and "this package is not
// walled inside internal/". Both were true and stayed true the entire time the
// package's own API carried a consumer's vocabulary in a struct field, a JSON
// tag, an enum and a SQL column.
//
// The dictionary was never the problem: the `forbidden` list below already
// contained the exact tokens that were sitting in registry.go. The CORPUS was
// the problem — the right words, applied to the wrong file. The reasoning in
// the go.mod comment made the same mistake in prose: "this module does not
// require any consumer, so no consumer-shaped symbol can appear here even by
// accident" is a non-sequitur. An absent dependency stops a consumer's code
// from arriving; nothing about it stops a consumer's NOUNS from being typed in
// by hand.
//
// The API, the struct tags and the schema are covered by the guard family in
// decoupling_test.go, which shares the dictionary below.
func TestNoConsumerShapedDependencies(t *testing.T) {
	root := moduleRootDir(t)
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	forbidden := append([]string{"backend"}, consumerVocabulary...)
	for i, line := range strings.Split(string(b), "\n") {
		code, _, _ := strings.Cut(line, "//")
		low := strings.ToLower(strings.TrimSpace(code))
		if low == "" {
			continue
		}
		for _, f := range forbidden {
			if strings.Contains(low, f) {
				t.Errorf("go.mod:%d depends on something consumer-shaped (%q contains %q). "+
					"This module must stay importable by any project.", i+1, code, f)
			}
		}
	}
	rel, err := filepath.Rel(root, mustGetwd(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "internal" {
			t.Fatalf("package sits under `internal/` (%s) — nothing outside this module "+
				"could ever import it", rel)
		}
	}
}

// ===========================================================================
// helpers
// ===========================================================================

// newTestMinter is deterministic so a failure is reproducible, but it is the
// REAL Minter with its real seams — not a stub.
func newTestMinter() *Minter {
	ms := int64(1756000000000)
	return NewMinter(
		WithClock(func() time.Time {
			ms++
			return time.UnixMilli(ms).UTC()
		}),
		WithEntropy(newDeterministicEntropy(42)),
	)
}

// deterministicEntropy is a reproducible byte source. It is NOT a PRNG worth
// minting production identifiers from; production uses crypto/rand.
type deterministicEntropy struct{ state uint64 }

func newDeterministicEntropy(seed uint64) *deterministicEntropy {
	return &deterministicEntropy{state: seed*6364136223846793005 + 1442695040888963407}
}

func (d *deterministicEntropy) Read(p []byte) (int, error) {
	for i := range p {
		d.state = d.state*6364136223846793005 + 1442695040888963407
		p[i] = byte(d.state >> 33)
	}
	return len(p), nil
}

func pidOfTextContaining(r *Registry, needle string) (PID, error) {
	var hits []PID
	for _, rec := range r.All() {
		if strings.Contains(rec.Text, needle) {
			hits = append(hits, rec.PID)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return "", fmt.Errorf("no passage contains %q", needle)
	default:
		return "", fmt.Errorf("%d passages contain %q; the fixture is ambiguous", len(hits), needle)
	}
}

func lookupByContentHash(r *Registry, hash string) PID {
	for _, rec := range r.All() {
		if rec.ContentHash == hash {
			return rec.PID
		}
	}
	return ""
}

func replaceInFile(path, old, new string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !bytes.Contains(b, []byte(old)) {
		return fmt.Errorf("fixture broken: %q is not in %s", old, path)
	}
	return os.WriteFile(path, bytes.Replace(b, []byte(old), []byte(new), 1), 0o644)
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	d, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return d
}

// moduleRootDir walks up to the nearest go.mod. Derived, never literal.
func moduleRootDir(t *testing.T) string {
	t.Helper()
	dir := mustGetwd(t)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked to the filesystem root without finding a go.mod")
		}
		dir = parent
	}
}
