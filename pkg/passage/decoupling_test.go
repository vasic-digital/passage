package passage

// The decoupling guard family.
//
// # What went wrong, and why the old guard did not see it
//
// This module documented itself as project-not-aware in four places, and for
// two releases its API contradicted every one of them. `Record` carried a
// `ChapterSlug *string` with a `chapter_slug` JSON tag; `Kind` was a CLOSED
// enum whose members were one consumer's content types; the derived schema had
// a `chapter_slug TEXT` column, a `passages_by_chapter` index and a
// `CHECK (kind IN (...))` listing the same members.
//
// One test claimed to assert the reusability: TestNoConsumerShapedDependencies.
// It reads go.mod. Nothing else. It never opened a single source file of the
// package it was vouching for.
//
// The dictionary it used was not the problem — it already listed the exact
// words that were sitting in registry.go. The CORPUS was the problem: the right
// words, applied to the wrong file. And the argument written above it in go.mod
// explains how that felt sufficient at the time:
//
//	"this module does not require any consumer, so no consumer-shaped symbol
//	 can appear here even by accident"
//
// That is a non-sequitur, and it is the interesting half of the defect. A
// module boundary is a compiler-enforced barrier against a consumer's CODE. It
// is no barrier at all against a consumer's VOCABULARY, which needs no import,
// no require line and no build-graph edge — someone types it into a struct
// field. The guard was built to test the barrier that existed rather than the
// claim that was made, and the claim was much larger than the barrier.
//
// So the fix is not a longer word list. It is a wider corpus:
//
//	G-DECOUPLE-1  every .go file in the module — code AND comments — plus
//	              go.mod and README.md, scanned for the shared dictionary.
//	              Comments are in scope deliberately: the original defect was a
//	              doc comment promising one thing while the code beneath it did
//	              another, and one instrument should be able to see both.
//	G-DECOUPLE-2  every field and struct tag of the exported record types,
//	              by reflection, so a field added without touching a comment is
//	              still caught.
//	G-DECOUPLE-3  the package declares NO constant of type Kind, by AST. Exact,
//	              and independent of any word list.
//	G-DECOUPLE-4  the derived database, by PRAGMA table_info and by storing a
//	              kind no vocabulary contains — behavioural, so a CHECK
//	              constraint reintroduced in a migration is still caught.
//
// Each ships with a paired mutation (§1.1) that drives the same code the real
// gate uses and requires it to report a violation.

import (
	"context"
	"database/sql"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// consumerVocabulary is the dictionary of consumer-domain nouns this module
// may not speak. It is shared with TestNoConsumerShapedDependencies so there is
// one list, not two that drift.
//
// Every entry names a concept belonging to some consuming application's subject
// matter. None of them can be defined without reference to a consumer, which is
// exactly the test for membership: if a second consumer with an unrelated
// domain would find the word meaningless or actively misleading in this
// package's API, it belongs here.
//
// Matching is substring, case-insensitive, so "chapter" catches "ChapterSlug"
// and "chapter_slug", and "transcript" catches "transcription".
var consumerVocabulary = []string{
	"chapter",
	"curricul",
	"workshop",
	"lesson",
	"course",
	"syllabus",
	"student",
	"teacher",
	"instructor",
	"transcript",
	"diariz",
	"speaker",
	"recording",
	"podcast",
	"episode",
	"video",
	"audio",
	"diagram",
}

// vocabularyGateExemptions is the complete list of files the source scan skips.
//
// It has exactly one entry, and one entry is the point: this file has to
// contain the dictionary and the seeded mutations, so it cannot scan itself.
// Every other file in the module, test files included, is in scope. The count
// is asserted below so the exemption list cannot quietly grow into the place
// violations go to hide.
var vocabularyGateExemptions = []string{"decoupling_test.go"}

type vocabHit struct {
	File  string
	Line  int
	Token string
	Text  string
}

// scanConsumerVocabulary reports every line of src containing a dictionary
// word. It is the single implementation behind both the real gate and its
// paired mutation, so the mutation proves the instrument the gate actually
// uses.
func scanConsumerVocabulary(name string, src []byte) []vocabHit {
	var hits []vocabHit
	for i, line := range strings.Split(string(src), "\n") {
		low := strings.ToLower(line)
		for _, tok := range consumerVocabulary {
			if strings.Contains(low, tok) {
				hits = append(hits, vocabHit{name, i + 1, tok, strings.TrimSpace(line)})
			}
		}
	}
	return hits
}

// gateCorpus returns every file the source scan covers, as paths relative to
// the module root. It is DERIVED by walking the tree, never a literal list: a
// hardcoded corpus is how a new file arrives unscanned.
func gateCorpus(t *testing.T, root string) []string {
	t.Helper()
	exempt := map[string]bool{}
	for _, e := range vocabularyGateExemptions {
		exempt[e] = true
	}
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if exempt[base] {
			return nil
		}
		if strings.HasSuffix(base, ".go") || base == "go.mod" || base == "README.md" {
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return rerr
			}
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("the gate corpus is empty — the scan would pass by looking at nothing, " +
			"which is the blind-instrument failure this family exists to prevent")
	}
	return out
}

