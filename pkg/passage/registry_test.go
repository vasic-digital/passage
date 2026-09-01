package passage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vasic-digital/verdict/pkg/verdict"
)

func minimalRecord(pid PID, text string) Record {
	return Record{
		PID:  pid,
		Kind: fixtureKind,
		SourceRef: SourceRef{
			Path: "corpus/x.md", LineStart: 2, LineEnd: 2, Anchor: AnchorInline,
		},
		Text:        text,
		MachineText: text,
		Provenance:  ProvenanceMachine,
		ContentHash: ContentHash(text),
		MintedAt:    "2026-09-01T09:12:44Z",
	}
}

// ---------------------------------------------------------------------------
// content_hash definition (§5.3)
// ---------------------------------------------------------------------------

func TestContentHashDefinition(t *testing.T) {
	h := ContentHash("hello")
	if !strings.HasPrefix(h, "sha256:") {
		t.Errorf("ContentHash = %q, want a sha256: prefix", h)
	}
	if len(h) != len("sha256:")+64 {
		t.Errorf("ContentHash = %q, want 64 lowercase hex digits", h)
	}
	if strings.ToLower(h) != h {
		t.Errorf("ContentHash = %q, want lowercase hex", h)
	}
	// Trailing whitespace is stripped per line, lines are LF-joined.
	if ContentHash("a  \nb\t\n") != ContentHash("a\nb\n") {
		t.Error("trailing whitespace changed the hash")
	}
	if ContentHash("a\r\nb") != ContentHash("a\nb") {
		t.Error("CRLF and LF hashed differently")
	}
	// NFC normalisation: composed and decomposed forms of the same text are
	// the same text.
	if ContentHash("café") != ContentHash("café") {
		t.Error("NFC-equivalent strings hashed differently")
	}
	if ContentHash("a") == ContentHash("b") {
		t.Error("different text hashed identically")
	}
}

// ---------------------------------------------------------------------------
// JSONL: the source of truth a human reads in a pull request (§4.1)
// ---------------------------------------------------------------------------

func TestJSONLIsSortedCompactAndByteStable(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	// Deliberately inserted out of order.
	for _, p := range []PID{"01JBX7QK3M8V2ZC4YT5N6RWDPC", "01JBX7QK3M8V2ZC4YT5N6RWDPA", "01JBX7QK3M8V2ZC4YT5N6RWDPB"} {
		if err := reg.Put(minimalRecord(p, "text for "+string(p))); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "passages.jsonl")
	if err := reg.WriteJSONL(path); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Error("no final newline")
	}
	if strings.Contains(string(b), "\r") {
		t.Error("CR found; line endings must be LF")
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines, want 3", len(lines))
	}
	for i := 1; i < len(lines); i++ {
		if lines[i-1] >= lines[i] {
			t.Errorf("records are not sorted ascending by pid at line %d", i+1)
		}
	}
	// Fixed key order (§4.1) — pid first, content_hash present and named.
	if !strings.HasPrefix(lines[0], `{"pid":`) {
		t.Errorf("first key is not pid: %s", lines[0])
	}
	if !strings.Contains(lines[0], `"content_hash":`) {
		t.Errorf("content_hash is absent from the serialisation: %s", lines[0])
	}
	// Round trip, then rewrite: byte-identical (FR-027's mechanical test).
	back, err := LoadJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	again := filepath.Join(dir, "again.jsonl")
	if err := back.WriteJSONL(again); err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(b2) {
		t.Errorf("re-writing a loaded registry is not byte-identical:\n%s\n---\n%s", b, b2)
	}
}

func TestRecordKeyOrderIsFixed(t *testing.T) {
	b, err := json.Marshal(minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"pid", "kind", "scope", "scope_order", "source_ref", "text", "machine_text",
		"provenance", "confidence", "uncertain", "uncertain_reason", "attrs",
		"redacted", "content_hash", "minted_at", "ingest_run"}
	at := 0
	for _, k := range want {
		idx := strings.Index(string(b), `"`+k+`":`)
		if idx < 0 {
			t.Fatalf("key %q missing from %s", k, b)
		}
		if idx < at {
			t.Fatalf("key %q is out of contract order in %s", k, b)
		}
		at = idx
	}
}

// ---------------------------------------------------------------------------
// Field invariants (§4.2 F1-F7)
// ---------------------------------------------------------------------------

