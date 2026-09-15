package passage

// The code-passage matching key and its rename aliases (contract §4.3 DDL,
// §6.2, §6.4).
//
// # Why this exists
//
// A text passage carries an anchor that travels with it (§3.1): the identity
// is written into the artifact itself, so re-attaching it on the next ingest
// is a lookup, not a guess. Source code gets no anchor at all (§3.3) — this
// package is never allowed to stamp a comment into a file it does not own —
// so re-attachment happens by MATCHING on where the symbol lives, and
// matching is a heuristic. §6 states that plainly rather than pretending the
// two guarantees are the same strength.
//
// The matching key is `(path, symbol)` — SourceRef.Path and SourceRef.Symbol,
// both already declared on Record. This file adds nothing to that key; it
// adds the table a caller consults when the key alone no longer matches
// because the symbol moved: `symbol_aliases`.
//
// # authored vs detected
//
// An alias is either written by whoever performed a rename (`authored` —
// authoritative) or proposed by an ingest heuristic (`detected` —
// best-effort, and stated as such). The heuristic can mis-attach two similar
// helpers, so a `detected` row must stay visibly a guess: a reviewer reading
// the table needs to see which links rest on a deliberate decision and which
// rest on a pattern match. Put refuses to let a later `detected` guess
// silently overwrite an `authored` decision for the same old key — see
// ErrAliasDowngrade.
//
// # Persistence
//
// The shape mirrors RedactionLog in redaction.go: a git-reviewable JSONL file
// is the source of truth, and the `symbol_aliases` table in passages.db
// (already declared in schemaSQL) is a cache rebuilt wholesale from it. One
// simplification against the SQL schema's four-column primary key: this
// store is indexed, and therefore looked up, by the OLD key alone
// (old_path, old_symbol) — every ingest lookup in this package's matching
// algorithm (ingest_match.go, R3) only ever needs "what did this old key
// become", never "list every row sharing an old key with a different
// target". A caller with a genuine need for more than one target per old key
// is better served reading All() directly.

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

// AliasOrigin records who is responsible for one rename entry.
type AliasOrigin string

const (
	// AliasAuthored: written by a maintainer as part of the rename.
	// Authoritative.
	AliasAuthored AliasOrigin = "authored"
	// AliasDetected: proposed by an ingest heuristic. Best-effort, may be
	// wrong — it can mis-attach two similarly named or similarly shaped
	// symbols, and is recorded with this origin precisely so a reviewer can
	// tell.
	AliasDetected AliasOrigin = "detected"
)

// Valid reports whether o is one of the two defined origins.
func (o AliasOrigin) Valid() bool { return o == AliasAuthored || o == AliasDetected }

// symbolKey renders a (path, symbol) pair as the string this file indexes on.
// NUL cannot appear in either field's intended values (a repo-relative path,
// a symbol path), so it is a safe separator that needs no escaping.
func symbolKey(path, symbol string) string { return path + "\x00" + symbol }

// SymbolAlias maps an old (path, symbol) to a new one, preserving the pid —
// contract §4.3 `symbol_aliases`. Field order is the serialisation order and
// is fixed, so an unchanged store re-writes byte-identically.
type SymbolAlias struct {
	OldPath   string      `json:"old_path"`
	OldSymbol string      `json:"old_symbol"`
	NewPath   string      `json:"new_path"`
	NewSymbol string      `json:"new_symbol"`
	PID       PID         `json:"pid"`
	Origin    AliasOrigin `json:"origin"`
	At        string      `json:"at"`
}

// Validate enforces the field-level invariants of one alias entry.
func (a SymbolAlias) Validate() error {
	if strings.TrimSpace(a.OldPath) == "" {
		return errors.New("passage: symbol alias carries an empty old_path")
	}
	if strings.TrimSpace(a.OldSymbol) == "" {
		return errors.New("passage: symbol alias carries an empty old_symbol")
	}
	if strings.TrimSpace(a.NewPath) == "" {
		return errors.New("passage: symbol alias carries an empty new_path")
	}
	if strings.TrimSpace(a.NewSymbol) == "" {
		return errors.New("passage: symbol alias carries an empty new_symbol")
	}
	if !a.PID.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidPID, a.PID)
	}
	if !a.Origin.Valid() {
		return fmt.Errorf("passage: unknown alias origin %q (want %q or %q)",
			a.Origin, AliasAuthored, AliasDetected)
	}
	if strings.TrimSpace(a.At) == "" {
		return errors.New("passage: symbol alias carries no timestamp")
	}
	if _, err := time.Parse(time.RFC3339, a.At); err != nil {
		return fmt.Errorf("passage: symbol alias timestamp %q is not RFC3339: %w", a.At, err)
	}
	if a.OldPath == a.NewPath && a.OldSymbol == a.NewSymbol {
		return errors.New("passage: symbol alias old and new key are identical; " +
			"nothing was renamed, so there is nothing to alias")
	}
	return nil
}

// ErrAliasDowngrade is returned by Put when a `detected` guess would replace
// an `authored`, authoritative entry for the same old key. Authority never
// silently loses to a heuristic.
var ErrAliasDowngrade = errors.New(
	"passage: refusing to replace an authored alias with a detected one")

// SymbolAliasStore is the set of rename aliases.
//
// It keeps TWO indexes over the same rows, because the two directions answer
// different questions. Lookup, by the OLD key, answers "what did this
// identity become" — a reviewer's question. LookupNew, by the NEW key,
// answers "what identity does the artifact at this (path, symbol) belong
// to" — ingest's question (contract §8 R3): a scan of the tree as it stands
// today observes the symbol's CURRENT name and location, never its old one,
// so matching has to search by where the alias says the symbol lives NOW.
// See the file comment for why the store, unlike the full SQL primary key,
// is indexed one row per key rather than a set per key.
type SymbolAliasStore struct {
	byOld map[string]SymbolAlias
	byNew map[string]SymbolAlias
}