// ---------------------------------------------------------------------------
// G-DECOUPLE-1 — no consumer vocabulary in this module's source.
// ---------------------------------------------------------------------------

func TestNoConsumerShapedVocabularyInSource(t *testing.T) {
	root := moduleRootDir(t)
	if n := len(vocabularyGateExemptions); n != 1 {
		t.Fatalf("the exemption list holds %d entries, want exactly 1 (%q). Every added "+
			"exemption is a place a violation can hide; if a file genuinely needs one, "+
			"that is a review, not an edit", n, vocabularyGateExemptions[0])
	}
	corpus := gateCorpus(t, root)
	var all []vocabHit
	for _, rel := range corpus {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		all = append(all, scanConsumerVocabulary(rel, b)...)
	}
	for _, h := range all {
		t.Errorf("%s:%d speaks a consumer's vocabulary (%q): %s\n"+
			"    This module names no consuming application's subject matter, in code or in "+
			"prose. Express it in this module's own generic terms, or leave it in the "+
			"consumer and carry it through Kind, Scope, ScopeOrder or Attrs.",
			h.File, h.Line, h.Token, h.Text)
	}
	if len(all) == 0 {
		t.Logf("%d files scanned, 0 consumer-vocabulary hits", len(corpus))
	}
}

func TestPairedMutation_TheVocabularyScannerCatchesASeededField(t *testing.T) {
	// CONTROL: a file with nothing consumer-shaped in it produces no hits, so
	// a hit below means the seeded line and not a scanner that fires on
	// anything.
	clean := []byte("package passage\n\ntype Record struct {\n\tScope *string `json:\"scope\"`\n}\n")
	if hits := scanConsumerVocabulary("clean.go", clean); len(hits) != 0 {
		t.Fatalf("CONTROL FAILED: the scanner reported %v on clean source", hits)
	}
	// The mutation: exactly the field this module shipped for two releases,
	// with exactly its JSON tag.
	seeded := []byte("package passage\n\ntype Record struct {\n" +
		"\tChapter" + "Slug *string `json:\"chapter" + "_slug\"`\n}\n")
	hits := scanConsumerVocabulary("seeded.go", seeded)
	if len(hits) == 0 {
		t.Fatal("MUTATION SURVIVED: the scanner did not report a re-added " +
			"consumer-shaped field. The gate is inoperative — it would not catch the exact " +
			"defect it exists for.")
	}
	t.Logf("mutation caught: %d hit(s), first at %s:%d on %q",
		len(hits), hits[0].File, hits[0].Line, hits[0].Token)

	// It also catches a comment, which is where the original contradiction
	// lived: the prose claimed one thing while the code did another.
	prose := []byte("// this package knows nothing about chap" + "ters\npackage passage\n")
	if hits := scanConsumerVocabulary("prose.go", prose); len(hits) == 0 {
		t.Error("MUTATION SURVIVED: the scanner ignores comments, so a doc comment could " +
			"again promise decoupling in the consumer's own words")
	}
}

// ---------------------------------------------------------------------------
// G-DECOUPLE-2 — no consumer-shaped field name or struct tag, by reflection.
// ---------------------------------------------------------------------------

// scanStructVocabulary walks a struct type recursively and reports every field
// name or struct tag containing a dictionary word. Reflection rather than text,
// so it sees the shape the compiler sees.
func scanStructVocabulary(t reflect.Type, path string) []vocabHit {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Map {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var hits []vocabHit
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		where := path + "." + f.Name
		subject := strings.ToLower(f.Name + " " + string(f.Tag))
		for _, tok := range consumerVocabulary {
			if strings.Contains(subject, tok) {
				hits = append(hits, vocabHit{t.Name(), i, tok, where + " " + string(f.Tag)})
			}
		}
		hits = append(hits, scanStructVocabulary(f.Type, where)...)
	}
	return hits
}

