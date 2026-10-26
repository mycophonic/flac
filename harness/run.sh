#!/usr/bin/env bash
## usage: ./run.sh [options]
##
## Generates the corpora, builds the harness against one mycophonic/flac
## commit, then decodes, probes, seeks, fuzzes and summarizes.
##
##   --fork REV         a commit, branch or version of github.com/mycophonic/flac,
##                      fetched as a Go module
##   --fork-dir DIR     a local checkout instead (a replace directive; default:
##                      the checkout this harness lives in)
##   --label NAME       a name for the commit in the summary
##   --out DIR          output directory (default: the checkout's .build/harness,
##                      LIMEN_BUILD/harness when just exports it)
##   --flac-dir [NAME=]DIR  also decode every .flac under DIR, as corpus NAME
##                      (default: user-<last element of DIR>; repeatable)
##   --fuzz-time SECS   fuzzing time over all the targets (default: 1500)
##   --fuzz-targets T   the targets, comma separated (default: FuzzStream,FuzzFrame,FuzzMeta)
##   --seed-dir DIR     also seed the fuzzers with the .flac files under DIR
##                      (repeatable; by default only the generated corpora)
##   --jobs N           parallel decodes (default: 2)
##   --fuzz-workers N   fuzz worker processes (default: --jobs)
##   --fuzz-minimize D  time Go spends minimizing each new input or failure
##                      (default: a twentieth of each target's time, 1s to 10s;
##                      Go's own default, 60s, stalls a short run)
##   --skip STEP        skip gen, decode, alloc, seek or fuzz (repeatable)
##   --allow-panic FUNCS  functions (comma separated) whose panics the fuzz run
##                      records and goes past: a known panic, such as #34's
##                      decodeFixedWide,decodeFIRWide on a commit without its fix
##   --meta-alloc-per-byte N  FuzzMeta's bound per input byte (default: 16, the
##                      bound #33 uses; 1040 looks past a cost of 4 KiB per
##                      metadata string)
##
## Environment: FLAC names the flac binary (default: flac on PATH); Go's own
## variables (GOCACHE, GOMODCACHE, GOFLAGS, GOTOOLCHAIN) apply as usual.
set -euo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
launch=$PWD
fork_rev="" fork_dir="" label="" out="${LIMEN_BUILD:-$here/../.build}/harness" fuzz_time=1500 jobs=2 workers="" skip="," allow="" minimize="" perbyte="" fuzz_targets=FuzzStream,FuzzFrame,FuzzMeta
flac_dirs=()
seed_dirs=""

usage() { sed -n 's/^## \{0,1\}//p' "$0"; exit "${1:-0}"; }
die() { echo "run.sh: $*" >&2; exit 1; }
while [ $# -gt 0 ]; do
	case "$1" in
	--fork) fork_rev=${2:?}; shift 2 ;;
	--fork-dir) fork_dir=${2:?}; shift 2 ;;
	--label) label=${2:?}; shift 2 ;;
	--out) out=${2:?}; shift 2 ;;
	--flac-dir) flac_dirs+=("${2:?}"); shift 2 ;;
	--seed-dir) seed_dirs="$seed_dirs:${2:?}"; shift 2 ;;
	--fuzz-time) fuzz_time=${2:?}; shift 2 ;;
	--fuzz-targets) fuzz_targets=${2:?}; shift 2 ;;
	--jobs) jobs=${2:?}; shift 2 ;;
	--fuzz-workers) workers=${2:?}; shift 2 ;;
	--fuzz-minimize) minimize=${2:?}; shift 2 ;;
	--skip) skip="$skip${2:?},"; shift 2 ;;
	--allow-panic) allow=${2:?}; shift 2 ;;
	--meta-alloc-per-byte) perbyte=${2:?}; shift 2 ;;
	-h | --help) usage ;;
	*) echo "run.sh: unknown argument $1" >&2; usage 2 ;;
	esac
