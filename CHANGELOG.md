# Changelog — passage

All notable changes to this module. Versions are the annotated git tags in this
repository; a published tag is never moved (§11.4.113).

---

## Unreleased (proposed v0.3.0, additive)

- `SyntaxForPath` now recognises `.xhtml` (case-insensitive) and returns
  `SyntaxHTML`, so `Registry.SyncFile` writes the same `<!-- pid: ... -->`
  anchors into XHTML sources (for example EPUB spine documents). Additive:
  every previously accepted extension behaves identically, and `.xhtml`
  previously returned "no inline syntax" (sidecar/registry-only). No other
  API, schema or wire format changed. Tests: `TestPerFormatAnchorSyntax`
  (extended) and `TestSyncFileXHTMLInlineAnchors` (new, synthetic fixture,
  idempotent re-sync).

---

## v0.2.0 — 2026-09-01

### BREAKING. Read the migration table before upgrading.

`v0.1.0` and `v0.1.1` documented this module as project-not-aware in four
places, and the API underneath contradicted every one of them: one consuming
application's nouns sat in a struct field, a JSON tag, a closed enum, two SQL
columns and an index name. This release removes them and replaces them with a
generic seam. Nothing in it is source-compatible with `v0.1.x`, and the derived
SQLite file written by `v0.1.x` is not readable by this release.

The old guard, `TestNoConsumerShapedDependencies`, read `go.mod` and nothing
else — it never opened a source file of the package it was vouching for. It is
kept, and it is now one of **six** gates in the decoupling family, **four** of
which ship a paired mutation that has been observed to fail.

**Corrected 2026-09-06, by counting rather than by recollection.** This
paragraph read "one of eight gates in `pkg/passage/decoupling_test.go`, four of
which ship a paired mutation", and both halves were wrong at the time. The
guard it names lives in `pkg/passage/pid_test.go:685`, **not** in
`decoupling_test.go`; that file then held **seven** test functions, of which
only **two** were paired mutations — G-DECOUPLE-3 (the AST check) and
G-DECOUPLE-4 (the schema read-back) shipped none, so "four" overstated the
suite by two. The two missing mutations were written rather than the claim
walked back, which is why the corrected figure is four and not two. Re-derive
instead of trusting this note:

```bash
grep -c '^func Test'              pkg/passage/decoupling_test.go   # 9
grep -c '^func TestPairedMutation' pkg/passage/decoupling_test.go  # 4
grep -rn '^func TestNoConsumerShapedDependencies' pkg/            # pid_test.go
```

### Migration — every removed name and what replaces it

| removed in v0.1.x | replacement in v0.2.0 | notes |
|---|---|---|
| `Record.ChapterSlug *string`, JSON `chapter_slug` | `Record.Scope *string`, JSON `scope` | Stored and indexed. This package never reads its value. |
| `Record.TStartS *float64`, JSON `t_start_s` | `Record.ScopeOrder *float64`, JSON `scope_order` | The ordering key within a `Scope`. Any monotone consumer-chosen number. |
| `Record.TEndS *float64`, JSON `t_end_s` | `Record.Attrs["<your key>"]` | No second ordinal is modelled. Carry it as an attribute. |
| `Record.Speaker *string`, JSON `speaker` | `Record.Attrs["<your key>"]` | |
| `Record.SpeakerSource *SpeakerSource`, JSON `speaker_source` | `Record.Attrs["<your key>"]` | |
| type `SpeakerSource` and its constants `SpeakerHuman`, `SpeakerUnattributed` | *(deleted)* | A consumer-owned enumeration. Validate it with `WithRecordValidator`. |
| `Kind` constants `KindTranscript`, `KindDocSection`, `KindCode`, `KindDiagram` | *(deleted — `Kind` remains, open)* | `Kind` is still `type Kind string`. This package declares **no** member and enumerates none; an AST gate asserts that. |
| `Kind.valid()` (unexported) | `Kind.Valid()` (exported) | Syntax only: `^[a-z][a-z0-9_]{0,63}$`. It is a storage constraint — indexable, case-stable, URL-safe — not a vocabulary. |
| `NewRegistry()` | `NewRegistry(opts ...RegistryOption)` | Variadic; the no-argument call still compiles. |
| `Unavailable(reason error)` | `Unavailable(reason error, opts ...RegistryOption)` | Variadic; the one-argument call still compiles. |
| `(*Registry).SyncFile(path string, m *Minter, opts ...SyncOption)` | `(*Registry).SyncFile(path string, kind Kind, m *Minter, opts ...SyncOption)` | The kind is now yours to pass. This package infers none. |

#### Derived SQLite schema, v1 → v2

