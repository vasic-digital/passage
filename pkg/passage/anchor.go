package passage

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// AnchorMode records how a passage's identity travels with its text.
type AnchorMode string

const (
	// AnchorInline: the pid is written into the artifact as a comment.
	AnchorInline AnchorMode = "inline"
	// AnchorSidecar: the format has no comment syntax that survives every
	// parser, so the pid lives in a <file>.pids.jsonl beside it.
	AnchorSidecar AnchorMode = "sidecar"
	// AnchorRegistryOnly: nothing may be written into the artifact — source
	// code the project does not own, binaries, supplied material.
	AnchorRegistryOnly AnchorMode = "registry_only"
)

// Syntax is one artifact family's anchor comment syntax.
//
// The set is deliberately small. "Write a comment" is not universally
// possible, and pretending otherwise would be inventing capability: many
// formats — binary containers, PDFs, timed-text files, program sources whose
// toolchain rewrites comments — carry no inline anchor at all.
type Syntax struct {
	// Name identifies the family in errors.
	Name string

	open, close string
	recognise   *regexp.Regexp
}

// The three families that can carry an inline anchor.
var (
	// SyntaxHTML is the canonical form, used by Markdown, HTML and SVG.
	// HTML comments do not render, so anchors are invisible to a reader while
	// remaining in the source a human edits.
	SyntaxHTML = Syntax{
		Name: "html", open: "<!--", close: "-->",
		recognise: regexp.MustCompile(`^<!--\s*pid:\s*([0-9A-Za-z]{26})\s*-->$`),
	}
	// SyntaxMermaid uses %% and must precede the statement it identifies.
	SyntaxMermaid = Syntax{
		Name: "mermaid", open: "%%", close: "",
		recognise: regexp.MustCompile(`^%%\s*pid:\s*([0-9A-Za-z]{26})\s*$`),
	}
	// SyntaxPlantUML uses a single quote.
	SyntaxPlantUML = Syntax{
		Name: "plantuml", open: "'", close: "",
		recognise: regexp.MustCompile(`^'\s*pid:\s*([0-9A-Za-z]{26})\s*$`),
	}
)

// SyntaxForPath returns the inline anchor syntax for a path, if the format has
// one. It returns false for every format that is sidecar or registry-only —
// which is most of them.
func SyntaxForPath(path string) (Syntax, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".html", ".htm", ".svg":
		return SyntaxHTML, true
	case ".mmd", ".mermaid":
		return SyntaxMermaid, true
	case ".puml", ".plantuml":
		return SyntaxPlantUML, true
	default:
		return Syntax{}, false
	}
}

// Format emits the CANONICAL anchor: exactly one space after the opener, one
// either side of the pid, one before the closer. Emission is canonical even
// though recognition is tolerant, so re-ingest produces byte-identical files.
func (s Syntax) Format(p PID) string {
	if s.close == "" {
		return fmt.Sprintf("%s pid: %s", s.open, p)
	}
	return fmt.Sprintf("%s pid: %s %s", s.open, p, s.close)
}

// ParseLine recognises an anchor on a line of its own, tolerantly, and returns
// the CANONICAL uppercase pid. A line with anything else on it is not an
// anchor — a pid mentioned inside prose does not claim an identity.
func (s Syntax) ParseLine(line string) (PID, bool) {
	m := s.recognise.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
	if m == nil {
		return "", false
	}
	p, err := ParsePID(m[1])
	if err != nil {
		return "", false
	}
	return p, true
}

// FormatAnchor emits the canonical HTML-comment anchor.
func FormatAnchor(p PID) string { return SyntaxHTML.Format(p) }

// ParseAnchorLine recognises the canonical HTML-comment anchor.
func ParseAnchorLine(line string) (PID, bool) { return SyntaxHTML.ParseLine(line) }

// Anchor is an anchor found in a document, with 1-based line numbers.
type Anchor struct {
	PID PID
	// Line is where the anchor comment itself sits.
	Line int
	// BodyLine is the first line of the passage it identifies — always
	// Line+1, because an anchor with a blank line under it identifies nothing.
	BodyLine int
}

