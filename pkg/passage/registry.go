package passage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vasic-digital/verdict/pkg/verdict"
	"golang.org/x/text/unicode/norm"

	_ "modernc.org/sqlite" // pure-Go SQLite with FTS5; no cgo
)

// Kind is the CONSUMER's own classification of a passage.
//
// This package declares no members and never will. An enum whose members are
// enumerated here would be this library naming its consumer's domain, and a
// second consumer with a different vocabulary would have to either misuse a
// member that does not fit or patch this file. Both are the coupling the
// module boundary exists to prevent.
//
// What survives is the validation that genuinely belongs to identity and
// storage rather than to any domain: a kind must be PRESENT and must be a
// stable, storable, diff-friendly token. Nothing here has an opinion about
// what the token means.
type Kind string

// kindPattern is the syntactic contract for a kind: lowercase ASCII, digits
// and underscores, starting with a letter, at most 64 characters. It is a
// storage constraint (indexable, case-stable, safe in a URL and in a column),
// not a vocabulary.
var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Valid reports whether k is a well-formed kind token. It says nothing about
// whether the kind means anything to any particular consumer — that judgement
// belongs to the consumer, and Registry.WithRecordValidator is the seam for it.
func (k Kind) Valid() bool { return kindPattern.MatchString(string(k)) }

// Provenance records whether the current text is the machine's or a human's.
// The transition is one-way: there is no path back, because the machine text
// is retained separately rather than overwritten.
//
// This one IS the library's own: it is provenance of the text this package
// stores, and G1 (a correction must not change the pid) is defined in terms
// of it. It is not a domain classification.
type Provenance string

const (
	ProvenanceMachine        Provenance = "machine"
	ProvenanceHumanCorrected Provenance = "human_corrected"
)

// SourceRef is where a passage was last seen.
//
// It is a CACHE, not a key. It is re-derived from the artifact on every sync,
// and a registry row whose SourceRef disagrees with the file is not a conflict
// — the file wins and the row is updated. That is guarantee G2 operating
// normally, not an error.
type SourceRef struct {
	Path      string     `json:"path"`
	LineStart int        `json:"line_start"`
	LineEnd   int        `json:"line_end"`
	Symbol    *string    `json:"symbol"`
	Anchor    AnchorMode `json:"anchor"`
}

// Record is one passage. Field order is the serialisation order and is fixed,
// so re-writing an unchanged registry is byte-identical.
type Record struct {
	PID  PID  `json:"pid"`
	Kind Kind `json:"kind"`

	// Scope is a CONSUMER-DEFINED grouping key, opaque to this package.
	//
	// It exists because grouping is a real storage need — the derived database
	// indexes on it — while the NAME of the group is not this library's
	// business. A consumer that groups by book section, by case file, by
	// meeting or by package puts its own key here; this package only stores,
	// indexes and returns it.
	Scope *string `json:"scope"`

	// ScopeOrder is a CONSUMER-DEFINED sort key WITHIN Scope, opaque to this
	// package. It is the second column of the passages_by_scope index, so a
	// consumer can page through a group in its own order without this package
	// knowing what that order means.
	ScopeOrder *float64 `json:"scope_order"`

	SourceRef SourceRef `json:"source_ref"`
	Text      string    `json:"text"`

	// MachineText is written once and NEVER mutated. A correction adds to the
	// correction overlay and updates Text; this field is untouched. Deleting
	// it would destroy the ability to re-measure producer accuracy later.
	MachineText string `json:"machine_text"`

	Provenance      Provenance `json:"provenance"`
	Confidence      *float64   `json:"confidence"`
	Uncertain       bool       `json:"uncertain"`
	UncertainReason *string    `json:"uncertain_reason"`

	// Attrs is CONSUMER-OWNED metadata. This package stores it, serialises it
	// and hands it back; it never reads a key, never validates a value against
	// a vocabulary, and never behaves differently because of one.
	//
	// It is map[string]string, not map[string]any, deliberately. passages.jsonl
	// is a source of truth that must re-write BYTE-IDENTICALLY and must diff
	// cleanly in a pull request. A JSON number round-trips through `any` as a
	// float64, so 512.0 comes back as `512` and a large integer loses
	// precision — an unchanged registry would then produce a changed file. A
	// consumer that needs a number formats it (strconv.FormatFloat with
	// precision -1 round-trips exactly) or puts it in ScopeOrder, which IS
	// typed.
	//
	// Keys must be non-empty and must not collide with a reserved name; see
	// reservedAttrKeys.
	Attrs map[string]string `json:"attrs"`

	Redacted bool `json:"redacted"`

	// ContentHash is CHANGE DETECTION ONLY and is deliberately NOT identity.
	//
	// Permitted, exhaustively: skip re-embedding unchanged text; detect that a
	// correction actually altered something; contribute to a generation's root
	// hash; detect an edit made outside the pipeline.
	//
	// Forbidden, exhaustively, and each is a DEFECT: as a citation target; as
	// a cross-reference endpoint; as any kind of key, unique index or foreign
	// key; as an equality test for "is this the same passage"; as a cache key
	// outliving a generation; aliased to a field named id, key, hash or
	// chunk_id.
	//
	// The name is part of the contract. It is `content_hash` in every
	// serialisation — JSONL, SQLite, HTTP, log lines — precisely so nobody
	// mistakes it for identity. Keying anything on it reproduces the measured
	// failure this whole package exists to prevent: a one-character correction
	// silently detaches every citation while the links keep rendering.
	ContentHash string `json:"content_hash"`

	// MintedAt is the provenance of the IDENTIFIER, not of the text.
	MintedAt  string  `json:"minted_at"`
	IngestRun *string `json:"ingest_run"`
}

