package passage

import (
	"strings"
	"testing"
)

const samplePID = PID("01JBX7QK3M8V2ZC4YT5N6RWDPA")

func TestEmissionIsExactlyCanonical(t *testing.T) {
	got := FormatAnchor(samplePID)
	want := "<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->"
	if got != want {
		t.Fatalf("FormatAnchor = %q, want %q — emission must be byte-canonical so "+
			"re-ingest produces byte-identical files (FR-027)", got, want)
	}
}

func TestRecognitionIsTolerantWhereTheContractSaysItIs(t *testing.T) {
	ok := []string{
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->",
		"<!--pid:01JBX7QK3M8V2ZC4YT5N6RWDPA-->",
		"<!--   pid:   01JBX7QK3M8V2ZC4YT5N6RWDPA   -->",
		"<!-- pid: 01jbx7qk3m8v2zc4yt5n6rwdpa -->", // lowercase, §2.1
	}
	for _, line := range ok {
		got, found := ParseAnchorLine(line)
		if !found {
			t.Errorf("ParseAnchorLine(%q) did not recognise an anchor", line)
			continue
		}
		if got != samplePID {
			t.Errorf("ParseAnchorLine(%q) = %q, want the canonical uppercase %q", line, got, samplePID)
		}
	}
	notOK := []string{
		"",
		"just a paragraph",
		"<!-- note: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->",
		"<!-- pid: 4b98e295c112a429 -->", // a Lumen chunk id, not a pid
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPI -->",
		"text before <!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->", // must be on its own line
	}
	for _, line := range notOK {
		if got, found := ParseAnchorLine(line); found {
			t.Errorf("ParseAnchorLine(%q) wrongly recognised %q", line, got)
		}
	}
}

func TestPerFormatAnchorSyntax(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"a/b/doc.md", "<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->"},
		{"a/b/page.html", "<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->"},
		{"a/b/flow.mmd", "%% pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA"},
		{"a/b/seq.puml", "' pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA"},
	}
	for _, c := range cases {
		syn, ok := SyntaxForPath(c.path)
		if !ok {
			t.Errorf("SyntaxForPath(%q): no inline syntax", c.path)
			continue
		}
		if got := syn.Format(samplePID); got != c.want {
			t.Errorf("%s: Format = %q, want %q", c.path, got, c.want)
		}
		back, found := syn.ParseLine(c.want)
		if !found || back != samplePID {
			t.Errorf("%s: round trip failed (%q, %v)", c.path, back, found)
		}
	}
	// §3.2/§3.3: these carry NO inline anchor. Claiming otherwise would be
	// inventing capability.
	for _, p := range []string{"x.vtt", "x.srt", "x.go", "x.pdf", "x.mp4", "x.json"} {
		if _, ok := SyntaxForPath(p); ok {
			t.Errorf("SyntaxForPath(%q) offered an inline anchor syntax; that format is "+
				"sidecar or registry-only", p)
		}
	}
}

func TestScanAnchorsReportsPositions(t *testing.T) {
	doc := strings.Join([]string{
		"intro paragraph without an anchor",
		"",
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->",
		"the anchored paragraph",
		"",
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPB -->",
		"another anchored paragraph",
		"",
	}, "\n")
	got, err := ScanAnchors([]byte(doc), SyntaxHTML)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d anchors, want 2", len(got))
	}
	if got[0].Line != 3 || got[0].BodyLine != 4 {
		t.Errorf("anchor 0 at line %d / body %d, want 3 / 4", got[0].Line, got[0].BodyLine)
	}
	if got[1].Line != 6 || got[1].BodyLine != 7 {
		t.Errorf("anchor 1 at line %d / body %d, want 6 / 7", got[1].Line, got[1].BodyLine)
	}
}

// §3.1: "A pid appears as an anchor at most once." Two blocks claiming the
// same identity is a duplicate-anchor error (R1b), not a last-one-wins merge.
func TestScanAnchorsRejectsADuplicatePID(t *testing.T) {
	doc := "<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->\nfirst\n\n" +
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->\nsecond\n"
	if _, err := ScanAnchors([]byte(doc), SyntaxHTML); err == nil {
		t.Fatal("a pid used as an anchor twice was accepted; a pid names one passage")
	}
}

// An anchor with nothing under it identifies nothing.
func TestScanAnchorsRejectsADanglingAnchor(t *testing.T) {
	for _, doc := range []string{
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->\n",
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->\n\nnot adjacent\n",
	} {
		if _, err := ScanAnchors([]byte(doc), SyntaxHTML); err == nil {
			t.Errorf("a dangling anchor was accepted in:\n%s", doc)
		}
	}
}

func TestBlocksCarryTheirAnchorAndLineRange(t *testing.T) {
	doc := strings.Join([]string{
		"<!-- pid: 01JBX7QK3M8V2ZC4YT5N6RWDPA -->",
		"first line of block one",
		"second line of block one",
		"",
		"block two, unanchored",
		"",
	}, "\n")
	obs, err := Blocks([]byte(doc), SyntaxHTML, "t.md", fixtureKind)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 2 {
		t.Fatalf("got %d blocks, want 2", len(obs))
	}
	if !obs[0].HasAnchor || obs[0].Anchor != samplePID {
		t.Errorf("block 0 anchor = %q / %v", obs[0].Anchor, obs[0].HasAnchor)
	}
	if obs[0].Ref.LineStart != 2 || obs[0].Ref.LineEnd != 3 {
		t.Errorf("block 0 lines %d..%d, want 2..3", obs[0].Ref.LineStart, obs[0].Ref.LineEnd)
	}
	if obs[0].Text != "first line of block one\nsecond line of block one" {
		t.Errorf("block 0 text = %q", obs[0].Text)
	}
	if obs[1].HasAnchor {
		t.Error("block 1 reported an anchor it does not have")
	}
	if obs[1].Ref.LineStart != 5 {
		t.Errorf("block 1 starts at line %d, want 5", obs[1].Ref.LineStart)
	}
	if obs[1].Ref.Anchor != AnchorInline {
		t.Errorf("block 1 anchor mode = %q, want %q", obs[1].Ref.Anchor, AnchorInline)
	}
}
