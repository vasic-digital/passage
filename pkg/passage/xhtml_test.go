package passage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Oracle (XHTML-1): an .xhtml source (EPUB spine documents) is synced through
// the REAL Registry.SyncFile entry point exactly like .html: anchors are
// written back into the file, a textbook_section-shaped kind is accepted (the
// kind is caller-supplied, never inferred), and a second run over unchanged
// input leaves the file byte-identical and mints nothing. The fixture is
// synthetic; no real document text is used.
func TestSyncFileXHTMLInlineAnchors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spine01.xhtml")
	doc := strings.Join([]string{
		"synthetic first block of an xhtml fixture",
		"",
		"synthetic second block of an xhtml fixture",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	m := newTestMinter()
	const kind Kind = "textbook_section"

	first, err := reg.SyncFile(path, kind, m)
	if err != nil {
		t.Fatalf("first SyncFile on .xhtml: %v", err)
	}
	if len(first.Minted) != 2 {
		t.Fatalf("minted %d pids, want 2", len(first.Minted))
	}
	afterFirst, _ := os.ReadFile(path)
	if n := strings.Count(string(afterFirst), "<!-- pid: "); n != 2 {
		t.Fatalf("inline anchors written = %d, want 2; file:\n%s", n, afterFirst)
	}

	second, err := reg.SyncFile(path, kind, m)
	if err != nil {
		t.Fatalf("second SyncFile: %v", err)
	}
	if len(second.Minted) != 0 {
		t.Fatalf("re-sync minted %d new pids, want 0", len(second.Minted))
	}
	afterSecond, _ := os.ReadFile(path)
	if string(afterFirst) != string(afterSecond) {
		t.Fatalf("re-sync changed the file; want byte-identical")
	}
}

// Oracle (XHTML-2): an XML declaration must be the very first bytes of an XML
// document. SyncFile on an .xhtml that starts with `<?xml ...?>` must therefore
// never put an anchor comment before it; the anchor goes after the declaration,
// the declaration line is never part of a passage's text, and a re-sync is
// byte-identical.
func TestSyncFileXHTMLKeepsXMLDeclarationFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spine02.xhtml")
	doc := "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n" +
		"<html xmlns=\"http://www.w3.org/1999/xhtml\"><body>\n" +
		"<p>synthetic one</p>\n\n<p>synthetic two</p>\n</body></html>\n"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	m := newTestMinter()
	const kind Kind = "textbook_section"
	if _, err := reg.SyncFile(path, kind, m); err != nil {
		t.Fatalf("SyncFile: %v", err)
	}
	first, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(first), "<?xml version=\"1.0\" encoding=\"utf-8\"?>\n") {
		t.Fatalf("XML declaration is not the first line:\n%s", first)
	}
	if n := strings.Count(string(first), "<!-- pid: "); n != 2 {
		t.Fatalf("anchors = %d, want 2:\n%s", n, first)
	}
	second, err := reg.SyncFile(path, kind, m)
	if err != nil || len(second.Minted) != 0 {
		t.Fatalf("re-sync err=%v minted=%d, want 0", err, len(second.Minted))
	}
	again, _ := os.ReadFile(path)
	if string(first) != string(again) {
		t.Fatalf("re-sync changed the file:\n%s\n---\n%s", first, again)
	}
}