// reservedAttrKeys are the metadata names this package emits from Metadata().
// An Attrs key may not use one, so a consumer's own value can never be
// silently overwritten by a library-owned one, or vice versa. A collision that
// resolves by "one of them wins" is the confident-but-wrong outcome; refusing
// the record is the honest one.
var reservedAttrKeys = map[string]bool{
	"kind": true, "scope": true, "scope_order": true,
	"path": true, "line_start": true, "line_end": true, "symbol": true,
	"provenance": true, "content_hash": true, "redacted": true,
}

// Validate enforces the field-level invariants THIS PACKAGE OWNS.
//
// It deliberately enforces nothing about what a passage means. Rules of the
// form "a passage of kind X requires field Y" are domain rules and belong to
// the consumer; Registry.WithRecordValidator is the seam that runs them at
// this same choke point, so moving them out of here does not move them out of
// the write path.
func (r Record) Validate() error {
	if !r.PID.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidPID, r.PID)
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("passage: kind %q is not a well-formed token "+
			"(lowercase ASCII letters, digits and underscores, starting with a letter, "+
			"1-64 characters); this package does not enumerate kinds, it only requires "+
			"that one is present and storable", r.Kind)
	}
	for k := range r.Attrs {
		if k == "" {
			return errors.New("passage: attrs carries an empty key; an attribute that " +
				"cannot be named cannot be read back")
		}
		if reservedAttrKeys[k] {
			return fmt.Errorf("passage: attrs key %q is reserved by this package "+
				"(see reservedAttrKeys); choose another name so neither value is "+
				"silently discarded", k)
		}
	}
	if r.SourceRef.Path == "" {
		return errors.New("passage: source_ref.path is empty")
	}
	switch r.SourceRef.Anchor {
	case AnchorInline, AnchorSidecar, AnchorRegistryOnly:
	default:
		return fmt.Errorf("passage: unknown anchor mode %q", r.SourceRef.Anchor)
	}
	if r.ContentHash == "" { // F7
		return errors.New("passage: content_hash is required on every row")
	}
	if r.MintedAt == "" {
		return errors.New("passage: minted_at is required")
	}
	switch r.Provenance {
	case ProvenanceMachine:
	case ProvenanceHumanCorrected: // F2
		if r.MachineText == "" {
			return errors.New("passage: human_corrected requires machine_text to be retained")
		}
	default:
		return fmt.Errorf("passage: unknown provenance %q", r.Provenance)
	}
	if r.Uncertain != (r.UncertainReason != nil) { // F3
		return errors.New("passage: uncertain and uncertain_reason must be set together")
	}
	return nil
}

// Metadata renders the record's library-owned facts plus its consumer-owned
// Attrs as one flat map.
//
// It exists so this package COMPOSES with a retrieval document type without
// depending on one. A retriever document of the common shape
// {ID, Content, Metadata, Score, Source} is filled in four lines:
//
//	doc.ID       = string(rec.PID)      // the minted pid IS the citable id
//	doc.Content  = rec.Public().Text    // Public() so a redacted row carries none
//	doc.Metadata = rec.Metadata()
//	doc.Source   = rec.SourceRef.Path
//
// Score belongs to the retriever and is none of this package's business.
// Nothing of that document type is imported, referenced or reimplemented here;
// only the shape is met.
//
// The library-owned keys are exactly reservedAttrKeys, which Validate refuses
// as Attrs keys, so no consumer value is ever shadowed.
func (r Record) Metadata() map[string]any {
	m := make(map[string]any, len(r.Attrs)+len(reservedAttrKeys))
	for k, v := range r.Attrs {
		m[k] = v
	}
	m["kind"] = string(r.Kind)
	if r.Scope != nil {
		m["scope"] = *r.Scope
	}
	if r.ScopeOrder != nil {
		m["scope_order"] = *r.ScopeOrder
	}
	m["path"] = r.SourceRef.Path
	m["line_start"] = r.SourceRef.LineStart
	m["line_end"] = r.SourceRef.LineEnd
	if r.SourceRef.Symbol != nil {
		m["symbol"] = *r.SourceRef.Symbol
	}
	m["provenance"] = string(r.Provenance)
	m["content_hash"] = r.ContentHash
	m["redacted"] = r.Redacted
	return m
}

