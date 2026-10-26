#!/bin/bash
# usage: enc32.sh <wav dir> <flac dir>
# Encodes every WAV with several libFLAC settings; keeps what `flac -t` accepts.
# Set FLAC to use a flac binary that is not first on PATH.
flac="${FLAC:-flac}"
in="$1"; out="$2"; mkdir -p "$out"
settings=(
  "m8|-8 -m"
  "M5|-5 -M"
  "f0|-0"
  "fx|-l 0 -m --lax"
  "e8|-8 -e -p -m"
  "l32|-l 32 -m --lax"
  "b16|-8 -m --blocksize=16"
  "b192|-8 -m --blocksize=192"
  "b4608|-8 -m --blocksize=4608"
  "r8|-8 -m -r 8"
  "ind|-8 --no-mid-side"
)
ok=0; bad=0
for w in "$in"/*.wav; do
  base=$(basename "$w" .wav)
  for s in "${settings[@]}"; do
    tag=${s%%|*}
    read -ra opts <<<"${s#*|}"
    f="$out/$base.$tag.flac"
    if "$flac" -s -f "${opts[@]}" -o "$f" "$w" 2>/dev/null && "$flac" -s -t "$f" 2>/dev/null; then ok=$((ok+1)); else rm -f "$f"; bad=$((bad+1)); fi
  done
done
echo "encoded $ok, refused $bad"
