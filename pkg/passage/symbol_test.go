package passage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// ===========================================================================
// helpers
// ===========================================================================

func strp(s string) *string { return &s }

// codeObs builds a symbol-keyed Observation — the shape a caller supplies to
// IngestCode for material with no travelling anchor.
func codeObs(path, symbol, text string) Observation {
	return Observation{
		Kind: fixtureKind,
		Text: text,
		Ref: SourceRef{
			Path:   path,
			Symbol: strp(symbol),
			Anchor: AnchorRegistryOnly,
		},
	}
}

func validAlias(oldPath, oldSymbol, newPath, newSymbol string, pid PID, origin AliasOrigin) SymbolAlias {
	return SymbolAlias{
		OldPath: oldPath, OldSymbol: oldSymbol,
		NewPath: newPath, NewSymbol: newSymbol,
		PID: pid, Origin: origin, At: "2026-09-05T12:00:00Z",
	}
}

// ===========================================================================
// SymbolAlias / SymbolAliasStore — direct coverage of T031.
// ===========================================================================

func TestSymbolAliasValidate(t *testing.T) {
	const pid = PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")
	good := validAlias("a/b.go", "Old", "a/b.go", "New", pid, AliasAuthored)
	if err := good.Validate(); err != nil {
		t.Fatalf("a well-formed alias was rejected: %v", err)
	}

	cases := []struct {
		name string
		a    SymbolAlias
	}{
		{"empty old_path", validAlias("", "Old", "a/b.go", "New", pid, AliasAuthored)},
		{"empty old_symbol", validAlias("a/b.go", "", "a/b.go", "New", pid, AliasAuthored)},
		{"empty new_path", validAlias("a/b.go", "Old", "", "New", pid, AliasAuthored)},
		{"empty new_symbol", validAlias("a/b.go", "Old", "a/b.go", "", pid, AliasAuthored)},
		{"invalid pid", validAlias("a/b.go", "Old", "a/b.go", "New", PID("not-a-pid"), AliasAuthored)},
		{"unknown origin", validAlias("a/b.go", "Old", "a/b.go", "New", pid, AliasOrigin("guessed"))},
		{"no timestamp", func() SymbolAlias {
			a := validAlias("a/b.go", "Old", "a/b.go", "New", pid, AliasAuthored)
			a.At = ""
			return a
		}()},
		{"non-RFC3339 timestamp", func() SymbolAlias {
			a := validAlias("a/b.go", "Old", "a/b.go", "New", pid, AliasAuthored)
			a.At = "not-a-time"
			return a
		}()},
		{"old equals new", validAlias("a/b.go", "Old", "a/b.go", "Old", pid, AliasAuthored)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.a.Validate(); err == nil {
				t.Fatalf("%s: accepted an invalid alias", c.name)
			}
		})
	}
}

