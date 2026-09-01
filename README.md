# passage

**Revision:** 1
**Last modified:** 2026-09-01T09:30:00Z

Stable identity for citable spans of text.

A **pid** is a ULID minted once at ingest. It is **not** derived from content
and **not** derived from position. Nothing else is ever a citable identity.

## Why not content, and why not position

Both alternatives were measured, and both fail — in opposite directions:

| candidate | measured behaviour |
|---|---|
| content-derived id | fixing **one typo**, line numbers unchanged, changed the id |
| positional key (file + line range) | prepending a section shifted every later range; all ids held, all ranges moved |

Under either scheme a corrected or moved passage loses its citations **while
the UI keeps rendering links** that now point somewhere else. That is a
failure with no visible symptom: nothing errors, nothing 404s, the reader is
simply shown the wrong text.

A minted identifier is the only form that survives both operations, which is
why it is the root of this package. Two gates prove it, each with a paired
mutation:

- **G1** — a text correction does not change the pid.
- **G2** — a line-position shift does not change the pid.

`content_hash` is kept alongside the pid and does the opposite job: it *does*
change when the text changes. Change detection is its one job; identity is not.

## What is in the box

- **`PID` / `Minter`** — 26-character Crockford base32 ULIDs, canonical
  uppercase, monotonic within a process even when the clock stalls or steps
  backwards. There is deliberately **no** accessor for the embedded timestamp:
  sorting by pid or reading a date out of one is a defect, and offering a
  `Time()` method would invite exactly that.
- **Anchors** — three modes, because "write a comment into the file" is not
  universally possible and pretending otherwise would be inventing capability:
  - `inline` — the pid lives in the artifact as a comment (`html`, `mermaid`,
    `plantuml` syntaxes).
  - `sidecar` — formats with no comment syntax that survives every parser get
    a `<file>.pids.jsonl` beside them.
  - `registry_only` — nothing may be written into the artifact: source the
    project does not own, binaries, supplied material.
- **`Registry`** — the record store: `Put`, `Lookup`, `Resolve`, JSONL
  load/write, `Sync` from observations, and `BuildDB` for a derived SQLite
  index with FTS5 (pure-Go driver, no cgo).
- **`Outcome` / `Resolution`** — resolution has four states over the mandated
  three: `found`, `redacted` (present but suppressed — a determined answer of
  its own), `not_in_registry` (a determined negative) and `undetermined` (the
  registry could not be read). The last is **never** folded into
  `not_in_registry`: an unreadable database must not look like a corpus that
  genuinely never contained the passage. `Unavailable(reason)` constructs a
  registry that reports that honestly rather than looking empty.

## This module is project-not-aware

It is about identity for spans of text in files. It knows nothing about
chapters, courses, recordings, transcripts or any other application concept —
those belong to consumers, which map their own content onto `Observation` and
read `Record` back.

It is a separate Go module precisely so that boundary is enforced by the
compiler rather than by review: it does not require any consumer, so no
consumer-shaped symbol can appear here even by accident.
`TestNoConsumerShapedDependencies` asserts it.

All test fixtures are synthetic.

## Install

```bash
go get github.com/vasic-digital/passage
```

As a submodule, mounted at the consuming project's **root** — nested
submodules are forbidden (HelixConstitution §11.4.28), so `verdict` is mounted
at the root too rather than nested inside this repository:

```bash
git submodule add git@github.com:vasic-digital/verdict.git submodules/verdict
git submodule add git@github.com:vasic-digital/passage.git submodules/passage
```

then in the consumer's `go.mod`:

```
require (
    github.com/vasic-digital/passage v0.0.0
    github.com/vasic-digital/verdict v0.0.0
)
replace github.com/vasic-digital/passage => ./submodules/passage
replace github.com/vasic-digital/verdict => ./submodules/verdict
```

A `replace` in a dependency's `go.mod` is ignored by the Go tool, so the
`verdict` replace has to be repeated in the consumer even though `passage`
declares one. That is a language rule, not a duplication mistake.

## Use

```go
m := passage.NewMinter()
reg := passage.NewRegistry()

// Ingest a markdown file: scan its anchors, mint pids for new blocks.
res, err := reg.SyncFile("corpus/notes.md", m)

// Resolve a citation.
switch r := reg.Resolve(pid); r.Outcome {
case passage.OutcomeFound:          // r.Record holds the passage
case passage.OutcomeRedacted:       // present, suppressed — still an answer
case passage.OutcomeNotInRegistry:  // the pid genuinely names nothing here
default:                            // undetermined — r.Err says why
}
```

`Outcome.Verdict()` maps onto `verdict.Verdict` and `Outcome.HTTPStatus()` onto
200 / 410 / 404 / **503**, so an unreadable registry reaches the wire as a
service problem instead of masquerading as a 404 "no such passage".

## Dependencies

Deliberately few, each load-bearing:

- `github.com/vasic-digital/verdict` — the three-valued outcome type. Zero
  dependencies of its own, so importing it costs nothing.
- `golang.org/x/text` — NFC normalisation. `content_hash` is defined over
  NFC-normalised text; hand-rolling Unicode normalisation to avoid a
  dependency would be worse.
- `modernc.org/sqlite` — pure-Go SQLite with FTS5 for the derived index. No
  cgo.

## Test

```bash
go test ./...
go test -race ./...
```

## License

MIT — see [LICENSE](LICENSE).
