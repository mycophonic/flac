# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
# The shared default covers the language-agnostic linters (limen, just, aqua,
# yaml, shell, dockerfile, commits); the Go linters (golangci-lint, govulncheck,
# go mod tidy, go-licenses) live in their own submodule and are added explicitly.
[doc('Lint everything (shared linters + Go linters)')]
lint: do::lint::default do::lint::go::default

# Shared fixers plus the Go fixers (golangci-lint --fix, go mod tidy).
[doc('Auto-fix what can be fixed (shared fixers + Go fixers)')]
fix: do::fix::default do::fix::go::default

# Unit, race and cover. Benchmarks are a performance measure, not a test:
# `just do perf go bench`.
[doc('Run the Go test suite: unit, race, cover')]
test: do::test::go::unit do::test::go::race do::test::go::cover

# The shared security scans (govulncheck, for a Go module); the security
# workflow runs this recipe.
[doc('Run the security scans')]
security: do::security::default

# The differential harness (harness/README.md): decode and seek against
# libFLAC and upstream mewkiz/flac, each finding mapped to its issue.
# libFLAC's flac is the oracle, unpinnable like brew (BREW_BIN in
# .limen/just/main.just) and captured from the ambient PATH the same way
# (run.sh reads FLAC; override by exporting it).
# The fuzz targets are left out here (minutes) and run by `just harness-fuzz`,
# which takes run.sh's options.
export FLAC := env_var_or_default('FLAC', `command -v flac || true`)

[doc('Run the differential harness against this checkout (no fuzz)')]
harness:
    ./harness/run.sh --skip fuzz

[doc('Run the differential harness, fuzz targets included; args go to run.sh')]
harness-fuzz *args:
    ./harness/run.sh {{ args }}

# The harness is its own module (upstream stays out of the library's
# dependency graph), which the shared Go linters do not enter: this keeps it
# building, vetted and tidy.
[doc('Vet and tidy-check the harness module')]
harness-check:
    go -C harness vet ./...
    go -C harness mod tidy -diff
