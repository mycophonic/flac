module github.com/mycophonic/flac/harness

go 1.25.8

require (
	github.com/mewkiz/flac v1.0.14
	github.com/mycophonic/flac v1.0.0-rc2.0.20261026082650-a874ba5247fb
)

require (
	github.com/icza/bitio v1.1.0 // indirect
	github.com/mewkiz/pkg v0.0.0-20250417130911-3f050ff8c56d // indirect
	github.com/mewpkg/term v0.0.0-20241026122259-37a80af23985 // indirect
)

// The fork under test is the checkout this harness lives in, whatever the
// require line says: run.sh keeps this for its default and --fork-dir, and
// drops it for --fork REV. Without it the require line pinned a digest of
// this repository's own main, which Renovate bumped after every merge, each
// bump a merge that opened the next.
replace github.com/mycophonic/flac => ../