// ScanAnchors finds every anchor in a document.
//
// It rejects two conditions rather than papering over them:
//
//   - a pid used as an anchor twice in one document. A pid names ONE passage;
//     "last one wins" would silently merge two passages into one identity.
//   - a dangling anchor with no passage under it. It identifies nothing, and
//     the next sync would either mint a duplicate or attach to the wrong text.
func ScanAnchors(src []byte, s Syntax) ([]Anchor, error) {
	lines := splitLines(src)
	var out []Anchor
	seen := map[PID]int{}
	for i, line := range lines {
		p, ok := s.ParseLine(line)
		if !ok {
			continue
		}
		if prev, dup := seen[p]; dup {
			return nil, fmt.Errorf("passage: pid %s is used as an anchor on lines %d and %d; "+
				"a pid names one passage", p, prev, i+1)
		}
		if i+1 >= len(lines) || strings.TrimSpace(lines[i+1]) == "" {
			return nil, fmt.Errorf("passage: dangling anchor for %s on line %d: "+
				"no passage follows it", p, i+1)
		}
		seen[p] = i + 1
		out = append(out, Anchor{PID: p, Line: i + 1, BodyLine: i + 2})
	}
	return out, nil
}

// Blocks splits a document into blank-line-separated blocks, carrying any
// anchor that immediately precedes each one.
//
// The segmentation is deliberately simple and is NOT the last word: a producer
// that already knows its own segment boundaries builds Observation values
// itself and calls Registry.Sync directly. Blocks exists so plain prose
// artifacts work with no producer at all.
func Blocks(src []byte, s Syntax, path string, kind Kind) ([]Observation, error) {
	if _, err := ScanAnchors(src, s); err != nil {
		return nil, err
	}
	lines := splitLines(src)
	var out []Observation
	i := 0
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		// An anchor line opens a block; the body starts on the next line.
		var anchor PID
		hasAnchor := false
		if p, ok := s.ParseLine(lines[i]); ok {
			anchor, hasAnchor = p, true
			i++
		}
		start := i
		for i < len(lines) && strings.TrimSpace(lines[i]) != "" {
			// A second anchor inside a block starts a new block.
			if i > start {
				if _, ok := s.ParseLine(lines[i]); ok {
					break
				}
			}
			i++
		}
		if i == start {
			continue
		}
		out = append(out, Observation{
			Anchor:    anchor,
			HasAnchor: hasAnchor,
			Kind:      kind,
			Text:      strings.Join(lines[start:i], "\n"),
			Ref: SourceRef{
				Path:      filepath.ToSlash(path),
				LineStart: start + 1,
				LineEnd:   i,
				Anchor:    AnchorInline,
			},
		})
	}
	return out, nil
}

// writeAnchors returns the document with each block's anchor line present and
// carrying the assigned pid, inserting one where absent and replacing one that
// disagrees. The second byte-identical value is whether anything changed —
// callers must not rewrite an unchanged file (idempotency I2/I4).
func writeAnchors(src []byte, s Syntax, assigned []assignment) ([]byte, bool) {
	lines := splitLines(src)
	// Work from the bottom so earlier insertions do not shift later indices.
	changed := false
	for i := len(assigned) - 1; i >= 0; i-- {
		a := assigned[i]
		body := a.ref.LineStart - 1 // 0-based index of the block's first line
		want := s.Format(a.pid)
		if body > 0 {
			if _, isAnchor := s.ParseLine(lines[body-1]); isAnchor {
				if lines[body-1] != want {
					lines[body-1] = want
					changed = true
				}
				continue
			}
		}
		lines = append(lines[:body], append([]string{want}, lines[body:]...)...)
		changed = true
	}
	if !changed {
		return src, false
	}
	return []byte(strings.Join(lines, "\n") + "\n"), true
}

type assignment struct {
	pid PID
	ref SourceRef
}

// splitLines splits on LF, tolerating CRLF input and a missing or present
// final newline, and never yielding a phantom trailing empty line.
func splitLines(src []byte) []string {
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