done
[ -z "$fork_rev" ] || [ -z "$fork_dir" ] || die "--fork and --fork-dir are exclusive"
# The harness lives in the fork: with neither option, it tests the checkout
# around it.
[ -n "$fork_rev" ] || [ -n "$fork_dir" ] || fork_dir=$here/..
workers=${workers:-$jobs}
skipped() { case "$skip" in *",$1,"*) return 0 ;; esac; return 1; }

flac=${FLAC:-flac}
export FLAC=$flac
command -v go >/dev/null || die "go not found on PATH"
command -v "$flac" >/dev/null || die "flac not found: install libFLAC's flac tool or set FLAC"
flac_version=$("$flac" --version 2>&1 | head -1)
case "$flac_version" in
"flac 1.5.0"*) ;;
*) echo "run.sh: warning: $flac_version; the verdicts were made with flac 1.5.0" >&2 ;;
esac

mkdir -p "$out"
out=$(cd "$out" && pwd)
# run.sh deletes and rewrites what it made under --out (corpus/, fuzz/), so it
# takes only an empty directory or one it made before (its .flacharness mark).
if [ ! -f "$out/.flacharness" ] && [ -n "$(ls -A "$out")" ]; then
	die "$out is not empty and was not made by run.sh: name a new or empty --out"
fi
: >"$out/.flacharness"
corpus=$out/corpus build=$out/build
step_start=0
step() { step_start=$(date +%s); echo "== $*"; }
done_step() { echo "   $(( $(date +%s) - step_start ))s" | tee -a "$out/timing.txt"; }
: > "$out/timing.txt"
t0=$(date +%s)


