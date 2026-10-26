# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
# The shared default covers the language-agnostic linters (limen, just, aqua,
# yaml, shell, dockerfile, commits); the Go linters (golangci-lint, govulncheck,
# go mod tidy, go-licenses) live in their own submodule and are added explicitly,
# and lint-harness takes them into the harness module.
[doc('Lint everything (shared linters + Go linters, the harness module included)')]
lint: do::lint::default do::lint::go::default lint-harness

# Shared fixers plus the Go fixers (golangci-lint --fix, go mod tidy), and the
# harness module's formatting and tidiness.
[doc('Auto-fix what can be fixed (shared fixers + Go fixers, the harness module included)')]
fix: do::fix::default do::fix::go::default fix-harness

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
# ffmpeg likewise: the fourth implementation the decode lane and the
# reference tests hold the files to; optional, the comparisons skip without it.
export FFMPEG := env_var_or_default('FFMPEG', `command -v ffmpeg || true`)

[doc('Run the differential harness against this checkout (no fuzz)')]
harness:
    ./harness/run.sh --skip fuzz

[doc('Run the differential harness, fuzz targets included; args go to run.sh')]
harness-fuzz *args:
    ./harness/run.sh {{ args }}

# The harness is its own module (upstream stays out of the library's
# dependency graph), and the shared Go lanes never enter a nested module.
# lint-harness runs them there with the same tools, baseline and platform loop.
# The overlay is harness/.lint-go.yaml, the module's own: `limen-lint-go -C
# harness` renders it on the baseline, since the root overlay's paths are
# relative to the root module and would also match the harness's files of the
# same name (both have a fuzz_test.go). The suppression check needs no second
# run: it greps every Go file in the repository.
harness_cfg := LIMEN_BUILD / 'golangci-harness.yml'

# Runs a command in harness/ under the environment _per-platform prefixes:
# golangci-lint, nilaway and go-licenses take no -C.
harness_run := "bash -c 'cd harness && exec \"$@\"' harness"

[private]
_harness-config: (do::lint::go::_go-tool "golangci-lint" "github.com/golangci/golangci-lint/v2/cmd/golangci-lint")
    @${LIMEN_LINT_GO_BIN:-limen-lint-go} check "$LIMEN_BUILD/tools/golangci-lint"
    @${LIMEN_LINT_GO_BIN:-limen-lint-go} -C harness render -o "{{ harness_cfg }}"

# NilAway blocks here whatever harness/.lint-go.yaml's nilaway.blocking says:
# the shared lane's informational mode is not carried over.
[doc('Lint the harness module with the shared Go lanes')]
lint-harness: (do::lint::go::_banner "lint harness" "go") _harness-config (do::lint::go::_go-tool "go-licenses" "github.com/forkcloser/go-licenses/cmd/go-licenses") (do::lint::go::_go-tool "nilaway" "go.uber.org/nilaway/cmd/nilaway") (do::lint::go::_per-platform harness_run + ' "$LIMEN_BUILD/tools/golangci-lint" run -c "' + harness_cfg + '"') (do::lint::go::_per-platform 'go -C harness vet -asmdecl -buildtag ./...') (do::lint::go::_per-platform harness_run + ' env GOROOT="$(go env GOROOT)" "$LIMEN_BUILD/tools/go-licenses" check --include_tests --ignore "$(go -C harness list -m)" $(${LIMEN_LINT_GO_BIN:-limen-lint-go} -C harness flags licenses) ./...') (do::lint::go::_per-platform harness_run + ' "$LIMEN_BUILD/tools/nilaway" $(${LIMEN_LINT_GO_BIN:-limen-lint-go} -C harness flags nilaway) ./...')
    #!/usr/bin/env bash
    set -euo pipefail
    cd harness
    "$LIMEN_BUILD/tools/golangci-lint" fmt --diff -c "{{ harness_cfg }}"
    go mod tidy -diff

# The formatters and tidy, as `just do fix go` runs them on the root module.
# Its golangci-lint --fix pass is left out: its list of trusted fixers is
# private to the shared recipe, and a copy here would drift from it.
[doc('Format and tidy the harness module')]
fix-harness: (do::fix::go::_banner "fix harness" "go") _harness-config
    #!/usr/bin/env bash
    set -euo pipefail
    cd harness
    "$LIMEN_BUILD/tools/golangci-lint" fmt -c "{{ harness_cfg }}"
    go mod tidy
