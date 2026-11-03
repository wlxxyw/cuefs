#!/bin/bash
# Read-only capability/accuracy test for the splitting engine (M1 pre-check).
# Writes only to the given scratch dir. Usage: ffmpeg-test.sh <scratch-dir>
set -u
SCRATCH="${1:?scratch dir required}"
mkdir -p "$SCRATCH"

find_src() {  # first .flac / .ape album file we can find that has a sibling cue
  find /home/user/music/ -iname "*.cue" -print0 2>/dev/null | while IFS= read -r -d '' c; do
    b="${c%.*}"
    for e in flac ape wav; do
      [ -f "$b.$e" ] && { printf '%s\n' "$b.$e"; return; }
    done
  done | head -1
}

SRC=$(find_src)
echo "src      : $SRC"
SRCLEN=$(stat -c %s "$SRC")
echo "src size : $SRCLEN"

echo
echo "=== 1. stream properties via ffprobe"
ffprobe=$(command -v ffprobe || true)
echo "ffprobe  : ${ffprobe:-MISSING}"
ffmpeg -hide_banner -nostdin -i "$SRC" 2>&1 | grep -E "Stream|Duration" | head -4

echo
echo "=== 2. extract a 5 s window by sample index and re-encode to FLAC"
SR=$(ffmpeg -hide_banner -nostdin -i "$SRC" 2>&1 | grep -oE "sample_rate[^,]*" | head -1)
START=3574657
LEN=220500      # 5 s @ 44.1k
OUT="$SCRATCH/track01.flac"
time ffmpeg -y -v error -nostdin -i "$SRC" -map 0:a -vn \
  -af "atrim=start_sample=$START:end_sample=$((START+LEN)),asetpts=PTS-STARTPTS" \
  -c:a flac "$OUT"
echo "out size : $(stat -c %s "$OUT")"

echo
echo "=== 3. PCM md5: source window vs re-encoded file"
H1=$(ffmpeg -v error -nostdin -i "$SRC" -map 0:a -vn -af "atrim=start_sample=$START:end_sample=$((START+LEN))" -f md5 -)
H2=$(ffmpeg -v error -nostdin -i "$OUT" -map 0:a -vn -f md5 -)
echo "source   : $H1"
echo "extract  : $H2"
if [ "$H1" = "$H2" ]; then echo "RESULT   : SAMPLE-EXACT (lossless round trip)"; else echo "RESULT   : MISMATCH"; fi

echo
echo "=== 4. metadata: clear inherited tags + write ours"
ffmpeg -y -v error -nostdin -i "$OUT" -map_metadata -1 \
  -metadata title="测试轨" -metadata album="测试专辑" -metadata tracknumber="01" \
  -c:a flac "$SCRATCH/tagged.flac"
ffmpeg -hide_banner -nostdin -i "$SCRATCH/tagged.flac" 2>&1 | grep -A12 "Metadata" | head -14

echo
echo "=== 5. duration as reported back"
ffmpeg -hide_banner -nostdin -i "$SCRATCH/tagged.flac" 2>&1 | grep Duration

echo
echo "=== 6. APE availability and cost"
APE=""
for a in $(find /home/user/music/ -iname "*.ape" 2>/dev/null); do
  APE="$a"; break
done
if [ -n "${APE:-}" ]; then
  echo "ape file : $APE ($(stat -c %s "$APE") bytes)"
  time ffmpeg -y -v error -nostdin -ss 0 -t 10 -i "$APE" -map 0:a -vn -c:a flac "$SCRATCH/ape10s.flac"
  echo "ape out  : $(stat -c %s "$SCRATCH/ape10s.flac") bytes (10 s)"
  ffmpeg -hide_banner -nostdin -i "$APE" 2>&1 | grep -E "Duration|Stream" | head -3
else
  echo "no .ape file found"
fi
