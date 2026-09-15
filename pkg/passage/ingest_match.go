package passage

// Ingest matching for symbol-keyed passages — contract §8, branches R2
// through R4, with minting reserved as the LAST branch (M4).
//
// # Scope, and how it is expressed without naming a consumer's tree
//
// Text passages carry an anchor (§3.1) and are matched by Registry.Sync via
// AnchorKey. A passage with no travelling anchor — most commonly code, since
// this package is never allowed to write a comment into a file it does not
// own (§3.3) — has to be re-attached by matching on where it lives, and this
// file is that algorithm.
//
// It is triggered structurally, never by comparing an observation's Kind to
// some hardcoded classification: any Observation whose Ref.Symbol is
// non-nil is symbol-keyed and goes through IngestCode. Kind stays exactly
// what Registry already documents it as — the CONSUMER's own vocabulary,
// opaque to this package — so a second consumer whose symbol-keyed material
// is not what the contract calls code loses nothing by using this file.
//
// R4 distinguishes a tree that REQUIRES an authored alias before an
// unaliased rename is accepted (contract §6.6: the ingest gate refuses,
// which a caller turns into a non-zero exit) from every other tree (§6.5:
// mint a fresh pid and remove the stale row, so the old citation goes dead
// rather than silently repointing). This package has no notion of which
// tree that is — it is a policy belonging to whichever project consumes it,
// not to identity — so the caller supplies it as RequireAliasFunc, exactly
// as Registry.WithRecordValidator lets a caller install its own invariants
// at the write path without this package having to know what they check.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RequireAliasFunc reports whether an unaliased rename at path must be
// refused (true, contract §6.6) rather than accepted through the weaker
// mint-and-remove path (false, §6.5). A caller with no such distinction
// passes a function that always returns false, or passes nil — IngestCode
// treats a nil RequireAliasFunc the same way.
type RequireAliasFunc func(path string) bool

// CodeBranch names which §8 branch produced (or refused) a pid for one
// observation, so a caller can report an ingest run with the same honesty
// the alias table requires of itself: a reviewer should be able to tell an
// exact-key attach from a best-effort alias from a freshly minted identity.
type CodeBranch string

const (
	// CodeBranchExactKey: R2, an unchanged (path, symbol).
	CodeBranchExactKey CodeBranch = "R2_exact_key"
	// CodeBranchAlias: R3, attached through symbol_aliases.
	CodeBranchAlias CodeBranch = "R3_symbol_alias"
	// CodeBranchMintedOrphan: R4, a fresh pid minted for an unaliased rename
	// outside a tree that requires one, with the stale row removed so the
	// old citation is a loud dead link rather than a silent misattachment.
	CodeBranchMintedOrphan CodeBranch = "R4_mint_and_orphan"
	// CodeBranchMinted: M4, no exact key, no alias, and nothing vanished for
	// this observation to have plausibly replaced — a new symbol, not a
	// rename this run can see.
	CodeBranchMinted CodeBranch = "M4_minted"
)

// CodeIngestResult reports what IngestCode did.
type CodeIngestResult struct {
	// Assigned holds the pid each observation ended up with, in the order
	// observations were supplied — mirrors SyncResult.Assigned.
	Assigned []PID
	// Branch is parallel to Assigned: which §8 branch produced each pid.
	Branch []CodeBranch
	// Orphaned holds every pid whose row was removed this run because its
	// symbol vanished and the rename that (probably) explains it carried no
	// alias. A removed pid resolves not_in_registry — the loud failure
	// contract §6.5 requires, never a silent repoint.
	Orphaned []PID
}

// ErrUnaliasedRename is returned by IngestCode when one or more renames in a
// tree that RequireAliasFunc flags carried no alias. The message names every
// offending (old -> new) pair so an operator can add the missing authored
// entries in one pass.
var ErrUnaliasedRename = errors.New("passage: unaliased rename requires an authored alias")