func TestNoConsumerShapedFieldOrTagOnTheExportedTypes(t *testing.T) {
	for _, v := range []any{Record{}, Observation{}, SourceRef{}, Resolution{}, SyncResult{}} {
		typ := reflect.TypeOf(v)
		for _, h := range scanStructVocabulary(typ, typ.Name()) {
			t.Errorf("%s carries a consumer-shaped field or tag (%q): %s",
				typ.Name(), h.Token, h.Text)
		}
	}
}

func TestPairedMutation_TheFieldScannerCatchesASeededField(t *testing.T) {
	// CONTROL: the generic replacement produces no hits.
	type clean struct {
		Scope      *string           `json:"scope"`
		ScopeOrder *float64          `json:"scope_order"`
		Attrs      map[string]string `json:"attrs"`
	}
	if hits := scanStructVocabulary(reflect.TypeOf(clean{}), "clean"); len(hits) != 0 {
		t.Fatalf("CONTROL FAILED: the generic shape reported %v", hits)
	}
	// The mutation, in the type system rather than in text.
	type seeded struct {
		ChapterSlug *string `json:"chapter_slug"`
	}
	hits := scanStructVocabulary(reflect.TypeOf(seeded{}), "seeded")
	if len(hits) == 0 {
		t.Fatal("MUTATION SURVIVED: the reflection scanner did not report a re-added " +
			"ChapterSlug field. The gate is inoperative.")
	}
	t.Logf("mutation caught: %s (%q)", hits[0].Text, hits[0].Token)

	// A field whose NAME is innocent but whose TAG is not must also be caught;
	// the wire format is as much of a contract as the Go identifier.
	type tagOnly struct {
		Group *string `json:"chapter_slug"`
	}
	if hits := scanStructVocabulary(reflect.TypeOf(tagOnly{}), "tagOnly"); len(hits) == 0 {
		t.Error("MUTATION SURVIVED: a consumer-shaped JSON tag on a generically named " +
			"field went unreported")
	}
}

// ---------------------------------------------------------------------------
// G-DECOUPLE-3 — Kind is an OPEN type: the package declares no member.
//
// Exact rather than lexical. A word list can only catch the members it knows
// about; this catches a re-closed enum whatever the members are called.
// ---------------------------------------------------------------------------

// declaredKindConstants parses every .go file in dir and returns the names of
// package-level constants explicitly typed `Kind`.
func declaredKindConstants(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.CONST {
					continue
				}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok || vs.Type == nil {
						continue
					}
					id, ok := vs.Type.(*ast.Ident)
					if !ok || id.Name != "Kind" {
						continue
					}
					for _, n := range vs.Names {
						out = append(out, n.Name)
					}
				}
			}
		}
	}
	return out
}

func TestKindIsAnOpenConsumerDefinedType(t *testing.T) {
	// Structural: no member is declared here. `fixtureKind` in pid_test.go is
	// a test's own token, not part of the package's API — a consumer would
	// declare exactly the same thing in its own package, which is the point.
	for _, name := range declaredKindConstants(t, thisPackageDir(t)) {
		if name == "fixtureKind" {
			continue
		}
		t.Errorf("the package declares a Kind constant %q. Kind is the CONSUMER's "+
			"classification: enumerating a member here re-closes the enum and puts one "+
			"consumer's vocabulary back into every consumer's API.", name)
	}

	// Behavioural: arbitrary consumer tokens are accepted, and malformed ones
	// are still refused, so opening the type did not remove the storage
	// constraint that genuinely belongs to identity.
	for _, k := range []Kind{"prose_block", "clause", "utterance", "commit_message", "frame_7"} {
		if !k.Valid() {
			t.Errorf("a consumer-defined kind %q was rejected; the type is not open", k)
		}
	}
	for _, k := range []Kind{"", "Prose", "prose-block", "prose block", "9lives",
		Kind(strings.Repeat("x", 65))} {
		if k.Valid() {
			t.Errorf("kind %q was accepted; it is not a storable token", k)
		}
	}
}

// ---------------------------------------------------------------------------
// G-DECOUPLE-4 — the derived schema, checked against the database rather than
// against the text that created it.
// ---------------------------------------------------------------------------

