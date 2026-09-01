# QWEN.md — passage

This carrier is read by Qwen Code. It is one of the four governance carriers §11.4.157 requires to be maintained in lockstep; every line below this one is byte-identical across all four, once each carrier's own name is normalised.

## INHERITED FROM constitution/QWEN.md

**The inheritance below is conditional. Both cases are stated; neither is
assumed.**

When this module is consumed inside a project that includes the Helix
Constitution submodule, the rules in `constitution/QWEN.md` — and in the
`constitution/Constitution.md` it references — are authoritative for every
topic not covered here. The module-local rules below extend them; they never
weaken or override them.

When this module is consumed standalone — cloned on its own, with no
constitution reachable in any parent — there is nothing to inherit, and **only
the module-local rules below apply**.

### Locating the base file: a resolver, never a path

`constitution/QWEN.md` above is the **canonical name** of the base file, written
as the constitution's own examples write it. It is not a filesystem path
relative to this module and must not be rewritten into one: a consuming project
may mount the constitution at `constitution/` or at
`submodules/constitution/`, and this module cannot know which. Resolve it by
walking parents:

```bash
root=$(git rev-parse --show-toplevel)
bash "$root/submodules/constitution/find_constitution.sh" 2>/dev/null \
  || bash "$root/constitution/find_constitution.sh" 2>/dev/null \
  || echo "standalone — no constitution in scope"
```

Read the canonical text on demand, never eagerly: the corpus is large, and a
native import would load it into every session before any work begins.

## What this module is

Stable identity for citable spans of text: ULIDs minted once at ingest that
survive both text correction and line-position shifts, three anchor modes, and
a registry whose resolution is three-valued. See [README.md](README.md).

**It is project-not-aware, and the enforcement is named rather than implied.**
The module boundary is NOT what enforces it. Being a separate Go module keeps a
consuming project's *code* out; it is no barrier at all to its *vocabulary*,
which needs no import, no require line and no build-graph edge — someone types
it into a struct field. For this module's first two releases the only guard
read `go.mod`, while the API underneath it carried a consumer's nouns in a
struct field, a JSON tag, a closed enum, a SQL column and an index name. What
enforces the claim now is the guard family in
`pkg/passage/decoupling_test.go`, which reads this module's own source,
comments, struct tags and the schema as the built database reports it, and each
of whose gates ships with a mutation that has been observed to make it fail.

## Module-local rules

These extend the inherited rules; they never weaken them.

1. **No consumer-shaped symbol, ever.** No type, field, constant, struct tag,
   column, index, file name, fixture or doc comment may name a concept
   belonging to a consuming application. If a consumer needs something
   expressed here, it is expressed in this module's own generic vocabulary —
   `Kind`, `Scope`, `ScopeOrder` and `Attrs` exist for exactly that — or it
   stays in the consumer.

   This is machine-checked, not reviewed: `consumerVocabulary` in
   `pkg/passage/decoupling_test.go` is the dictionary, the corpus is derived by
   walking the tree rather than listed, and the exemption list holds exactly
   one file (the one that has to contain the dictionary). Prose is in scope
   alongside code, because the violation this rule was written for was a doc
   comment promising decoupling in the consumer's own words while the struct
   beneath it delivered the opposite.

   Domain rules that a consumer genuinely needs enforced do not disappear with
   the vocabulary: `Registry.WithRecordValidator` runs the consumer's own
   invariants at the same choke point this package's run.

2. **Fixtures are synthetic.** This repository is public. No fixture may
   contain material derived from any private corpus, any real recording, any
   real transcript, or any identifiable person's name or speech. Synthetic test
   data only — no exceptions, and no "it is only a test file".

3. **Dependencies are load-bearing or absent.** Every `require` line must
   justify itself in the `go.mod` comment block. Convenience is not a
   justification: every consumer inherits whatever this module requires.

4. **Gates ship with paired mutations (§1.1).** A gate is not trusted until a
   mutation has been observed to make it fail. A gate that cannot fail is
   unvalidated instrumentation, not a gate.

5. **The three-valued distinction is never collapsed.** "Could not determine"
   is neither a pass nor a failure. Any code path that folds it into either is
   a defect, regardless of how convenient the two-valued read would be.

6. **No CI workflow files.** Per §11.4.156 this repository ships no active
   server-side CI. Gates run locally and at the consuming project's seams.

7. **Never force-push** (§11.4.113). Integrate by merging onto the latest
   `main`; a fast-forward push then always succeeds and no commit is lost.

## Build and test

```bash
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

## Upstreams

Mirrored, and both are pushed on every publish (§2.1):

- `git@github.com:vasic-digital/passage.git`
- `git@gitlab.com:vasic-digital/passage.git`

Recipes are in `upstreams/`; run `install_upstreams` from the repository
root after cloning (§11.4.36).