// RenameStrategy decides the outcome for one vanished-symbol / candidate pair
// once R2 and R3 have both failed to attach the candidate (contract §6.5,
// §6.6). The interface carries an unexported method so only this package can
// implement it — production callers get DefaultRenameStrategy and nothing
// else; a paired-mutation proof (§1.1) injects a deliberately wrong strategy
// into the REAL IngestCode path, exactly as
// TestPairedMutation_UnconditionalMintingBreaksIdempotency injects a broken
// KeyStrategy into the real Sync path in pid_test.go.
type RenameStrategy interface {
	Name() string
	// Resolve is called once per pair. requireAlias is RequireAliasFunc's
	// answer for candidate.Ref.Path.
	//
	// A non-empty refused aborts the WHOLE ingest run for this candidate:
	// nothing is minted and the old row is left exactly as it was. It is the
	// operator-facing reason and is combined with every other refusal into
	// ErrUnaliasedRename.
	//
	// Otherwise a pid is returned. orphanOld reports whether the caller
	// should remove oldRec's row — the correct strategy always sets it when
	// it is not refusing, because leaving the stale row resolvable is
	// exactly the silent-misattachment failure mode contract §7.1 forbids.
	Resolve(m *Minter, requireAlias bool, oldRec Record, candidate Observation) (
		pid PID, orphanOld bool, refused string, err error)
	renameStrategy()
}

type defaultRenameStrategy struct{}

func (defaultRenameStrategy) Name() string    { return "default" }
func (defaultRenameStrategy) renameStrategy() {}
func (defaultRenameStrategy) Resolve(m *Minter, requireAlias bool, oldRec Record,
	candidate Observation) (PID, bool, string, error) {

	if requireAlias {
		// The old row is STILL removed (orphanOld=true): §6.5's "resolves
		// not_in_registry" is the failure mode of an unaliased rename
		// itself, not a consequence of whether this tree also refuses to
		// mint. Refusing without breaking the stale link would leave a
		// reader trusting a citation that no longer points at anything real
		// — the opposite of "fails loudly".
		return "", true, fmt.Sprintf(
			"%s: %s renamed to %s (old pid %s) carries no authored alias; this tree "+
				"requires one before an unaliased rename is accepted (contract §6.6)",
			candidate.Ref.Path, *oldRec.SourceRef.Symbol, *candidate.Ref.Symbol, oldRec.PID,
		), nil
	}
	newPID, err := m.Mint()
	if err != nil {
		return "", false, "", err
	}
	return newPID, true, "", nil
}

// DefaultRenameStrategy is the correct §6.5 / §6.6 behaviour and is what
// every production caller gets by default. It is exported only so a proof
// can name it explicitly when composing options.
func DefaultRenameStrategy() RenameStrategy { return defaultRenameStrategy{} }

// codeSyncConfig configures IngestCode. It intentionally does NOT reuse
// registry.go's syncConfig: that type's KeyStrategy is for anchor matching,
// which never applies to a symbol-keyed observation, and reusing it would
// let a caller pass an option that silently does nothing.
type codeSyncConfig struct {
	run      *string
	now      func() time.Time
	strategy RenameStrategy
}

// CodeSyncOption configures IngestCode.
type CodeSyncOption func(*codeSyncConfig)

// WithCodeIngestRun records a run id on every row this call touches.
func WithCodeIngestRun(id string) CodeSyncOption {
	return func(c *codeSyncConfig) { c.run = &id }
}

// WithCodeNow replaces the clock used for minted_at.
func WithCodeNow(fn func() time.Time) CodeSyncOption {
	return func(c *codeSyncConfig) { c.now = fn }
}

// WithRenameStrategy replaces the R4 decision. Production code should never
// pass anything but DefaultRenameStrategy — the option exists so a
// paired-mutation proof can inject a broken one into the real code path and
// demonstrate the gate catches it.
func WithRenameStrategy(s RenameStrategy) CodeSyncOption {
	return func(c *codeSyncConfig) { c.strategy = s }
}