func TestSymbolAliasStorePutLookupAll(t *testing.T) {
	const pid1, pid2 = PID("01JBX7QK3M8V2ZC4YT5N6RWDPA"), PID("01JBX7QK3M8V2ZC4YT5N6RWDPB")
	s := NewSymbolAliasStore()
	if s.Len() != 0 {
		t.Fatalf("a fresh store has %d entries, want 0", s.Len())
	}
	if _, ok := s.Lookup("a/b.go", "Old"); ok {
		t.Fatal("Lookup found an entry in an empty store")
	}

	a1 := validAlias("a/b.go", "Old", "a/b.go", "New", pid1, AliasAuthored)
	if err := s.Put(a1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	a2 := validAlias("c/d.go", "Foo", "c/d.go", "Bar", pid2, AliasDetected)
	if err := s.Put(a2); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	got, ok := s.Lookup("a/b.go", "Old")
	if !ok || got != a1 {
		t.Fatalf("Lookup(a/b.go, Old) = %+v, %v, want %+v, true", got, ok, a1)
	}

	all := s.All()
	if len(all) != 2 {
		t.Fatalf("All returned %d entries, want 2", len(all))
	}
	if all[0].OldPath > all[1].OldPath {
		t.Fatal("All is not sorted ascending by old_path")
	}
}

func TestSymbolAliasStorePutRefusesAuthoredDowngrade(t *testing.T) {
	const pid1, pid2 = PID("01JBX7QK3M8V2ZC4YT5N6RWDPA"), PID("01JBX7QK3M8V2ZC4YT5N6RWDPB")
	s := NewSymbolAliasStore()
	authored := validAlias("a/b.go", "Old", "a/b.go", "New", pid1, AliasAuthored)
	if err := s.Put(authored); err != nil {
		t.Fatalf("Put authored: %v", err)
	}

	// A later, unrelated `detected` guess for the SAME old key must not
	// silently replace the maintainer's own decision.
	guess := validAlias("a/b.go", "Old", "a/b.go", "Elsewhere", pid2, AliasDetected)
	if err := s.Put(guess); !errors.Is(err, ErrAliasDowngrade) {
		t.Fatalf("Put(detected over authored) = %v, want %v", err, ErrAliasDowngrade)
	}
	got, _ := s.Lookup("a/b.go", "Old")
	if got != authored {
		t.Fatalf("the authored alias was overwritten: %+v", got)
	}

	// The reverse direction — authored replacing detected, or the same
	// origin replacing itself — is allowed.
	s2 := NewSymbolAliasStore()
	if err := s2.Put(guess); err != nil {
		t.Fatalf("Put detected: %v", err)
	}
	if err := s2.Put(authored); err != nil {
		t.Fatalf("an authored entry could not replace a detected one: %v", err)
	}
	if got, _ := s2.Lookup("a/b.go", "Old"); got != authored {
		t.Fatalf("authored did not win: %+v", got)
	}
}

func TestSymbolAliasStoreJSONLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "symbol_aliases.jsonl")

	// A file that has never existed is an EMPTY, DETERMINED store.
	empty, err := LoadSymbolAliasesJSONL(path)
	if err != nil {
		t.Fatalf("loading an absent file: %v", err)
	}
	if empty.Len() != 0 {
		t.Fatalf("an absent file loaded %d entries", empty.Len())
	}

	const pid = PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")
	s := NewSymbolAliasStore()
	if err := s.Put(validAlias("a/b.go", "Old", "a/b.go", "New", pid, AliasAuthored)); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteJSONL(path); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadSymbolAliasesJSONL(path)
	if err != nil {
		t.Fatalf("LoadSymbolAliasesJSONL: %v", err)
	}
	if loaded.Len() != 1 {
		t.Fatalf("loaded %d entries, want 1", loaded.Len())
	}
	got, ok := loaded.Lookup("a/b.go", "Old")
	if !ok || got.NewSymbol != "New" || got.PID != pid {
		t.Fatalf("round-tripped alias = %+v", got)
	}

	// Re-writing an unchanged store is byte-identical (the same discipline
	// Registry.WriteJSONL applies to passages.jsonl).
	if err := loaded.WriteJSONL(path); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("re-writing an unchanged store was not byte-identical:\n--- 1 ---\n%s\n--- 2 ---\n%s",
			first, second)
	}
}

func TestSymbolAliasStoreLoadRefusesUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "symbol_aliases.jsonl")
	line := `{"old_path":"a/b.go","old_symbol":"Old","new_path":"a/b.go","new_symbol":"New",` +
		`"pid":"01JBX7QK3M8V2ZC4YT5N6RWDPA","origin":"authored","at":"2026-09-05T12:00:00Z",` +
		`"unexpected_field":"x"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSymbolAliasesJSONL(path); err == nil {
		t.Fatal("a key this type does not define was silently dropped instead of refused")
	}
}

func TestSymbolAliasStoreSyncDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "p.db")
	ctx := context.Background()

	reg := NewRegistry()
	m := newTestMinter()
	res, err := reg.IngestCode([]Observation{codeObs("a/b.go", "Old", "func Old() {}")}, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pid := res.Assigned[0]
	if err := reg.BuildDB(ctx, dbPath); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}

	s := NewSymbolAliasStore()
	if err := s.Put(validAlias("a/b.go", "Old", "a/b.go", "New", pid, AliasAuthored)); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncDB(ctx, dbPath); err != nil {
		t.Fatalf("SyncDB: %v", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM symbol_aliases WHERE old_path = ? AND old_symbol = ? `+
			`AND new_symbol = ? AND pid = ? AND origin = ?`,
		"a/b.go", "Old", "New", string(pid), string(AliasAuthored),
	).Scan(&n); err != nil {
		t.Fatalf("querying symbol_aliases: %v", err)
	}
	if n != 1 {
		t.Fatalf("symbol_aliases holds %d matching row(s), want 1", n)
	}

	// A cache is rebuilt wholesale, not accumulated: a second SyncDB with the
	// SAME store must not duplicate the row.
	if err := s.SyncDB(ctx, dbPath); err != nil {
		t.Fatalf("second SyncDB: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbol_aliases`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a second SyncDB produced %d rows, want 1", n)
	}
}

// ===========================================================================
// IngestCode — direct coverage of T032's R2 / R3 / M4 branches.
// ===========================================================================

