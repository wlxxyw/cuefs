#!/bin/bash
# Follow-ups for M1: vorbis comment key case, APE decode, 24-bit round trip.
set -u
SCRATCH="${1:?scratch dir required}"
mkdir -p "$SCRATCH"

find_src() {
  find /home/user/music/ -iname "*.cue" -print0 2>/dev/null | while IFS= read -r -d '' c; do
    b="${c%.*}"
    for e in flac ape wav; do
      [ -f "$b.$e" ] && { printf '%s\0' "$b.$e"; return; }
    done
  done | head -z -1 2>/dev/null || true
}

echo "=== A. vorbis comment key case (do -metadata keys survive verbatim?)"
SRC=$(find /home/user/music/ -iname "*.flac" -print0 2>/dev/null | head -z -1)
OUT="$SCRATCH/tag.flac"
ffmpeg -y -v error -nostdin -i "$SRC" -map 0:a -vn -map_metadata -1 \
  -metadata "TITLE=Mixed Case" -metadata "tracknumber=99" \
  -metadata "ARTIST=A" -metadata "ALBUMARTIST=B" \
  -c:a flac "$OUT"
for k in TITLE tracknumber TRACKNUMBER ARTIST ALBUMARTIST; do
  if grep -qa "$k" "$OUT"; then echo "  present  : $k"; else echo "  absent   : $k"; fi
done
echo "  (grep for the literal key bytes inside the FLAC file)"
echo "  what ffmpeg reports back:"
ffmpeg -hide_banner -nostdin -i "$OUT" 2>&1 | sed -n '/Metadata:/,/Stream/p'

echo
echo "=== B. APE decode"
find /home/user/music/ -iname "*.ape" -print0 2>/dev/null | while IFS= read -r -d '' A; do
  echo "  ape file : $(stat -c %s "$A") bytes"
  case "$SCRATCH" in
    /*) ok=1 ;;
    *) echo "  skip (relative scratch)"; ok=0 ;;
  esac
  [ "${ok:-}" = 1 ] || exit 0
  time ffmpeg -y -v error -nostdin -ss 0 -t 10 -i "$A" -map 0:a -vn -c:a flac "$SCRATCH/ape10s.flac"
  echo "  ape out  : $(stat -c %s "$SCRATCH/ape10s.flac") bytes (10 s)"
  ffmpeg -hide_banner -nostdin -i "$A" 2>&1 | grep -E "Duration|Stream" | head -3
  # full-track cost: decode the whole thing once
  time ffmpeg -y -v error -nostdin -i "$A" -map 0:a -vn -c:a flac "$SCRATCH/ape-full.flac"
  echo "  ape full : $(stat -c %s "$SCRATCH/ape-full.flac") bytes"
  exit 0
done
[ -n "${A:-}" ] || echo "  no .ape file found"

echo
echo "=== C. 24-bit round trip"
find /home/user/music/ -iname "*.flac" -print0 2>/dev/null | while IFS= read -r -d '' S; do
  info=$(ffmpeg -hide_banner -nostdin -i "$S" 2>&1 | grep -m1 "Stream #0:0")
  case "$info" in *"s32"*|*"s24"*|*"24*"|*"32"*) ;;
    *) continue ;;
  esac
  echo "  file     : ${S##*/}"
  echo "  stream   : $info"
  ffmpeg -y -v error -nostdin -i "$S" -map 0:a -vn \
    -af "atrim=start_sample=48000:end_sample=691200,asetpts=PTS-STARTPTS" \
    -c:a flac "$SCRATCH/hires.flac"
  H1=$(ffmpeg -v error -nostdin -i "$S" -map 0:a -vn -af "atrim=start_sample=48000:end_sample=691200" -f md5 -)
  H2=$(ffmpeg -v error -nostdin -i "$SCRATCH/hires.flac" -map 0:a -vn -f md5 -)
  echo "  source md5 $H1"
  echo "  extract md5 $H2"
  [ "$H1" = "$H2" ] && echo "  RESULT   : SAMPLE-EXACT" || echo "  RESULT   : MISMATCH"
  ffmpeg -hide_banner -nostdin -i "$SCRATCH/hires.flac" 2>&1 | grep -m1 "Stream #0:0"
  exit 0
done
echo "  no 24-bit file found"