// IngestCode runs the symbol-keyed branches of the §8 matching algorithm over
// one batch of observations.
//
// Every observation MUST carry a non-nil Ref.Symbol; a caller separates
// symbol-keyed material from anchored material before calling this, exactly
// as it separates it into Observation values in the first place — this
// package does not sniff Kind to decide for itself (see the file comment).
//
// Order matters and is the contract: R2 (exact key) and R3 (symbol_aliases)
// are tried for every observation first; only once BOTH have failed for a
// given observation does R4 (an unaliased-rename candidate) or, failing
// that, M4 (a fresh mint) apply. Minting is always the last resort.
func (r *Registry) IngestCode(obs []Observation, m *Minter, aliases *SymbolAliasStore,
	requireAlias RequireAliasFunc, opts ...CodeSyncOption) (CodeIngestResult, error) {

	cfg := codeSyncConfig{now: time.Now, strategy: DefaultRenameStrategy()}
	for _, o := range opts {
		o(&cfg)
	}
	if r == nil || r.unavailable != nil {
		return CodeIngestResult{}, errors.New("passage: registry unavailable")
	}
	if m == nil {
		return CodeIngestResult{}, errors.New("passage: no minter")
	}
	if requireAlias == nil {
		requireAlias = func(string) bool { return false }
	}

	// Every EXISTING row that carries a symbol key, indexed for R2. This is
	// a snapshot of what the registry held BEFORE this run touches anything,
	// so "vanished" (below) means "known before, not observed this run" —
	// never "removed earlier in this same loop".
	before := map[string]Record{}
	for _, rec := range r.rows {
		if rec.SourceRef.Symbol != nil {
			before[symbolKey(rec.SourceRef.Path, *rec.SourceRef.Symbol)] = rec
		}
	}
	seen := map[string]bool{}

	res := CodeIngestResult{
		Assigned: make([]PID, len(obs)),
		Branch:   make([]CodeBranch, len(obs)),
	}
	var unmatched []int

	for i, o := range obs {
		if o.Ref.Symbol == nil {
			return res, fmt.Errorf("passage: observation %d (%s) carries no symbol; "+
				"IngestCode matches only observations that supply one", i, o.Ref.Path)
		}
		key := symbolKey(o.Ref.Path, *o.Ref.Symbol)
		seen[key] = true

		if rec, ok := before[key]; ok { // R2
			pid, err := r.attachSymbol(rec, o, cfg.run)
			if err != nil {
				return res, err
			}
			res.Assigned[i] = pid
			res.Branch[i] = CodeBranchExactKey
			continue
		}
		if aliases != nil {
			if a, ok := aliases.LookupNew(o.Ref.Path, *o.Ref.Symbol); ok { // R3
				rec, ok2 := r.Lookup(a.PID)
				if !ok2 {
					return res, fmt.Errorf("passage: alias %s:%s -> %s:%s names pid %s, "+
						"which is not in the registry", a.OldPath, a.OldSymbol,
						a.NewPath, a.NewSymbol, a.PID)
				}
				pid, err := r.attachSymbol(rec, o, cfg.run)
				if err != nil {
					return res, err
				}
				res.Assigned[i] = pid
				res.Branch[i] = CodeBranchAlias
				continue
			}
		}
		unmatched = append(unmatched, i)
	}

	// R4: every row this run's observations never mentioned is a candidate
	// for "a symbol vanished". Sorted so pairing below is deterministic.
	var vanishedKeys []string
	for key := range before {
		if !seen[key] {
			vanishedKeys = append(vanishedKeys, key)
		}
	}
	sort.Strings(vanishedKeys)
	byPath := map[string][]string{}
	for _, key := range vanishedKeys {
		rec := before[key]
		byPath[rec.SourceRef.Path] = append(byPath[rec.SourceRef.Path], key)
	}

	// JUDGMENT CALL, recorded rather than left implicit: the contract names
	// the condition for R4 as "a symbol vanished and an unaliased candidate
	// exists" but does not specify how a vanished symbol is paired with a
	// candidate when a run contains more than one of either. Pairing within
	// the SAME FILE, in stable sorted order, is the narrowest reading that
	// still covers the motivating case (one symbol renamed in place) and
	// never guesses across files, which would be a materially weaker basis
	// for minting-and-removing a row. A vanished symbol with no unmatched
	// candidate in its file is left untouched — that is an ordinary deletion
	// (§6.3), which this function does not tombstone; it is out of this
	// file's scope.
	var refused []string
	for _, i := range unmatched {
		o := obs[i]
		candidates := byPath[o.Ref.Path]
		if len(candidates) == 0 {
			continue // M4 below: nothing vanished here, so this is new, not renamed
		}
		oldKey := candidates[0]
		byPath[o.Ref.Path] = candidates[1:]
		oldRec := before[oldKey]

		pid, orphanOld, reason, err := cfg.strategy.Resolve(m, requireAlias(o.Ref.Path), oldRec, o)
		if err != nil {
			return res, err
		}
		// Orphaning is decided BEFORE the refusal check and independently of
		// it: the correct strategy breaks the stale citation whether or not
		// it also refuses to mint a pid for the candidate. A mutant strategy
		// that never orphans is exactly the defect this file's paired
		// mutation proof (symbol_test.go) demonstrates the gate catches.
		if orphanOld {
			if err := r.orphanSymbol(oldRec.PID); err != nil {
				return res, err
			}
			res.Orphaned = append(res.Orphaned, oldRec.PID)
		}
		if reason != "" {
			refused = append(refused, reason)
			continue
		}
		rec := newSymbolRecord(pid, o, cfg.run, cfg.now)
		if err := r.Put(rec); err != nil {
			return res, err
		}
		res.Assigned[i] = pid
		res.Branch[i] = CodeBranchMintedOrphan
	}

	if len(refused) > 0 {
		sort.Strings(refused)
		return res, fmt.Errorf("%w:\n%s", ErrUnaliasedRename, strings.Join(refused, "\n"))
	}

	// M4: whatever is still unassigned mints fresh. Always the last branch.
	for i, pid := range res.Assigned {
		if pid != "" {
			continue
		}
		newPID, err := m.Mint()
		if err != nil {
			return res, err
		}
		rec := newSymbolRecord(newPID, obs[i], cfg.run, cfg.now)
		if err := r.Put(rec); err != nil {
			return res, err
		}
		res.Assigned[i] = newPID
		res.Branch[i] = CodeBranchMinted
	}
	sort.Slice(res.Orphaned, func(a, b int) bool { return res.Orphaned[a] < res.Orphaned[b] })
	return res, nil
}