func TestIngestCodeExactKeyReattaches(t *testing.T) {
	reg := NewRegistry()
	m := newTestMinter()

	res1, err := reg.IngestCode(
		[]Observation{codeObs("a/b.go", "Foo", "func Foo() int { return 1 }")}, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res1.Branch[0] != CodeBranchMinted {
		t.Fatalf("first sight branch = %s, want %s", res1.Branch[0], CodeBranchMinted)
	}
	p1 := res1.Assigned[0]

	res2, err := reg.IngestCode(
		[]Observation{codeObs("a/b.go", "Foo", "func Foo() int { return 2 }")}, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Branch[0] != CodeBranchExactKey {
		t.Fatalf("re-attach branch = %s, want %s (R2)", res2.Branch[0], CodeBranchExactKey)
	}
	if res2.Assigned[0] != p1 {
		t.Fatalf("pid changed on a body edit under the SAME key: %s -> %s", p1, res2.Assigned[0])
	}
	rec, ok := reg.Lookup(p1)
	if !ok {
		t.Fatal("the re-attached row is missing")
	}
	if rec.Text != "func Foo() int { return 2 }" {
		t.Fatalf("stored text = %q", rec.Text)
	}
	if rec.ContentHash != ContentHash(rec.Text) {
		t.Fatal("content_hash was not recomputed on a body edit")
	}
	if rec.Provenance != ProvenanceHumanCorrected {
		t.Fatalf("provenance = %q, want %q once the body diverged from machine_text",
			rec.Provenance, ProvenanceHumanCorrected)
	}
	if rec.MachineText != "func Foo() int { return 1 }" {
		t.Fatalf("machine_text was mutated (F1): %q", rec.MachineText)
	}

	// I1/I3 discipline: an unchanged second ingest attaches without minting
	// or reporting a correction.
	res3, err := reg.IngestCode(
		[]Observation{codeObs("a/b.go", "Foo", "func Foo() int { return 2 }")}, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Branch[0] != CodeBranchExactKey || res3.Assigned[0] != p1 {
		t.Fatalf("idempotent re-ingest changed the outcome: %+v", res3)
	}
}

func TestIngestCodeAliasAttaches(t *testing.T) {
	reg := NewRegistry()
	m := newTestMinter()

	res1, err := reg.IngestCode(
		[]Observation{codeObs("a/b.go", "Old", "func Old() {}")}, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p1 := res1.Assigned[0]

	aliases := NewSymbolAliasStore()
	if err := aliases.Put(validAlias("a/b.go", "Old", "a/b.go", "New", p1, AliasAuthored)); err != nil {
		t.Fatal(err)
	}

	res2, err := reg.IngestCode(
		[]Observation{codeObs("a/b.go", "New", "func New() {}")}, m, aliases, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Branch[0] != CodeBranchAlias {
		t.Fatalf("branch = %s, want %s (R3)", res2.Branch[0], CodeBranchAlias)
	}
	if res2.Assigned[0] != p1 {
		t.Fatalf("alias attach changed the pid: %s -> %s", p1, res2.Assigned[0])
	}
	if got := reg.Resolve(p1); got.Outcome != OutcomeFound {
		t.Fatalf("resolve(%s) = %s, want %s", p1, got.Outcome, OutcomeFound)
	}
}

func TestIngestCodeMintsWhenNothingMatches(t *testing.T) {
	reg := NewRegistry()
	m := newTestMinter()

	res, err := reg.IngestCode(
		[]Observation{codeObs("a/b.go", "Brand", "func Brand() {}")}, m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Branch[0] != CodeBranchMinted {
		t.Fatalf("branch = %s, want %s (M4)", res.Branch[0], CodeBranchMinted)
	}
	if !res.Assigned[0].Valid() {
		t.Fatalf("minted pid %q fails validation", res.Assigned[0])
	}
	if len(res.Orphaned) != 0 {
		t.Fatalf("nothing vanished, yet %d row(s) were orphaned", len(res.Orphaned))
	}
}

func TestIngestCodeRejectsObservationWithNoSymbol(t *testing.T) {
	reg := NewRegistry()
	m := newTestMinter()
	obs := Observation{Kind: fixtureKind, Text: "x", Ref: SourceRef{Path: "a/b.go", Anchor: AnchorRegistryOnly}}
	if _, err := reg.IngestCode([]Observation{obs}, m, nil, nil); err == nil {
		t.Fatal("an observation with no symbol was accepted by IngestCode")
	}
}

// ===========================================================================
// G-PID-4 (contract §6.5, §6.6) — the code-identity gate and its paired
// mutation.
//
// checkGPID4 carries both halves of the gate so the REAL and the MUTATED run
// share one set of assertions, exactly as checkG1/checkG2 in pid_test.go
// share theirs between a gate test and its paired-mutation test.
// ===========================================================================

// checkGPID4 runs the gate against strategy and returns the first violation,
// or nil if every assertion held. DefaultRenameStrategy must return nil; a
// deliberately wrong strategy must return non-nil.
func checkGPID4(strategy RenameStrategy) error {
	reg := NewRegistry()
	m := newTestMinter()
	requireAlias := func(string) bool { return true } // this tree requires an authored alias

	// --- half 1: rename WITH an authored alias -> the pid is PRESERVED and
	//     its citations still resolve. ---
	res1, err := reg.IngestCode(
		[]Observation{codeObs("pkg/svc.go", "Handle", "func Handle() {}")}, m, nil, nil)
	if err != nil {
		return fmt.Errorf("setting up the tracked symbol: %w", err)
	}
	trackedPID := res1.Assigned[0]

	aliases := NewSymbolAliasStore()
	if err := aliases.Put(validAlias(
		"pkg/svc.go", "Handle", "pkg/svc.go", "Serve", trackedPID, AliasAuthored)); err != nil {
		return fmt.Errorf("storing the authored alias: %w", err)
	}

	res2, err := reg.IngestCode(
		[]Observation{codeObs("pkg/svc.go", "Serve", "func Serve() {}")},
		m, aliases, requireAlias, WithRenameStrategy(strategy))
	if err != nil {
		return fmt.Errorf("renaming WITH an authored alias failed ingest: %w", err)
	}
	if got := res2.Assigned[0]; got != trackedPID {
		return fmt.Errorf("pid NOT preserved across an aliased rename: %s -> %s", trackedPID, got)
	}
	if got := reg.Resolve(trackedPID); got.Outcome != OutcomeFound {
		return fmt.Errorf("the preserved pid resolves %q, want %q", got.Outcome, OutcomeFound)
	}

	// --- half 2: rename the SAME KIND of symbol WITHOUT an alias -> the old
	//     pid resolves not_in_registry (a loud dead link) AND ingest reports
	//     the unaliased rename as a non-nil error (a caller's exit 1). ---
	res3, err := reg.IngestCode(
		[]Observation{codeObs("pkg/other.go", "Fetch", "func Fetch() {}")}, m, nil, nil)
	if err != nil {
		return fmt.Errorf("setting up the unaliased symbol: %w", err)
	}
	unaliasedPID := res3.Assigned[0]

	_, err = reg.IngestCode(
		[]Observation{codeObs("pkg/other.go", "Retrieve", "func Retrieve() {}")},
		m, nil, requireAlias, WithRenameStrategy(strategy))
	if err == nil {
		return errors.New("an unaliased rename did not fail ingest (want a non-nil error, " +
			"which a caller reports as exit 1)")
	}
	if !errors.Is(err, ErrUnaliasedRename) {
		return fmt.Errorf("ingest failed for the unaliased rename, but not with "+
			"ErrUnaliasedRename: %w", err)
	}
	if got := reg.Resolve(unaliasedPID); got.Outcome != OutcomeNotInRegistry {
		return fmt.Errorf("the old pid resolves %q after an unaliased rename, want %q "+
			"(a loud dead link, never a stale-but-resolvable row)", got.Outcome, OutcomeNotInRegistry)
	}
	return nil
}

func TestGPID4_CodeIdentityGate(t *testing.T) {
	if err := checkGPID4(DefaultRenameStrategy()); err != nil {
		t.Fatal(err)
	}
}

// mutantSilentRename is the mutation named by the contract: make an
// unaliased rename mint a new pid SILENTLY, with no refusal, and leave the
// old row exactly as it was. From the registry's point of view that is what
// "re-point the old citation by nearest-text match" amounts to: the stale
// identifier keeps resolving as though nothing happened, instead of the
// citation going loudly dead.
type mutantSilentRename struct{}

func (mutantSilentRename) Name() string    { return "MUTANT:silent-rename" }
func (mutantSilentRename) renameStrategy() {}
func (mutantSilentRename) Resolve(m *Minter, requireAlias bool, oldRec Record,
	candidate Observation) (PID, bool, string, error) {
	newPID, err := m.Mint()
	if err != nil {
		return "", false, "", err
	}
	return newPID, false, "", nil // never orphans, never refuses -- regardless of requireAlias
}

func TestGPID4_PairedMutation_SilentRenameIsCaught(t *testing.T) {
	if err := checkGPID4(mutantSilentRename{}); err == nil {
		t.Fatal("MUTATION SURVIVED: G-PID-4 passed even with a strategy that mints a new " +
			"pid silently and never breaks the old citation. The gate is inoperative — it " +
			"would not catch the exact defect §6.5/§6.6 exist to prevent.")
	} else {
		t.Logf("mutation caught: %v", err)
	}
}
