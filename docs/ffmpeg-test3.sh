#!/bin/bash
# A: vorbis comment key case.  D: is input -ss sample-exact too? (required for APE)
set -u
SCRATCH="${1:?scratch dir required}"
mkdir -p "$SCRATCH"

first_file() {  # first library file matching $1 (case-insensitive), NUL-safe
  find /home/user/music/ -iname "$1" -print0 2>/dev/null |
    while IFS= read -r -d '' p; do printf '%s\n' "$p"; break; done
}

echo "=== A. do -metadata keys survive verbatim into the FLAC?"
SRC=$(first_file "*.flac")
OUT="$SCRATCH/tag.flac"
ffmpeg -y -v error -nostdin -i "$SRC" -map 0:a -vn -map_metadata -1 \
  -metadata "TITLE=Mixed Case" -metadata "tracknumber=99" \
  -metadata "ARTIST=A" -metadata "ALBUMARTIST=B" \
  -c:a flac "$OUT"
for k in TITLE tracknumber TRACKNUMBER TRACKNUM ARTIST ALBUMARTIST COMPOSER; do
  if grep -qa "$k" "$OUT"; then echo "  present  : $k"; else echo "  absent   : $k"; fi
done
echo "  ffmpeg reports back:"
ffmpeg -hide_banner -nostdin -i "$OUT" 2>&1 | sed -n '/Metadata:/,/Stream #/p'

echo
echo "=== D. input -ss/-t vs atrim (sample-exactness)"
START=3660300   # exactly 83.0 s @ 44.1 kHz
LEN=220500      # exactly 5.0 s

FLAC=$(first_file "*.flac")
echo "  flac src : ${FLAC##*/}"
B=$(ffmpeg -v error -nostdin -i "$FLAC" -map 0:a -vn -af "atrim=start_sample=$START:end_sample=$((START+LEN))" -f md5 -)
C=$(ffmpeg -v error -nostdin -ss 83 -t 5 -i "$FLAC" -map 0:a -vn -f md5 -)
echo "  atrim md5    : $B"
echo "  -ss/-t md5   : $C"
[ "$B" = "$C" ] && echo "  RESULT   : INPUT -ss IS SAMPLE-EXACT for FLAC" || echo "  RESULT   : MISMATCH for FLAC"

APE=$(first_file "*.ape")
if [ -n "${APE:-}" ]; then
  echo "  ape src  : ${APE##*/}"
  # baseline via atrim (decodes from byte 0, cheap: only the first 88 s)
  A=$(ffmpeg -v error -nostdin -i "$APE" -map 0:a -vn -af "atrim=start_sample=$START:end_sample=$((START+LEN))" -f md5 -)
  E=$(ffmpeg -v error -nostdin -ss 83 -t 5 -i "$APE" -map 0:a -vn -f md5 -)
  echo "  atrim md5    : $A"
  echo "  -ss/-t md5   : $E"
  [ "$A" = "$E" ] && echo "  RESULT   : INPUT -ss IS SAMPLE-EXACT for APE" || echo "  RESULT   : MISMATCH for APE"
fi