func TestRecordValidationEnforcesTheFieldInvariants(t *testing.T) {
	base := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")
	if err := base.Validate(); err != nil {
		t.Fatalf("a minimal valid record was rejected: %v", err)
	}
	cases := []struct {
		name  string
		mutex func(*Record)
	}{
		{"F3 uncertain without a reason", func(r *Record) { r.Uncertain = true }},
		{"F3 reason without uncertain", func(r *Record) { s := "unresolved"; r.UncertainReason = &s }},
		{"F2 human_corrected without machine_text", func(r *Record) {
			r.Provenance = ProvenanceHumanCorrected
			r.MachineText = ""
		}},
		{"F7 missing content_hash", func(r *Record) { r.ContentHash = "" }},
		{"invalid pid", func(r *Record) { r.PID = "not-a-ulid" }},
		{"empty kind", func(r *Record) { r.Kind = "" }},
		{"malformed kind: uppercase", func(r *Record) { r.Kind = "ProseBlock" }},
		{"malformed kind: leading digit", func(r *Record) { r.Kind = "1st_block" }},
		{"malformed kind: punctuation", func(r *Record) { r.Kind = "prose-block" }},
		{"attrs with an empty key", func(r *Record) { r.Attrs = map[string]string{"": "x"} }},
		{"attrs colliding with a reserved key", func(r *Record) {
			r.Attrs = map[string]string{"content_hash": "sha256:deadbeef"}
		}},
	}
	for _, c := range cases {
		rec := base
		c.mutex(&rec)
		if err := rec.Validate(); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// The counterpart: a kind this package has never heard of is ACCEPTED,
	// because the vocabulary belongs to the consumer.
	for _, k := range []Kind{"prose_block", "clause", "utterance", "commit_message", "q7"} {
		rec := base
		rec.Kind = k
		if err := rec.Validate(); err != nil {
			t.Errorf("a consumer-defined kind %q was rejected: %v", k, err)
		}
	}
}

// The seam that replaces the domain rules this package used to hardcode. A
// consumer installs its own invariants at the same choke point; they run on
// every write, and they are the CONSUMER's, so a different consumer is not
// bound by them.
func TestAConsumerRecordValidatorRunsOnEveryWritePath(t *testing.T) {
	// A rule that could not live in this package: it is true of one consumer's
	// domain and meaningless in another's.
	const myKind Kind = "timed_segment"
	domain := func(r Record) error {
		if r.Kind != myKind {
			return nil
		}
		if r.Attrs["t_start_s"] == "" || r.Attrs["t_end_s"] == "" {
			return fmt.Errorf("kind %q requires t_start_s and t_end_s in attrs", myKind)
		}
		return nil
	}
	reg := NewRegistry(WithRecordValidator(domain))

	bad := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")
	bad.Kind = myKind
	if err := reg.Put(bad); err == nil {
		t.Fatal("the consumer's record validator did not run on Put")
	}
	bad.Attrs = map[string]string{"t_start_s": "1", "t_end_s": "2"}
	if err := reg.Put(bad); err != nil {
		t.Fatalf("a record satisfying the consumer's rule was rejected: %v", err)
	}
	// It also runs on the Sync path, which reaches putForce rather than Put.
	obs := []Observation{{
		Kind: myKind,
		Text: "a segment with no offsets",
		Ref:  SourceRef{Path: "corpus/x.md", LineStart: 9, LineEnd: 9, Anchor: AnchorInline},
	}}
	if _, err := reg.Sync(obs, newTestMinter()); err == nil {
		t.Fatal("the consumer's record validator did not run on Sync")
	}
	// And a registry WITHOUT the validator accepts the same record, proving
	// the rule is the consumer's rather than this package's.
	if err := NewRegistry().Put(func() Record {
		r := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")
		r.Kind = myKind
		return r
	}()); err != nil {
		t.Fatalf("a registry with no consumer validator rejected the record: %v", err)
	}
}

// Scope and ScopeOrder are stored, indexed and returned without this package
// having any opinion about what they mean.
func TestScopeAndAttrsRoundTripAsOpaqueConsumerData(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	scope := "any-grouping-key-the-consumer-likes"
	order := 512.4
	rec := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")
	rec.Scope = &scope
	rec.ScopeOrder = &order
	rec.Attrs = map[string]string{"zeta": "last", "alpha": "first"}
	if err := reg.Put(rec); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "passages.jsonl")
	if err := reg.WriteJSONL(path); err != nil {
		t.Fatal(err)
	}
	back, err := LoadJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := back.Lookup(rec.PID)
	if !ok {
		t.Fatal("the record did not survive a round trip")
	}
	if got.Scope == nil || *got.Scope != scope {
		t.Errorf("scope = %v, want %q", got.Scope, scope)
	}
	if got.ScopeOrder == nil || *got.ScopeOrder != order {
		t.Errorf("scope_order = %v, want %v", got.ScopeOrder, order)
	}
	if got.Attrs["alpha"] != "first" || got.Attrs["zeta"] != "last" {
		t.Errorf("attrs = %v", got.Attrs)
	}
	// Byte-identical re-write, which is why Attrs is map[string]string and not
	// map[string]any: a JSON number would round-trip through float64 and could
	// come back formatted differently.
	again := filepath.Join(dir, "again.jsonl")
	if err := back.WriteJSONL(again); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("a registry carrying attrs did not re-write byte-identically:\n%s\n---\n%s", a, b)
	}
}