| removed column / index | replacement | notes |
|---|---|---|
| `passages.chapter_slug TEXT` | `passages.scope TEXT` | |
| `passages.t_start_s REAL` | `passages.scope_order REAL` | |
| `passages.t_end_s REAL` | `passages.attrs TEXT` | Consumer-owned JSON object. Written, read back, nothing inside it indexed. |
| `passages.speaker TEXT` | `passages.attrs TEXT` | |
| `passages.speaker_source TEXT CHECK (...)` | `passages.attrs TEXT` | |
| `kind TEXT NOT NULL CHECK (kind IN (...))` | `kind TEXT NOT NULL` | The `CHECK` list was this library enumerating a consumer's domain in DDL. |
| `CREATE INDEX passages_by_chapter ON passages(chapter_slug, t_start_s)` | `CREATE INDEX passages_by_scope ON passages(scope, scope_order)` | |

`passages_by_path`, `passages_symbol_key`, `passages_fts` and every other table
are unchanged.

### New

- **`Record.Attrs map[string]string`, JSON `attrs`.** Consumer-owned data,
  stored verbatim and never interpreted. `reservedAttrKeys` — `kind`, `scope`,
  `scope_order`, `path`, `line_start`, `line_end`, `symbol`, `provenance`,
  `content_hash`, `redacted` — are **refused** as `Attrs` keys by
  `Record.Validate`, so a library-owned key can never silently shadow a
  consumer's value or the reverse.
- **`Record.Metadata() map[string]any`.** Renders `Attrs` plus the
  library-owned facts as one map, which is the seam onto a retrieval document
  of the common `{ID, Content, Metadata, Score, Source}` shape — met, never
  imported, never reimplemented.
- **`WithRecordValidator(fn func(Record) error) RegistryOption`.** Domain rules
  that left this package with the vocabulary do not disappear: the consumer's
  own invariants run at the same choke point this package's do, on every write
  path, `LoadJSONL` included.

### Changed

- **`PRAGMA user_version` is now stamped, and a foreign version is refused.**
  `schemaVersion = 2`. A file at any other version — including an *unstamped*
  file that already holds a `passages` table, which is a v1 file rather than a
  fresh one — is refused with a message naming the file, both versions and the
  remedy. The derived database is a cache; the remedy is to delete it and
  rebuild from `passages.jsonl`, which is the source of truth. This package
  will not drop tables it does not write.
- **`LoadJSONL` sets `DisallowUnknownFields`.** A record carrying a key this
  package does not define stops the load and reports the offending key **from
  the data**, rather than loading successfully minus whatever those fields
  carried. That rule needs no list of retired names, which is why rejecting a
  `v0.1.x` line does not require this package to write a consumer's vocabulary
  into its own source.

### Upgrade procedure

1. Delete the derived `passages.db`. It is a cache and it will be refused
   otherwise.
2. Rewrite `passages.jsonl`: rename `chapter_slug` → `scope`,
   `t_start_s` → `scope_order`, and move `t_end_s`, `speaker` and
   `speaker_source` into an `attrs` object under keys you choose. Any key left
   over that this package does not define will now stop the load by name — a
   partial migration fails loudly instead of quietly dropping fields.
3. Pass your own `Kind` token to `SyncFile`. There are no built-in members.
4. Move any rule of the form "a passage of kind X requires field Y" into a
   `WithRecordValidator` function. Those are domain rules; they were never this
   package's to enforce.
5. Rebuild: `BuildDB` stamps `user_version = 2`.

### A note on this file's scope

The vocabulary gate `TestNoConsumerShapedVocabularyInSource` derives its corpus
by walking the tree and covers every `*.go` file, `go.mod` and `README.md`.
`CHANGELOG.md` is outside that corpus **by construction, not by exemption** —
the exemption list still holds exactly one entry and the gate asserts that
count. This file names the removed symbols deliberately, because a
breaking-change note that cannot say what it removed is useless to whoever has
to migrate. They are recorded here as historical facts about this module's own
retired API, not as vocabulary this module speaks. Anyone widening the gate's
corpus to all Markdown should exempt this file explicitly and say so, rather
than deleting the migration table.

---

## v0.1.1 — carriers in lockstep; requires verdict v0.1.1

Pinned `github.com/vasic-digital/verdict` at `v0.1.1` — an immutable tag —
instead of the moved `v0.1.0` tag. Brought the four §11.4.157 governance
carriers into measurable lockstep.

## v0.1.0 — first public release

Stable identity for citable spans of text: ULIDs minted once at ingest that
survive both text correction and line-position shifts, three anchor modes, and
a registry whose resolution is three-valued.
