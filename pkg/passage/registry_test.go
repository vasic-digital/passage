package passage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vasic-digital/verdict/pkg/verdict"
)

func minimalRecord(pid PID, text string) Record {
	return Record{
		PID:  pid,
		Kind: KindDocSection,
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
	want := []string{"pid", "kind", "chapter_slug", "source_ref", "text", "machine_text",
		"provenance", "confidence", "uncertain", "uncertain_reason", "t_start_s", "t_end_s",
		"speaker", "speaker_source", "redacted", "content_hash", "minted_at", "ingest_run"}
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
	f := 0.0
	cases := []struct {
		name  string
		mutex func(*Record)
	}{
		{"F3 uncertain without a reason", func(r *Record) { r.Uncertain = true }},
		{"F3 reason without uncertain", func(r *Record) { s := "inaudible"; r.UncertainReason = &s }},
		{"F4 machine-attributed speaker", func(r *Record) {
			s := "Speaker A"
			r.Speaker = &s
			src := SpeakerSource("machine")
			r.SpeakerSource = &src
		}},
		{"F4 speaker with no source", func(r *Record) { s := "Speaker A"; r.Speaker = &s }},
		{"F5 transcript without timestamps", func(r *Record) { r.Kind = KindTranscript }},
		{"F5 t_end before t_start", func(r *Record) {
			r.Kind = KindTranscript
			hi, lo := 9.0, 1.0
			r.TStartS, r.TEndS = &hi, &lo
		}},
		{"F2 human_corrected without machine_text", func(r *Record) {
			r.Provenance = ProvenanceHumanCorrected
			r.MachineText = ""
		}},
		{"F7 missing content_hash", func(r *Record) { r.ContentHash = "" }},
		{"invalid pid", func(r *Record) { r.PID = "not-a-ulid" }},
		{"unknown kind", func(r *Record) { r.Kind = "video" }},
		{"non-transcript with a timestamp", func(r *Record) { r.TStartS = &f }},
	}
	for _, c := range cases {
		rec := base
		c.mutex(&rec)
		if err := rec.Validate(); err == nil {
			t.Errorf("%s: accepted", c.name)
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
	same := "verbatim repetition happens in a spoken transcript"
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