// Public returns the record as it may leave the registry. A redacted passage
// carries NO text and NO machine_text at any layer (F6) — not merely in the
// rendered view.
func (r Record) Public() Record {
	if r.Redacted {
		r.Text = ""
		r.MachineText = ""
	}
	return r
}

// ContentHash computes the change-detection hash: SHA-256 over the NFC-
// normalised text with trailing whitespace stripped per line and lines
// LF-joined, rendered as lowercase hex with a "sha256:" prefix.
//
// It is NOT an identity. See Record.ContentHash.
func ContentHash(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	sum := sha256.Sum256(norm.NFC.Bytes([]byte(strings.Join(lines, "\n"))))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------------

// Outcome is one of exactly four resolution results. Three are the mandated
// three states; the fourth, redaction, is a determined answer of its own.
type Outcome string

const (
	// OutcomeFound: registry readable, row present, not redacted.
	OutcomeFound Outcome = "found"
	// OutcomeRedacted: registry readable, row present, suppressed.
	OutcomeRedacted Outcome = "redacted"
	// OutcomeNotInRegistry: registry readable, row absent. A DETERMINED
	// negative — the passage genuinely is not here.
	OutcomeNotInRegistry Outcome = "not_in_registry"
	// OutcomeUndetermined: the registry could not be read. NEVER collapsed
	// into not_in_registry — doing so would make an unreadable database look
	// like a corpus that never contained the passage.
	OutcomeUndetermined Outcome = "undetermined"
)

// Verdict maps an outcome onto the three-valued result type.
//
// Both `found` and `redacted` are OK: the question was answered. Only an
// absent row is a Problem, and only an unreadable registry is Undetermined.
func (o Outcome) Verdict() verdict.Verdict {
	switch o {
	case OutcomeFound, OutcomeRedacted:
		return verdict.OK
	case OutcomeNotInRegistry:
		return verdict.Problem
	default:
		return verdict.Undetermined
	}
}

// HTTPStatus is the status an API serves for this outcome.
func (o Outcome) HTTPStatus() int {
	switch o {
	case OutcomeFound:
		return 200
	case OutcomeRedacted:
		return 410
	case OutcomeNotInRegistry:
		return 404
	default:
		return 503
	}
}

// Resolution is the result of resolving a pid.
type Resolution struct {
	Outcome Outcome
	Record  Record
	// Err explains an undetermined outcome. Nil otherwise.
	Err error
}

// ---------------------------------------------------------------------------
// Registry
// ---------------------------------------------------------------------------

// Registry is the passage registry: identity, and everything reachable from
// it. `passages.jsonl` is the source of truth — human-reviewable, line-
// oriented, diffable, and tracked in git. `passages.db` is a derived cache,
// rebuildable at any time. If they disagree, the JSONL wins.
type Registry struct {
	rows map[PID]Record
	// unavailable, when non-nil, makes every resolution undetermined. It is
	// how "the registry could not be read" is represented as a value rather
	// than as a nil map that silently answers "not found".
	unavailable error
	// domain is the consumer's own record check, run at the same choke point
	// as Validate. Nil means the consumer has none.
	domain func(Record) error
}

// RegistryOption configures a Registry.
type RegistryOption func(*Registry)

// WithRecordValidator installs the CONSUMER's domain invariants on this
// registry's write path.
//
// It exists so that removing domain rules from this package does not remove
// them from the pipeline. Rules like "a passage of my kind X must carry a
// start and an end offset", or "an attributed name may only come from a human,
// because my producer cannot attribute one and must never claim to", are true
// and worth enforcing — they are simply not true of every consumer, so they
// cannot live here. Install them and they run on every Put, every Sync and
// every LoadJSONL, exactly where the package's own checks run.
//
// The function must be pure and must not retain the Record.
func WithRecordValidator(fn func(Record) error) RegistryOption {
	return func(r *Registry) { r.domain = fn }
}

// NewRegistry returns an empty, readable registry.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{rows: map[PID]Record{}}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Unavailable returns a Registry that resolves everything as undetermined.
// Use it when the store could not be opened, so callers cannot accidentally
// serve "no such passage" for "I could not look".
func Unavailable(reason error, opts ...RegistryOption) *Registry {
	if reason == nil {
		reason = errors.New("passage: registry unavailable")
	}
	r := &Registry{rows: map[PID]Record{}, unavailable: reason}
	for _, o := range opts {
		o(r)
	}
	return r
}

// validate runs this package's invariants and then the consumer's, in that
// order. Every write path goes through it; there is no second door.
func (r *Registry) validate(rec Record) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	if r.domain != nil {
		if err := r.domain(rec); err != nil {
			return fmt.Errorf("passage: %s rejected by the consumer's record validator: %w",
				rec.PID, err)
		}
	}
	return nil
}

// Len is the number of rows.
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.rows)
}

