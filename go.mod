module github.com/vasic-digital/passage

go 1.26.2

// A SEPARATE MODULE, NOT `internal/`, AND NOT A `pkg/` OF A CONSUMING APP.
//
// Passage identity is the contract every citation, cross-reference and
// redaction resolves through. It is also completely generic: it is about
// stable identifiers for spans of text in files, and it knows nothing about
// chapters, transcripts, recordings or curricula. Those are CONSUMER concepts,
// and the module boundary is what keeps them out — this module does not
// require any consumer, so no consumer-shaped symbol can appear here
// even by accident. `TestNoConsumerShapedDependencies` asserts it.
//
// `internal/` was rejected outright: Go makes an `internal/` package
// importable only from within its own module, by language rule, so it would
// have foreclosed reuse permanently.
//
// Dependencies are deliberately few and each is load-bearing:
//
//	github.com/vasic-digital/verdict  the three-valued outcome type. Zero deps
//	                                  of its own, so importing it costs nothing.
//	golang.org/x/text                 NFC normalisation. content_hash is defined
//	                                  over NFC-normalised text (contract §5.3);
//	                                  hand-rolling Unicode normalisation to
//	                                  avoid a dependency would be worse.
//	modernc.org/sqlite                pure-Go SQLite with FTS5, for the derived
//	                                  passages.db. Already the reference
//	                                  module's driver — no new dependency to the
//	                                  tree, and no cgo.
//
// `verdict` is consumed as a PUBLISHED module at a pinned version, not via a
// sibling-checkout `replace`. A consuming project that mounts both as
// submodules at its own root supplies its own `replace` directives; a
// `replace` here would be ignored by the Go tool in that consumer anyway.

require (
	github.com/vasic-digital/verdict v0.1.0
	golang.org/x/text v0.40.0
	modernc.org/sqlite v1.56.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.74.4 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
