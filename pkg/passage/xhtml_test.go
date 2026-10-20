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