// Lookup returns a row by pid. Prefer Resolve, which distinguishes "absent"
// from "could not look".
func (r *Registry) Lookup(p PID) (Record, bool) {
	if r == nil || r.unavailable != nil {
		return Record{}, false
	}
	rec, ok := r.rows[p]
	return rec, ok
}

// All returns every row, sorted ascending by pid so iteration is deterministic.
func (r *Registry) All() []Record {
	if r == nil {
		return nil
	}
	out := make([]Record, 0, len(r.rows))
	for _, rec := range r.rows {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}

// Resolve is the ONE path from a pid to a passage.
//
// It NEVER falls back to fuzzy text matching, nearest-neighbour lookup, prefix
// matching, or "the passage with the same content_hash". An unknown pid is not
// "probably this one": a wrong-but-plausible resolution is the confident-but-
// wrong outcome that must never ship, and a dead link is the correct
// behaviour.
func (r *Registry) Resolve(p PID) Resolution {
	if r == nil {
		return Resolution{Outcome: OutcomeUndetermined,
			Err: errors.New("passage: no registry")}
	}
	if r.unavailable != nil {
		return Resolution{Outcome: OutcomeUndetermined, Err: r.unavailable}
	}
	rec, ok := r.rows[p]
	if !ok {
		return Resolution{Outcome: OutcomeNotInRegistry}
	}
	if rec.Redacted {
		return Resolution{Outcome: OutcomeRedacted, Record: rec.Public()}
	}
	return Resolution{Outcome: OutcomeFound, Record: rec}
}

// Put inserts or updates a row.
//
// A pid is never reused, never reassigned and never recycled (M3), so Put
// refuses to point an existing pid at a passage in a different file or at a
// different symbol. Updating a row in place — new text, new line numbers — is
// the normal case and is allowed.
func (r *Registry) Put(rec Record) error {
	if r == nil {
		return errors.New("passage: nil registry")
	}
	if r.unavailable != nil {
		return fmt.Errorf("passage: registry unavailable: %w", r.unavailable)
	}
	if err := r.validate(rec); err != nil {
		return err
	}
	if prev, ok := r.rows[rec.PID]; ok {
		if prev.SourceRef.Path != rec.SourceRef.Path {
			return fmt.Errorf("passage: pid %s is already bound to %s; it may not be "+
				"reassigned to %s (M3)", rec.PID, prev.SourceRef.Path, rec.SourceRef.Path)
		}
		if prev.MachineText != "" && rec.MachineText != prev.MachineText {
			return fmt.Errorf("passage: pid %s: machine_text is immutable (F1)", rec.PID)
		}
	}
	r.rows[rec.PID] = rec
	return nil
}

// ---------------------------------------------------------------------------
// passages.jsonl — the source of truth
// ---------------------------------------------------------------------------

// LoadJSONL reads a registry. A read failure returns an error; callers that
// must keep serving should wrap it with Unavailable so resolution reports
// undetermined rather than "not found".
//
// A key this package does not define is REFUSED, not ignored.
//
// That is the opposite of encoding/json's default, and deliberately so.
// passages.jsonl is a source of truth; an unknown key in it is data somebody
// meant to keep, and dropping it silently is a corpus that loses content with
// no error, no exit code and no visible symptom — the same shape of failure the
// whole package exists to argue against. It is also what makes the v0.1 -> v0.2
// break SAFE: a row written when this package still owned a consumer's field
// names stops the load and says which key, rather than loading successfully
// minus whatever those fields carried.
//
// This rule is stated generically and needs no list of retired names. The
// offending key is reported from the DATA, which is both more useful to whoever
// has to fix the file and the reason this package does not have to write a
// consumer's vocabulary into its own source in order to reject it.
//
// Options are applied to the registry being built, so a consumer's record
// validator runs over every loaded row rather than only over rows it writes.
func LoadJSONL(path string, opts ...RegistryOption) (*Registry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	reg := NewRegistry(opts...)
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.DisallowUnknownFields()
		var rec Record
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("%s:%d: %w; a key this package does not define is refused "+
				"rather than dropped — carry consumer-owned data in `attrs`, `scope` or "+
				"`scope_order`, which are stored verbatim and never interpreted",
				path, i+1, err)
		}
		if err := reg.Put(rec); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
	}
	return reg, nil
}