// attachSymbol re-derives an existing row's SourceRef from where R2 or R3
// found it and, if the text changed, applies the same correction bookkeeping
// Sync applies for anchored passages: recompute content_hash and, once a
// machine layer exists, flip provenance to human_corrected. The machine
// layer itself is never touched (F1).
func (r *Registry) attachSymbol(rec Record, o Observation, run *string) (PID, error) {
	rec.SourceRef = o.Ref
	rec.SourceRef.Anchor = AnchorRegistryOnly
	rec.IngestRun = run
	if rec.Text != o.Text {
		rec.Text = o.Text
		rec.ContentHash = ContentHash(o.Text)
		if rec.MachineText != "" {
			rec.Provenance = ProvenanceHumanCorrected
		}
	}
	if err := r.putForce(rec); err != nil {
		return "", err
	}
	return rec.PID, nil
}

// newSymbolRecord builds a fresh row for a freshly minted pid. Anchor is
// always registry_only: nothing this function reaches is ever written back
// into the source file (§3.3).
func newSymbolRecord(pid PID, o Observation, run *string, now func() time.Time) Record {
	ref := o.Ref
	ref.Anchor = AnchorRegistryOnly
	return Record{
		PID: pid, Kind: o.Kind, Scope: o.Scope, ScopeOrder: o.ScopeOrder,
		SourceRef: ref, Text: o.Text, MachineText: o.Text,
		Provenance: ProvenanceMachine, Confidence: o.Confidence, Attrs: o.Attrs,
		ContentHash: ContentHash(o.Text), MintedAt: now().UTC().Format(time.RFC3339),
		IngestRun: run,
	}
}

// orphanSymbol removes a row entirely, so it resolves not_in_registry rather
// than staying a stale-but-resolvable record.
//
// This is deliberately STRONGER than the general M3 treatment ("tombstone,
// never free the identifier") text passages get: contract §6.5 states the
// code guarantee explicitly as "the old pid resolves as not_in_registry", not
// merely redacted, and that is what makes the break LOUD — a reader sees a
// dead link, never a row that still looks live.
func (r *Registry) orphanSymbol(pid PID) error {
	if r == nil {
		return errors.New("passage: nil registry")
	}
	if _, ok := r.rows[pid]; !ok {
		return fmt.Errorf("%w: %s", ErrPIDNotInRegistry, pid)
	}
	delete(r.rows, pid)
	return nil
}