// NewSymbolAliasStore returns an empty store.
func NewSymbolAliasStore() *SymbolAliasStore {
	return &SymbolAliasStore{byOld: map[string]SymbolAlias{}, byNew: map[string]SymbolAlias{}}
}

// Len is the number of aliases.
func (s *SymbolAliasStore) Len() int {
	if s == nil {
		return 0
	}
	return len(s.byOld)
}

// Lookup returns the alias whose OLD key is (path, symbol), if one exists.
func (s *SymbolAliasStore) Lookup(oldPath, oldSymbol string) (SymbolAlias, bool) {
	if s == nil {
		return SymbolAlias{}, false
	}
	a, ok := s.byOld[symbolKey(oldPath, oldSymbol)]
	return a, ok
}

// LookupNew returns the alias whose NEW key is (path, symbol), if one
// exists. This is the direction ingest matching (§8 R3) actually needs: a
// scan of the artifact as it stands today observes the symbol's CURRENT
// name, and the alias says which pid that current name belongs to.
func (s *SymbolAliasStore) LookupNew(newPath, newSymbol string) (SymbolAlias, bool) {
	if s == nil {
		return SymbolAlias{}, false
	}
	a, ok := s.byNew[symbolKey(newPath, newSymbol)]
	return a, ok
}

// All returns every alias, sorted so iteration and serialisation are
// deterministic.
func (s *SymbolAliasStore) All() []SymbolAlias {
	if s == nil {
		return nil
	}
	out := make([]SymbolAlias, 0, len(s.byOld))
	for _, a := range s.byOld {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OldPath != out[j].OldPath {
			return out[i].OldPath < out[j].OldPath
		}
		if out[i].OldSymbol != out[j].OldSymbol {
			return out[i].OldSymbol < out[j].OldSymbol
		}
		if out[i].NewPath != out[j].NewPath {
			return out[i].NewPath < out[j].NewPath
		}
		return out[i].NewSymbol < out[j].NewSymbol
	})
	return out
}

// Put inserts or replaces the alias for a.OldPath/a.OldSymbol.
//
// A `detected` entry never overwrites an `authored` one for the same old
// key — see ErrAliasDowngrade. Every other combination (a fresh key,
// `authored` replacing `detected`, or the same origin replacing itself) is
// allowed.
func (s *SymbolAliasStore) Put(a SymbolAlias) error {
	if s == nil {
		return errors.New("passage: nil symbol alias store")
	}
	if err := a.Validate(); err != nil {
		return err
	}
	oldKey := symbolKey(a.OldPath, a.OldSymbol)
	if prev, ok := s.byOld[oldKey]; ok {
		if prev.Origin == AliasAuthored && a.Origin == AliasDetected {
			return fmt.Errorf("%w: %s:%s is already aliased to %s:%s by an authored entry",
				ErrAliasDowngrade, a.OldPath, a.OldSymbol, prev.NewPath, prev.NewSymbol)
		}
	}
	s.byOld[oldKey] = a
	s.byNew[symbolKey(a.NewPath, a.NewSymbol)] = a
	return nil
}

// LoadSymbolAliasesJSONL reads a store from a git-reviewable JSONL file.
//
// A file that does not exist yields an EMPTY store and no error — a
// DETERMINED state (no alias has ever been written), not an inability to
// tell. Any other read failure is returned.
//
// A key this type does not define is REFUSED, not dropped, for the same
// reason Registry.LoadJSONL refuses one: a rename table that silently loses
// a field is not a reviewable one.
func LoadSymbolAliasesJSONL(path string) (*SymbolAliasStore, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewSymbolAliasStore(), nil
		}
		return nil, err
	}
	store := NewSymbolAliasStore()
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		var a SymbolAlias
		if err := dec.Decode(&a); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		if err := store.Put(a); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
	}
	return store, nil
}

// WriteJSONL writes the store: UTF-8, LF, final newline, one compact record
// per line, fixed key order, sorted so re-writing an unchanged store produces
// a byte-identical file.
func (s *SymbolAliasStore) WriteJSONL(path string) error {
	var sb strings.Builder
	for _, a := range s.All() {
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		sb.Write(b)
		sb.WriteByte('\n')
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// SyncDB rebuilds the derived `symbol_aliases` table from this store.
//
// The table is a CACHE of the JSONL file, exactly as `passages` caches the
// registry and `redactions` caches RedactionLog: rebuilt wholesale rather
// than migrated, so running this twice is safe. Call it after
// Registry.BuildDB, since the table declares a foreign key onto
// passages(pid).
func (s *SymbolAliasStore) SyncDB(ctx context.Context, path string) error {
	if s == nil {
		return errors.New("passage: nil symbol alias store")
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

	if _, err := tx.ExecContext(ctx, `DELETE FROM symbol_aliases`); err != nil {
		return err
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO symbol_aliases
	  (old_path,old_symbol,new_path,new_symbol,pid,origin,at) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for _, a := range s.All() {
		if _, err := ins.ExecContext(ctx, a.OldPath, a.OldSymbol, a.NewPath, a.NewSymbol,
			string(a.PID), string(a.Origin), a.At); err != nil {
			return fmt.Errorf("passage: writing alias %s:%s -> %s:%s: %w",
				a.OldPath, a.OldSymbol, a.NewPath, a.NewSymbol, err)
		}
	}
	return tx.Commit()
}