// WriteJSONL writes the registry: UTF-8, LF, final newline, one compact record
// per line, fixed key order, sorted ascending by pid, no trailing whitespace.
//
// Re-writing an unchanged registry produces a BYTE-IDENTICAL file. That is the
// mechanical test for idempotent re-ingest, and it is why a `git diff` of one
// changed passage is one changed line.
func (r *Registry) WriteJSONL(path string) error {
	var sb strings.Builder
	for _, rec := range r.All() {
		b, err := json.Marshal(rec)
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

// ---------------------------------------------------------------------------
// Sync — attaching observed passages to identities
// ---------------------------------------------------------------------------

// Observation is a passage as observed in an artifact during a scan. A
// producer with real structure — one that already knows its own segment
// boundaries — builds these itself; Blocks builds them for plain prose.
type Observation struct {
	// Anchor is the pid read out of the artifact, if any.
	Anchor    PID
	HasAnchor bool

	Kind Kind
	Text string
	Ref  SourceRef

	// Scope, ScopeOrder and Attrs are consumer-owned and are carried through
	// to a newly minted Record verbatim. See the fields of the same name on
	// Record for what this package does and does not do with them.
	Scope      *string
	ScopeOrder *float64
	Attrs      map[string]string

	Confidence *float64
}

// KeyStrategy decides how an Observation attaches to an existing row.
//
// The interface carries an unexported method, so only this package can
// implement it. Production callers get AnchorKey and nothing else. The
// paired mutation proofs implement deliberately wrong strategies and inject
// them into the REAL Sync path — the proof runs the real entry point rather
// than a copy of it.
type KeyStrategy interface {
	Name() string
	// Match returns the pid an observation attaches to. A non-nil error
	// aborts the sync.
	Match(r *Registry, obs Observation) (PID, bool, error)
	keyStrategy()
}

type anchorKey struct{}

func (anchorKey) Name() string { return "anchor" }
func (anchorKey) keyStrategy() {}

// Match implements the ingest matching algorithm's anchor rules.
//
//	R1a  anchor present and in the registry -> attach. Authoritative.
//	R1c  anchor present and NOT in the registry -> ERROR. Do not mint, do not
//	     adopt. A copy-pasted block must not hijack an identity, and an unknown
//	     anchor may mean the registry was rolled back — an operator must see it.
//	R5   no anchor -> no match, so Sync mints. Minting is the LAST branch.
func (anchorKey) Match(r *Registry, obs Observation) (PID, bool, error) {
	if !obs.HasAnchor {
		return "", false, nil
	}
	if _, ok := r.Lookup(obs.Anchor); ok {
		return obs.Anchor, true, nil
	}
	return "", false, fmt.Errorf("passage: %s:%d carries anchor %s, which is not in the "+
		"registry; refusing to mint or adopt it (foreign anchor)",
		obs.Ref.Path, obs.Ref.LineStart, obs.Anchor)
}

// AnchorKey is the only correct key strategy: identity travels with the text
// in the artifact, and position is re-derived rather than relied upon.
func AnchorKey() KeyStrategy { return anchorKey{} }

type syncConfig struct {
	strategy KeyStrategy
	run      *string
	now      func() time.Time
}

// SyncOption configures a sync.
type SyncOption func(*syncConfig)

// WithKeyStrategy replaces the attachment strategy. Production code should
// never pass anything but AnchorKey; the option exists so the survival gates
// can inject a broken strategy into the real code path and prove they catch it.
func WithKeyStrategy(k KeyStrategy) SyncOption {
	return func(c *syncConfig) { c.strategy = k }
}

// WithIngestRun records a run id on every row the sync touches.
func WithIngestRun(id string) SyncOption {
	return func(c *syncConfig) { c.run = &id }
}

// WithNow replaces the clock used for minted_at.
func WithNow(fn func() time.Time) SyncOption {
	return func(c *syncConfig) { c.now = fn }
}

// SyncResult reports what a sync did. Minted being empty on a re-run over
// unchanged input is the mechanical test for idempotency.
type SyncResult struct {
	Minted    []PID
	Attached  []PID
	Corrected []PID
	Unchanged []PID

	// Assigned holds the pid each observation ended up with, in the order the
	// observations were supplied. Callers that need to write anchors back read
	// it rather than re-deriving the assignment — re-deriving would have to
	// re-run the matching, and under a mutated strategy the two answers can
	// differ, which is exactly how a write-back lands the wrong pid on a block.
	Assigned []PID
}

// Sync attaches observations to identities, minting only where the strategy
// fails to attach.
//
// Minting is the LAST branch, never the first. Re-running over unchanged input
// mints zero pids — idempotency by construction, not by luck.
func (r *Registry) Sync(obs []Observation, m *Minter, opts ...SyncOption) (SyncResult, error) {
	cfg := syncConfig{strategy: AnchorKey(), now: time.Now}
	for _, o := range opts {
		o(&cfg)
	}
	if r == nil || r.unavailable != nil {
		return SyncResult{}, fmt.Errorf("passage: registry unavailable")
	}
	if m == nil {
		return SyncResult{}, errors.New("passage: no minter")
	}

	var res SyncResult
	for _, o := range obs {
		pid, matched, err := cfg.strategy.Match(r, o)
		if err != nil {
			return res, err
		}
		if matched {
			rec, _ := r.Lookup(pid)
			hash := ContentHash(o.Text)
			changed := rec.Text != o.Text
			// source_ref is a cache: always re-derived from where the passage
			// was actually found. This is guarantee G2's mechanism.
			rec.SourceRef = o.Ref
			rec.IngestRun = cfg.run
			if changed {
				rec.Text = o.Text
				rec.ContentHash = hash
				// The machine layer is evidence and is never overwritten.
				if rec.MachineText != "" {
					rec.Provenance = ProvenanceHumanCorrected
				}
				res.Corrected = append(res.Corrected, pid)
			} else {
				res.Unchanged = append(res.Unchanged, pid)
			}
			if err := r.putForce(rec); err != nil {
				return res, err
			}
			res.Attached = append(res.Attached, pid)
			res.Assigned = append(res.Assigned, pid)
			continue
		}

		// M4: minting is reached only when attachment failed.
		newPID, err := m.Mint()
		if err != nil {
			return res, err
		}
		rec := Record{
			PID:         newPID,
			Kind:        o.Kind,
			Scope:       o.Scope,
			ScopeOrder:  o.ScopeOrder,
			SourceRef:   o.Ref,
			Text:        o.Text,
			MachineText: o.Text,
			Provenance:  ProvenanceMachine,
			Confidence:  o.Confidence,
			Attrs:       o.Attrs,
			ContentHash: ContentHash(o.Text),
			MintedAt:    cfg.now().UTC().Format(time.RFC3339),
			IngestRun:   cfg.run,
		}
		if err := r.Put(rec); err != nil {
			return res, err
		}
		res.Minted = append(res.Minted, newPID)
		res.Assigned = append(res.Assigned, newPID)
	}
	return res, nil
}

// putForce updates an existing row, bypassing the machine_text immutability
// check that Put applies to externally supplied records. Sync preserves
// MachineText itself; this exists so an updated SourceRef is not rejected.
func (r *Registry) putForce(rec Record) error {
	if err := r.validate(rec); err != nil {
		return err
	}
	r.rows[rec.PID] = rec
	return nil
}

// SyncFile is the file-level entry point: read the artifact, attach or mint,
// write anchors back for newly minted pids, and re-derive every source_ref
// from the file as it now stands.
//
// The kind is SUPPLIED BY THE CALLER and is never inferred. An earlier version
// guessed it from the file extension, which meant this package decided what a
// consumer's `.mmd` file *is* — a classification it has no way to know and no
// business making. Passing it in is one more argument and one less guess.
//
// The file is rewritten ONLY when an anchor had to be added or corrected, so a
// second run over unchanged input leaves every source file byte-identical.
func (r *Registry) SyncFile(path string, kind Kind, m *Minter, opts ...SyncOption) (SyncResult, error) {
	syn, ok := SyntaxForPath(path)
	if !ok {
		return SyncResult{}, fmt.Errorf("passage: %s has no inline anchor syntax; "+
			"use Sync with sidecar or registry-only observations", path)
	}
	if !kind.Valid() {
		return SyncResult{}, fmt.Errorf("passage: %q is not a well-formed kind token; "+
			"SyncFile does not infer one", kind)
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return SyncResult{}, err
	}
	obs, err := Blocks(src, syn, path, kind)
	if err != nil {
		return SyncResult{}, err
	}
	res, err := r.Sync(obs, m, opts...)
	if err != nil {
		return res, err
	}

	// Write every block's anchor back, then re-derive positions from the file
	// AS WRITTEN. Without the refresh, the recorded line numbers would
	// describe the file BEFORE the anchors were inserted — a source_ref that
	// is stale the moment it is stored.
	if len(res.Assigned) != len(obs) {
		return res, fmt.Errorf("passage: internal: %d observations produced %d assignments",
			len(obs), len(res.Assigned))
	}
	assigned := make([]assignment, 0, len(obs))
	for i, o := range obs {
		assigned = append(assigned, assignment{res.Assigned[i], o.Ref})
	}
	out, changed := writeAnchors(src, syn, assigned)
	if !changed {
		return res, nil
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return res, err
	}
	// The refresh is a direct anchor->position update, NOT another matching
	// pass. Every block now carries the anchor this sync assigned it, so the
	// mapping is a lookup with no strategy involved and nothing to re-mint.
	if err := r.refreshPositions(out, syn, path, kind); err != nil {
		return res, err
	}
	return res, nil
}

// refreshPositions re-derives every anchored block's source_ref from the file
// as it now stands on disk. The kind is passed through only because Blocks
// requires one; nothing here reads it.
func (r *Registry) refreshPositions(src []byte, syn Syntax, path string, kind Kind) error {
	obs, err := Blocks(src, syn, path, kind)
	if err != nil {
		return err
	}
	for _, o := range obs {
		if !o.HasAnchor {
			continue
		}
		rec, ok := r.Lookup(o.Anchor)
		if !ok {
			continue
		}
		rec.SourceRef = o.Ref
		if err := r.putForce(rec); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// passages.db — derived, rebuildable, never authoritative
// ---------------------------------------------------------------------------

// schemaVersion is stamped into the file's PRAGMA user_version.
//
//	1  the original schema. It named one consuming application's subject
//	   matter in four places: two closed CHECK vocabularies, two columns and
//	   one index.
//	2  those five are gone. Classification and grouping are now the generic
//	   `kind`, `scope` and `scope_order` columns with no CHECK vocabulary
//	   between them, everything else consumer-owned lives in the opaque
//	   `attrs` JSON column, and the grouping index is `passages_by_scope`.
//
// The v1 names are NOT recited here. Reciting them would put the vocabulary
// back into this module's source to document its own removal, and the guard
// family in decoupling_test.go would be right to fail on it. They belong in
// the release note, which is written for whoever has to migrate, and the
// runtime errors in ensureSchema and LoadJSONL name the offending object or
// key from the FILE — which is the copy that matters.
const schemaVersion = 2

// schemaSQL is the normative schema.
//
// Two properties are load-bearing and both are asserted BEHAVIOURALLY rather
// than by grepping this text, because a grep stays green when a migration adds
// the constraint elsewhere:
//
//   - content_hash appears in no primary key, no unique index and no foreign
//     key — proven by storing two passages that share one.
//   - `kind` and `crossrefs.relation` carry NO closed CHECK vocabulary — proven
//     by storing a kind this package has never heard of. A CHECK listing
//     members would be this library enumerating its consumer's domain in DDL,
//     which is the same defect as an enum in Go with extra steps.
//
// `attrs` holds a consumer-owned JSON object. This package writes it, reads it
// back and indexes nothing inside it.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS passages (
  pid              TEXT PRIMARY KEY,
  kind             TEXT NOT NULL,
  scope            TEXT,
  scope_order      REAL,
  path             TEXT NOT NULL,
  line_start       INTEGER, line_end INTEGER,
  symbol           TEXT,
  anchor           TEXT NOT NULL CHECK (anchor IN ('inline','sidecar','registry_only')),
  text             TEXT,
  machine_text     TEXT,
  provenance       TEXT NOT NULL CHECK (provenance IN ('machine','human_corrected')),
  confidence       REAL,
  uncertain        INTEGER NOT NULL DEFAULT 0,
  uncertain_reason TEXT,
  attrs            TEXT,
  redacted         INTEGER NOT NULL DEFAULT 0,
  content_hash     TEXT NOT NULL,
  minted_at        TEXT NOT NULL,
  ingest_run       TEXT
);
CREATE INDEX IF NOT EXISTS passages_by_scope ON passages(scope, scope_order);
CREATE INDEX IF NOT EXISTS passages_by_path    ON passages(path, line_start);
CREATE UNIQUE INDEX IF NOT EXISTS passages_symbol_key ON passages(path, symbol) WHERE symbol IS NOT NULL;

CREATE TABLE IF NOT EXISTS corrections (
  seq     INTEGER PRIMARY KEY AUTOINCREMENT,
  pid     TEXT NOT NULL REFERENCES passages(pid),
  text    TEXT NOT NULL,
  author  TEXT NOT NULL,
  at      TEXT NOT NULL,
  reason  TEXT
);
CREATE INDEX IF NOT EXISTS corrections_by_pid ON corrections(pid, seq);

CREATE TABLE IF NOT EXISTS redactions (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  pid         TEXT NOT NULL REFERENCES passages(pid),
  action      TEXT NOT NULL CHECK (action IN ('redact','unredact')),
  reason_code TEXT NOT NULL,
  by          TEXT NOT NULL,
  at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS crossrefs (
  from_pid   TEXT NOT NULL REFERENCES passages(pid),
  to_pid     TEXT NOT NULL REFERENCES passages(pid),
  relation   TEXT NOT NULL,
  origin     TEXT NOT NULL CHECK (origin IN ('derived','authored')),
  score      REAL,
  generation INTEGER,
  PRIMARY KEY (from_pid, to_pid, relation, origin),
  CHECK (from_pid <> to_pid)
);

CREATE TABLE IF NOT EXISTS symbol_aliases (
  old_path TEXT NOT NULL, old_symbol TEXT NOT NULL,
  new_path TEXT NOT NULL, new_symbol TEXT NOT NULL,
  pid      TEXT NOT NULL REFERENCES passages(pid),
  origin   TEXT NOT NULL CHECK (origin IN ('detected','authored')),
  at       TEXT NOT NULL,
  PRIMARY KEY (old_path, old_symbol, new_path, new_symbol)
);

CREATE TABLE IF NOT EXISTS generations (
  generation INTEGER PRIMARY KEY,
  state      TEXT NOT NULL CHECK (state IN ('building','verified','live','superseded','discarded')),
  pid_count  INTEGER NOT NULL,
  built_at   TEXT NOT NULL,
  root_hash  TEXT
);
CREATE TABLE IF NOT EXISTS generation_members (
  generation INTEGER NOT NULL REFERENCES generations(generation),
  pid        TEXT NOT NULL REFERENCES passages(pid),
  PRIMARY KEY (generation, pid)
);

CREATE VIRTUAL TABLE IF NOT EXISTS passages_fts USING fts5(
  pid UNINDEXED, title, body, symbol,
  prefix = '2 3 4',
  tokenize = 'unicode61 remove_diacritics 2'
);
`

// BuildDB materialises the derived database from the registry.
//
// The database is a CACHE. It is rebuilt wholesale rather than migrated, so a
// rebuild is always safe to repeat and the JSONL always wins a disagreement.
//
// Redaction propagates here, not merely to a rendered view: a redacted row
// stores no text and no machine_text, and is excluded from the search index
// entirely, so it cannot be returned by search.
func (r *Registry) BuildDB(ctx context.Context, path string) error {
	if r == nil || r.unavailable != nil {
		return errors.New("passage: registry unavailable")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
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

	for _, stmt := range []string{`DELETE FROM passages_fts`, `DELETE FROM passages`} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	ins, err := tx.PrepareContext(ctx, `INSERT INTO passages
	  (pid,kind,scope,scope_order,path,line_start,line_end,symbol,anchor,text,machine_text,
	   provenance,confidence,uncertain,uncertain_reason,attrs,
	   redacted,content_hash,minted_at,ingest_run)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	fts, err := tx.PrepareContext(ctx, `INSERT INTO passages_fts (pid,title,body,symbol) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer fts.Close()

	for _, rec := range r.All() {
		pub := rec.Public() // F6: a redacted row stores no text at any layer
		var text, machine any
		if pub.Text != "" {
			text = pub.Text
		}
		if pub.MachineText != "" {
			machine = pub.MachineText
		}
		var attrs any
		if len(rec.Attrs) > 0 {
			// encoding/json sorts map keys, so the stored blob is stable
			// across rebuilds of the same registry.
			b, err := json.Marshal(rec.Attrs)
			if err != nil {
				return fmt.Errorf("passage: encoding attrs for %s: %w", rec.PID, err)
			}
			attrs = string(b)
		}
		if _, err := ins.ExecContext(ctx,
			string(rec.PID), string(rec.Kind), rec.Scope, rec.ScopeOrder, rec.SourceRef.Path,
			rec.SourceRef.LineStart, rec.SourceRef.LineEnd, rec.SourceRef.Symbol,
			string(rec.SourceRef.Anchor), text, machine, string(rec.Provenance),
			rec.Confidence, boolInt(rec.Uncertain), rec.UncertainReason, attrs,
			boolInt(rec.Redacted), rec.ContentHash, rec.MintedAt, rec.IngestRun,
		); err != nil {
			return fmt.Errorf("passage: inserting %s: %w", rec.PID, err)
		}
		if rec.Redacted {
			continue // R1: unreachable through search
		}
		if _, err := fts.ExecContext(ctx, string(rec.PID), "", rec.Text, rec.SourceRef.Symbol); err != nil {
			return fmt.Errorf("passage: indexing %s: %w", rec.PID, err)
		}
	}
	return tx.Commit()
}

// ensureSchema creates the schema in a fresh file, accepts a file already at
// the current version, and REFUSES anything else.
//
// The refusal is deliberate and is the honest half of the clean break. This
// database is a derived cache and is rebuilt wholesale rather than migrated,
// so the tempting move is to drop and recreate whatever is there. But the file
// also declares tables this package never writes — corrections, redactions,
// crossrefs, symbol_aliases, generations — and dropping a consumer's rows out
// of them to satisfy a schema change would be destroying data on a guess.
//
// This package cannot determine whether that is safe, so it does not decide.
// It stops, names the file, names the versions, and names the remedy: delete
// the derived database and rebuild it from passages.jsonl, which is the source
// of truth and always wins a disagreement.
func ensureSchema(ctx context.Context, db *sql.DB, path string) error {
	var have int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&have); err != nil {
		return fmt.Errorf("passage: reading schema version of %s: %w", path, err)
	}
	switch {
	case have == schemaVersion:
		// Already current. Re-running the CREATE ... IF NOT EXISTS statements
		// is a no-op and keeps a partially built file honest.
	case have == 0:
		// Either a brand-new file or one written before versions were
		// stamped. The two are told apart by whether the table is already
		// there — an unstamped file that already holds `passages` is a v1
		// file, not a fresh one, and its columns do not match.
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='passages'`,
		).Scan(&n); err != nil {
			return fmt.Errorf("passage: inspecting %s: %w", path, err)
		}
		if n > 0 {
			return fmt.Errorf("passage: %s carries an unstamped `passages` table, so it was "+
				"built by schema v1 (before PRAGMA user_version was set); this package now "+
				"writes v%d and the columns differ. The derived database is a cache: delete "+
				"it and rebuild from passages.jsonl. Refusing to drop tables this package "+
				"does not write", path, schemaVersion)
		}
	default:
		return fmt.Errorf("passage: %s is at schema v%d and this package writes v%d; "+
			"the derived database is a cache: delete it and rebuild from passages.jsonl. "+
			"Refusing to drop tables this package does not write", path, have, schemaVersion)
	}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("passage: creating schema: %w", err)
	}
	// PRAGMA does not accept a bound parameter; schemaVersion is an untainted
	// package constant.
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return fmt.Errorf("passage: stamping schema version on %s: %w", path, err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