// Composition with a retrieval document of the shape
// {ID, Content, Metadata, Score, Source} — met, not imported, not duplicated.
func TestMetadataComposesWithARetrievalDocument(t *testing.T) {
	scope := "group-7"
	order := 512.4
	rec := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "the passage body")
	rec.Scope = &scope
	rec.ScopeOrder = &order
	// A consumer's own attribute, under a name only the consumer knows. The
	// name is DERIVED from the decoupling gate's dictionary rather than
	// written out, so this test can demonstrate that a consumer-shaped key
	// travels as data without this module's source speaking the word. The
	// real thing is demonstrated in the consumer, where the word belongs.
	consumerKey := consumerVocabulary[0] + "_slug"
	rec.Attrs = map[string]string{consumerKey: "01-first"}

	// The four-line mapping a consumer writes. The field NAMES here are the
	// retrieval document's; the values are all this package supplies.
	id := string(rec.PID)
	content := rec.Public().Text
	metadata := rec.Metadata()
	source := rec.SourceRef.Path

	if !ValidPID(id) {
		t.Errorf("the document id %q is not a pid", id)
	}
	if content != "the passage body" {
		t.Errorf("content = %q", content)
	}
	if source != "corpus/x.md" {
		t.Errorf("source = %q", source)
	}
	if metadata[consumerKey] != "01-first" {
		t.Errorf("the consumer's own attribute did not survive: %v", metadata)
	}
	if metadata["scope"] != scope || metadata["scope_order"] != order {
		t.Errorf("metadata lost scope/scope_order: %v", metadata)
	}
	if metadata["kind"] != string(fixtureKind) || metadata["content_hash"] != rec.ContentHash {
		t.Errorf("metadata lost a library-owned key: %v", metadata)
	}
	// A redacted passage contributes no text to the document (F6).
	rec.Redacted = true
	if rec.Public().Text != "" {
		t.Error("F6 VIOLATED: a redacted record would hand text to a retrieval document")
	}
}