func TestSchemaCarriesNoConsumerShapedColumnOrIndex(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	if err := reg.Put(minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "p.db")
	if err := reg.BuildDB(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Every object name and every column name the file actually holds, read
	// back from the database. A grep over schemaSQL would miss a column added
	// by a migration; this does not.
	names := schemaObjectAndColumnNames(t, db)
	for _, n := range names {
		for _, h := range scanConsumerVocabulary("schema", []byte(n)) {
			t.Errorf("the derived database carries a consumer-shaped name (%q): %s",
				h.Token, n)
		}
	}
	t.Logf("%d schema object and column names checked", len(names))

	// Behavioural counterpart to the structural one above: a kind this package
	// has never heard of must STORE. A CHECK (kind IN (...)) reintroduced
	// anywhere — in schemaSQL or in a later migration — fails here.
	unheard := minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPB", "y")
	unheard.Kind = "wombat_marginalia"
	if err := reg.Put(unheard); err != nil {
		t.Fatalf("the registry refused an unheard-of kind: %v", err)
	}
	if err := reg.BuildDB(context.Background(), path); err != nil {
		t.Fatalf("the derived database refused an unheard-of kind (%v) — `kind` is under a "+
			"closed vocabulary somewhere, so this module still enumerates its consumer's "+
			"content types", err)
	}
	var got string
	if err := db.QueryRow(`SELECT kind FROM passages WHERE pid=?`, string(unheard.PID)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "wombat_marginalia" {
		t.Errorf("the stored kind is %q, want it carried through verbatim", got)
	}
}

// The schema is versioned, and a file written by an older one is REFUSED
// rather than written into or silently dropped.
func TestBuildDBRefusesAForeignSchemaVersionInsteadOfDestroyingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.db")
	reg := NewRegistry()
	if err := reg.Put(minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")); err != nil {
		t.Fatal(err)
	}
	// CONTROL: a fresh file builds, and rebuilding it repeatedly stays fine.
	for i := 0; i < 2; i++ {
		if err := reg.BuildDB(context.Background(), path); err != nil {
			t.Fatalf("CONTROL FAILED: rebuild %d: %v", i, err)
		}
	}
	// A file stamped with a version this package does not write.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	err = reg.BuildDB(context.Background(), path)
	if err == nil {
		t.Fatal("a database at a foreign schema version was written into anyway")
	}
	if !strings.Contains(err.Error(), "passages.jsonl") {
		t.Errorf("the refusal does not name the remedy: %v", err)
	}

	// An UNSTAMPED file that already holds `passages` is a v1 file, not a
	// fresh one, and must be told apart from one.
	old := filepath.Join(dir, "old.db")
	odb, err := sql.Open("sqlite", old)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := odb.Exec(`CREATE TABLE passages (pid TEXT PRIMARY KEY, chapter_slug TEXT)`); err != nil {
		t.Fatal(err)
	}
	odb.Close()
	if err := reg.BuildDB(context.Background(), old); err == nil {
		t.Fatal("an unstamped pre-version database was mistaken for a fresh file")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// schemaObjectAndColumnNames reads back every object name and every column
// name the database actually holds. It is the single implementation behind
// G-DECOUPLE-4 and its paired mutation, so the mutation drives the code the
// real gate runs rather than a copy that could agree while the original rots.
//
// It fails rather than returning an empty slice when the file holds nothing:
// a scan over no names reports no hits and is indistinguishable from clean.
func schemaObjectAndColumnNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var names []string
	rows, err := db.Query(`SELECT type, name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var objects []struct{ kind, name string }
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		objects = append(objects, struct{ kind, name string }{kind, name})
		names = append(names, kind+" "+name)
	}
	rows.Close()
	if len(objects) == 0 {
		t.Fatal("the database holds no objects — the check would pass by looking at nothing")
	}
	for _, o := range objects {
		if o.kind != "table" {
			continue
		}
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, o.name)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var c string
			if err := cols.Scan(&c); err != nil {
				cols.Close()
				t.Fatal(err)
			}
			names = append(names, "column "+o.name+"."+c)
		}
		cols.Close()
	}
	return names
}

// TestPairedMutation_TheKindConstantScannerCatchesASeededMember is the §1.1
// proof G-DECOUPLE-3 shipped without.
//
// The gate asserts an ABSENCE — that this package declares no `Kind` constant
// — over a subject set it derives by parsing a directory. An absence assertion
// with no positive control passes trivially the day the parse returns nothing,
// and until this test existed nothing had ever seen `declaredKindConstants`
// report a single name. The mutation is DATA: a seeded source file in a temp
// directory, parsed by the same function the gate calls.
func TestPairedMutation_TheKindConstantScannerCatchesASeededMember(t *testing.T) {
	// CONTROL: a package declaring no Kind constant produces no names, so a
	// name below means the seeded declaration and not a scanner that fires on
	// anything. Constants of OTHER types must not be reported either.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "clean.go"), []byte(
		"package passage\n\ntype Kind string\n\nconst unrelated int = 3\n"+
			"const alsoUnrelated Syntax = \"html\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := declaredKindConstants(t, clean); len(got) != 0 {
		t.Fatalf("CONTROL FAILED: the scanner reported %v on a package declaring no Kind "+
			"constant", got)
	}

	// The mutation: exactly the shape this module shipped for two releases —
	// a closed enum of one consumer's content types, re-added.
	seeded := t.TempDir()
	if err := os.WriteFile(filepath.Join(seeded, "seeded.go"), []byte(
		"package passage\n\ntype Kind string\n\nconst (\n"+
			"\tKindSeededAlpha Kind = \"seeded_alpha\"\n"+
			"\tKindSeededBeta  Kind = \"seeded_beta\"\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := declaredKindConstants(t, seeded)
	if len(got) == 0 {
		t.Fatal("MUTATION SURVIVED: the scanner did not report a re-added Kind constant. " +
			"The gate is inoperative — Kind could be re-closed around one consumer's " +
			"vocabulary and this would stay green.")
	}
	t.Logf("mutation caught: %d Kind constant(s) reported — %v", len(got), got)
}

// TestPairedMutation_TheSchemaScannerCatchesASeededColumn is the §1.1 proof
// G-DECOUPLE-4 shipped without.
//
// It adds a consumer-shaped column to a REAL built database and requires the
// same read-back the gate uses to surface it. That is the case the gate's own
// comment claims to cover — "a grep over schemaSQL would miss a column added
// by a migration; this does not" — and it had never been demonstrated.
func TestPairedMutation_TheSchemaScannerCatchesASeededColumn(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	if err := reg.Put(minimalRecord("01JBX7QK3M8V2ZC4YT5N6RWDPA", "x")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "p.db")
	if err := reg.BuildDB(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// CONTROL: as built, the schema carries nothing consumer-shaped. This is
	// the same assertion the gate makes, restated here so a hit after the
	// ALTER is attributable to the ALTER.
	before := schemaObjectAndColumnNames(t, db)
	for _, n := range before {
		if hits := scanConsumerVocabulary("schema", []byte(n)); len(hits) != 0 {
			t.Fatalf("CONTROL FAILED: the as-built schema already carries %q", n)
		}
	}

	// The mutation, applied to the database rather than to the gate: exactly
	// the column this module shipped in schema v1, re-added the way a careless
	// migration would add it.
	if _, err := db.Exec(`ALTER TABLE passages ADD COLUMN chapter_slug TEXT`); err != nil {
		t.Fatalf("seeding the column: %v", err)
	}
	after := schemaObjectAndColumnNames(t, db)
	if len(after) <= len(before) {
		t.Fatalf("the seeded column did not reach the file: %d names before, %d after",
			len(before), len(after))
	}
	var caught []string
	for _, n := range after {
		for _, h := range scanConsumerVocabulary("schema", []byte(n)) {
			caught = append(caught, n+" ("+h.Token+")")
		}
	}
	if len(caught) == 0 {
		t.Fatal("MUTATION SURVIVED: a consumer-shaped column added by a migration was not " +
			"reported. The gate is inoperative — it would not catch the exact defect its " +
			"own comment says it exists for.")
	}
	t.Logf("mutation caught: %v", caught)

	// And an index, which is the other half of the gate's subject set and is
	// read back through sqlite_master rather than through pragma_table_info.
	if _, err := db.Exec(`CREATE INDEX passages_by_chapter ON passages(chapter_slug)`); err != nil {
		t.Fatalf("seeding the index: %v", err)
	}
	var idxCaught bool
	for _, n := range schemaObjectAndColumnNames(t, db) {
		if len(scanConsumerVocabulary("schema", []byte(n))) != 0 && strings.Contains(n, "index") {
			idxCaught = true
		}
	}
	if !idxCaught {
		t.Error("MUTATION SURVIVED: a consumer-shaped INDEX name went unreported, so only " +
			"the column half of this gate is proven")
	}
}

// thisPackageDir returns the directory holding this package's sources, derived
// from the working directory rather than written down.
func thisPackageDir(t *testing.T) string {
	t.Helper()
	return mustGetwd(t)
}