step "build the harness"
mkdir -p "$build"
cp "$here/go.mod" "$build/fork.mod"
cp "$here/go.sum" "$build/fork.sum"
tags=()
(
	cd "$here"
	if [ -n "$fork_dir" ]; then
		case "$fork_dir" in /*) abs=$fork_dir ;; *) abs=$launch/$fork_dir ;; esac
		[ -f "$abs/go.mod" ] || die "no go.mod in $abs"
		# Clean: with `..` left in, Go cannot tell this nested module apart
		# from the fork's harness/ directory and reports an ambiguous import.
		abs=$(cd "$abs" && pwd -P)
		go mod edit -modfile="$build/fork.mod" -replace "github.com/mycophonic/flac=$abs"
	elif [ -n "$fork_rev" ]; then
		# go.mod's replace points at the checkout; a fetched revision needs it
		# gone first.
		go mod edit -modfile="$build/fork.mod" -dropreplace github.com/mycophonic/flac
		go get -modfile="$build/fork.mod" "github.com/mycophonic/flac@$fork_rev"
	fi
	go build -modfile="$build/fork.mod" -trimpath ${tags[@]+"${tags[@]}"} -o "$build/flacharness" .
)

bin=$build/flacharness

if ! skipped gen; then
	step "generate the corpora"
	rm -rf "$corpus"
	mkdir -p "$corpus"
	"$bin" gen -out "$corpus" -sha "$out/corpus.sha256"
	echo "   $(wc -l <"$out/corpus.sha256" | tr -d ' ') files; sha256 in corpus.sha256"
	done_step
fi
[ -d "$corpus/gen32" ] || die "no corpus in $corpus: run without --skip gen"
"$bin" version >"$out/version.json"
if [ -z "$label" ] && [ -n "$fork_dir" ]; then
	label="local checkout at $(git -C "$fork_dir" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
fi
[ -n "$label" ] || label=${fork_rev:-default}
echo "   $(cat "$out/version.json")"
done_step

corpora=(-corpus "gen32=$corpus/gen32" -corpus "bench=$corpus/bench" -corpus "crafted=$corpus/crafted"
	-corpus "control=$corpus/control" -corpus "boundary=$corpus/boundary" -corpus "targeted=$corpus/targeted"
	-corpus "hostile=$corpus/hostile")
for d in ${flac_dirs[@]+"${flac_dirs[@]}"}; do
	case "$d" in
	*=*) corpora+=(-corpus "$d") ;;
	*) corpora+=(-corpus "user-$(basename "$d")=$d") ;;
	esac
done

if ! skipped decode; then
	step "decode: fork, upstream and libFLAC"
	"$bin" decode -j "$jobs" -out "$out/decode.jsonl" "${corpora[@]}"
	done_step
fi
if ! skipped alloc; then
	step "allocation probes"
	"$bin" alloc -out "$out/alloc.jsonl"
	done_step
fi
if ! skipped seek; then
	step "seek against flac -d --skip/--until"
	"$bin" seek -out "$out/seek.jsonl" -corpus "bench=$corpus/bench" -corpus "gen32=$corpus/gen32" \
		-match 'bench-*.flac,*.m8.flac,*.b16.flac,*.b192.flac,*.b4608.flac'
	done_step
fi

if ! skipped fuzz && [ "$fuzz_time" -gt 0 ]; then
	IFS=, read -r -a targets <<<"$fuzz_targets"
	per=$((fuzz_time / ${#targets[@]}))
	if [ -z "$minimize" ]; then
		m=$((per / 20))
		[ "$m" -ge 1 ] || m=1
		[ "$m" -le 10 ] || m=10
		minimize=${m}s
	fi
	seeds="$corpus/gen32:$corpus/bench:$corpus/crafted:$corpus/control:$corpus/boundary:$corpus/targeted:$corpus/hostile$seed_dirs"
	rm -rf "$out/fuzz"
	mkdir -p "$out/fuzz"
	for t in "${targets[@]}"; do
		step "fuzz $t for ${per}s"
		[ ! -e "$here/testdata/fuzz/$t" ] || die "testdata/fuzz/$t exists: move it away first"
		(
			cd "$here"
			FLACHARNESS_SEEDS=$seeds FLACHARNESS_OBS=$out/fuzz FLACHARNESS_ALLOW_PANIC=$allow FLACHARNESS_META_PER_BYTE=$perbyte \
				go test -modfile="$build/fork.mod" ${tags[@]+"${tags[@]}"} -run '^$' -fuzz "^$t\$" -fuzztime "${per}s" -fuzzminimizetime "$minimize" \
				-parallel "$workers" -timeout "$((per + 900))s" .
		) >"$out/fuzz/$t.log" 2>&1 || true
		# A failing input lands in testdata/fuzz/<target>: move it out.
		if [ -d "$here/testdata/fuzz/$t" ]; then
			mkdir -p "$out/fuzz/crashers/$t"
			mv "$here/testdata/fuzz/$t"/* "$out/fuzz/crashers/$t/"
			rmdir "$here/testdata/fuzz/$t"
			rmdir "$here/testdata/fuzz" "$here/testdata" 2>/dev/null || true
		fi
		tail -1 "$out/fuzz/$t.log"
		done_step
	done
	step "triage fuzz failures and observations against libFLAC"
	: >"$out/fuzz/triage.jsonl"
	for t in "${targets[@]}"; do
		for kind in crashers observations allowed; do
			set -- "$out/fuzz/$kind/$t"/*
			[ -e "$1" ] || continue
			case "$kind" in
			observations) set -- "$out/fuzz/$kind/$t"/*.flac ;;
			# Allowed panics are known: replay ten, to show they are what was allowed.
			allowed) set -- "$out/fuzz/$kind/$t"/*.flac; set -- "${@:1:10}" ;;
			esac
			"$bin" triage -target "$t" -dir "$out/fuzz/replay/$kind-$t" "$@" >>"$out/fuzz/triage.jsonl"
		done
	done
	done_step
fi

step "summarize"
"$bin" summarize -dir "$out" -label "$label"
echo "   total $(( $(date +%s) - t0 ))s" | tee -a "$out/timing.txt"
echo "== outputs in $out: summary.txt, summary.json, decode.jsonl, alloc.jsonl, seek.jsonl, fuzz/"
