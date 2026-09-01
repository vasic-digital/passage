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

It is about identity for spans of text in files. It names no consuming
application's content types, no unit of its subject matter and no role of the
people in it. Those belong to consumers, which map their own material onto
`Observation` and read `Record` back.

Three fields exist so a consumer can carry its own vocabulary through without
this module learning any of it:

| field | what this module does with it |
|---|---|
| `Kind` | stores it. A consumer-defined token; **no members are declared here** and no `CHECK` constraint enumerates any. The only rule is that it must be a storable token. |
| `Scope` / `ScopeOrder` | stores and **indexes** them, as the two columns of `passages_by_scope`. Grouping and ordering are real storage needs; what the group *is* is not this module's business. |
| `Attrs` | stores it and hands it back. A `map[string]string` this module never reads a key of. |

`Record.Metadata()` renders those plus the library-owned facts as one
`map[string]any`, which is the seam onto a retrieval document of the common
`{ID, Content, Metadata, Score, Source}` shape — met, never imported and never
reimplemented:

```go
doc.ID       = string(rec.PID)   // the minted pid IS the citable id
doc.Content  = rec.Public().Text // Public() so a redacted row carries none
doc.Metadata = rec.Metadata()
doc.Source   = rec.SourceRef.Path
```

**The module boundary is not what enforces this, and saying otherwise was the
bug.** Being a separate Go module keeps a consumer's *code* out; it is no
barrier at all to a consumer's *vocabulary*, which arrives with no import and
no require line, typed in by hand — and for two releases it had. The
instruments that actually hold the line are in
[`pkg/passage/decoupling_test.go`](pkg/passage/decoupling_test.go): a
dictionary scan over every source file, comments included, plus go.mod and this
README; a reflection pass over the exported types' fields and struct tags; an
AST check that no `Kind` constant is declared; and a behavioural check that
reads the built database back. Each ships with a paired mutation that has been
observed to fail.

All test fixtures are synthetic.

## Install

```bash
go get github.com/vasic-digital/passage
```

**`v0.2.0` is a breaking release.** Nothing in it is source-compatible with
`v0.1.x`, and a derived database written by `v0.1.x` is refused rather than
migrated in place. [`CHANGELOG.md`](CHANGELOG.md) carries the complete
field-by-field and column-by-column mapping and the upgrade procedure.

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

// Optional: your own domain invariants, run on every write this registry
// takes. They are yours, so a different consumer is not bound by them.
reg := passage.NewRegistry(passage.WithRecordValidator(myRules))

// Ingest a markdown file: scan its anchors, mint pids for new blocks. The
// kind is YOUR token — this package declares none and infers none.
res, err := reg.SyncFile("corpus/notes.md", passage.Kind("my_section"), m)

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