// A line carrying a key this package does not define must FAIL LOUDLY rather
// than load with that key silently dropped by encoding/json.
//
// This is what makes the v0.1 -> v0.2 break safe. Every field this package
// retired when the consumer-shaped vocabulary left is, by construction, a key
// it no longer defines, so a pre-v0.2 row stops the load instead of loading
// successfully minus its contents. The seeds below are DERIVED from the
// decoupling gate's dictionary rather than written out, so proving the rule
// does not require this module's source to speak the words it is refusing.
func TestLoadJSONLRefusesAKeyItDoesNotDefine(t *testing.T) {
	dir := t.TempDir()
	base, err := json.Marshal(minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x"))
	if err != nil {
		t.Fatal(err)
	}
	// CONTROL: the same line with nothing extra loads.
	ok := filepath.Join(dir, "ok.jsonl")
	if err := os.WriteFile(ok, append(base, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJSONL(ok); err != nil {
		t.Fatalf("CONTROL FAILED: a current line did not load: %v", err)
	}

	seeds := []string{"a_key_this_package_does_not_define"}
	for _, tok := range consumerVocabulary {
		// For the first entry this composes the exact field this module used
		// to ship, without the literal appearing anywhere in its source.
		seeds = append(seeds, tok+"_slug")
	}
	for i, k := range seeds {
		var m map[string]any
		if err := json.Unmarshal(base, &m); err != nil {
			t.Fatal(err)
		}
		m[k] = "a value somebody meant to keep"
		line, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, fmt.Sprintf("seed%02d.jsonl", i))
		if err := os.WriteFile(p, append(line, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = LoadJSONL(p)
		if err == nil {
			t.Errorf("a line carrying the undefined key %q loaded silently; its value would "+
				"have been dropped with no error and no symptom", k)
			continue
		}
		if !strings.Contains(err.Error(), k) {
			t.Errorf("the refusal for %q does not name the key: %v", k, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Resolution: four outcomes, and undetermined is never collapsed (§7.1)
// ---------------------------------------------------------------------------

func TestResolutionHasFourOutcomesMappedToThreeVerdicts(t *testing.T) {
	reg := NewRegistry()
	live := PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")
	gone := PID("01JBX7QK3M8V2ZC4YT5N6RWDPB")
	if err := reg.Put(minimalRecord(live, "present")); err != nil {
		t.Fatal(err)
	}
	hidden := minimalRecord(gone, "secret")
	hidden.Redacted = true
	if err := reg.Put(hidden); err != nil {
		t.Fatal(err)
	}
	absent := PID("01JBX7QK3M8V2ZC4YT5N6RWDPZ")

	cases := []struct {
		pid    PID
		reg    *Registry
		want   Outcome
		status int
		verd   verdict.Verdict
	}{
		{live, reg, OutcomeFound, 200, verdict.OK},
		{gone, reg, OutcomeRedacted, 410, verdict.OK},
		{absent, reg, OutcomeNotInRegistry, 404, verdict.Problem},
		{live, Unavailable(os.ErrPermission), OutcomeUndetermined, 503, verdict.Undetermined},
		{live, nil, OutcomeUndetermined, 503, verdict.Undetermined},
	}
	for _, c := range cases {
		res := c.reg.Resolve(c.pid)
		if res.Outcome != c.want {
			t.Errorf("Resolve(%s) = %s, want %s", c.pid, res.Outcome, c.want)
		}
		if got := res.Outcome.HTTPStatus(); got != c.status {
			t.Errorf("%s: HTTP %d, want %d", c.want, got, c.status)
		}
		if got := res.Outcome.Verdict(); got != c.verd {
			t.Errorf("%s: verdict %s, want %s", c.want, got, c.verd)
		}
	}
	// The one that gets collapsed in practice.
	if OutcomeUndetermined.Verdict() == OutcomeNotInRegistry.Verdict() {
		t.Fatal("`undetermined` and `not_in_registry` map to the same verdict — an unreadable " +
			"registry would look like a corpus that never contained the passage")
	}
}

// §7.1, normative: resolution NEVER falls back to fuzzy matching. A
// wrong-but-plausible answer is the failure mode; a dead link is correct.
func TestResolutionNeverGuesses(t *testing.T) {
	reg := NewRegistry()
	real := PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")
	if err := reg.Put(minimalRecord(real, "the only passage")); err != nil {
		t.Fatal(err)
	}
	// One character different from a pid that exists.
	near := PID("01JBX7QK3M8V2ZC4YT5N6RWDPB")
	if res := reg.Resolve(near); res.Outcome != OutcomeNotInRegistry {
		t.Fatalf("a near-miss pid resolved %s to %v — resolution must not guess",
			res.Outcome, res.Record)
	}
}

func TestRedactedRecordCarriesNoTextOnTheWire(t *testing.T) {
	rec := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "third-party detail")
	rec.Redacted = true
	b, err := json.Marshal(rec.Public())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "third-party detail") {
		t.Fatalf("F6 VIOLATED: a redacted record serialised its text: %s", b)
	}
	if !strings.Contains(string(b), `"redacted":true`) {
		t.Errorf("the redaction is not visible in the serialisation: %s", b)
	}
}

// ---------------------------------------------------------------------------
// passages.db — derived, rebuildable, never authoritative (§4.3)
// ---------------------------------------------------------------------------

func TestBuildDBMaterialisesAndFTSWorks(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	rows := map[PID]string{
		"01JBX7QK3M8V2ZC4YT5N6RWDPA": "we always cut inside a measured silence",
		"01JBX7QK3M8V2ZC4YT5N6RWDPB": "the receiver verifies the hash before trusting the part",
		"01JBX7QK3M8V2ZC4YT5N6RWDPC": "a passage identifier is minted once",
	}
	for p, txt := range rows {
		if err := reg.Put(minimalRecord(p, txt)); err != nil {
			t.Fatal(err)
		}
	}
	redacted := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPD", "a redacted mention of silence")
	redacted.Redacted = true
	if err := reg.Put(redacted); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(dir, "passages.db")
	if err := reg.BuildDB(context.Background(), dbPath); err != nil {
		t.Fatalf("BuildDB: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM passages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("passages holds %d rows, want 4", n)
	}
	// R1: a redacted passage cannot be returned by search, and F6: it stores
	// no text.
	var text sql.NullString
	if err := db.QueryRow(`SELECT text FROM passages WHERE pid=?`, string(redacted.PID)).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text.Valid {
		t.Errorf("F6 VIOLATED: a redacted row stored text %q", text.String)
	}
	// FTS5 actually works — executed, not asserted from the schema text.
	var hits []string
	rs, err := db.Query(`SELECT pid FROM passages_fts WHERE passages_fts MATCH 'silence'`)
	if err != nil {
		t.Fatalf("FTS5 query failed — the virtual table is not usable: %v", err)
	}
	defer rs.Close()
	for rs.Next() {
		var pid string
		if err := rs.Scan(&pid); err != nil {
			t.Fatal(err)
		}
		hits = append(hits, pid)
	}
	if len(hits) != 1 || hits[0] != "01JBX7QK3M8V2ZC4YT5N6RWDPA" {
		t.Errorf("FTS5 'silence' matched %v; want only the non-redacted passage "+
			"(R1: redacted content must be unreachable through search)", hits)
	}
	// Prefix search, the type-ahead workload.
	if err := db.QueryRow(`SELECT COUNT(*) FROM passages_fts WHERE passages_fts MATCH 'sil*'`).Scan(&n); err != nil {
		t.Fatalf("FTS5 prefix query failed: %v", err)
	}
	if n != 1 {
		t.Errorf("prefix match 'sil*' returned %d rows, want 1", n)
	}
}

// S2: content_hash appears in no primary key, no unique index and no foreign
// key. Asserted BEHAVIOURALLY — two different passages with identical text
// must both be storable. A grep over the schema would stay green if someone
// added the constraint in a migration.
func TestS2_ContentHashCarriesNoUniquenessConstraint(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	same := "verbatim repetition happens in real material"
	a := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", same)
	b := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPB", same)
	b.SourceRef.LineStart, b.SourceRef.LineEnd = 40, 40
	if err := reg.Put(a); err != nil {
		t.Fatal(err)
	}
	if err := reg.Put(b); err != nil {
		t.Fatalf("the registry refused a second passage with identical text: %v", err)
	}
	if a.ContentHash != b.ContentHash {
		t.Fatal("fixture broken: the two records do not share a content_hash")
	}
	if err := reg.BuildDB(context.Background(), filepath.Join(dir, "p.db")); err != nil {
		t.Fatalf("S2 VIOLATED: passages.db rejected two rows sharing a content_hash "+
			"(%v) — content_hash is under a uniqueness constraint somewhere", err)
	}
}

// The database is a cache; the JSONL is the source of truth. Rebuilding must
// be safe to repeat.
func TestBuildDBIsRepeatable(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	if err := reg.Put(minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "p.db")
	for i := 0; i < 3; i++ {
		if err := reg.BuildDB(context.Background(), p); err != nil {
			t.Fatalf("rebuild %d: %v", i, err)
		}
	}
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM passages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("three rebuilds produced %d rows, want 1", n)
	}
}

// M3: a pid is never reused, never reassigned, never recycled.
func TestPutRefusesToReassignAPID(t *testing.T) {
	reg := NewRegistry()
	p := PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")
	if err := reg.Put(minimalRecord(p, "first")); err != nil {
		t.Fatal(err)
	}
	other := minimalRecord(p, "a completely different passage")
	other.SourceRef.Path = "corpus/elsewhere.md"
	if err := reg.Put(other); err == nil {
		t.Fatal("M3 VIOLATED: a pid was reassigned to a passage in a different file")
	}
}
