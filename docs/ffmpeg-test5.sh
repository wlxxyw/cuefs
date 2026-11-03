#!/usr/bin/env bash
# Final engine shape we intend to use:
#   ffmpeg -ss <floor-seconds> -i SRC -af atrim=start_sample=R[:end_sample=R+LEN],asetpts=PTS-STARTPTS -c:a flac OUT
# Check: (1) fractional start via combo, (2) tail to EOF, (3) 24-bit.
set -u
SCRATCH="${1:?scratch dir required}"
mkdir -p "$SCRATCH"

first_file() {
  find /home/user/music/ -iname "$1" -print0 2>/dev/null |
    while IFS= read -r -d '' p; do printf '%s\n' "$p"; break; done
}

combo() { # file start_samples len_samples  (-- when len<0: to EOF)
  local f=$1 s=$2 l=$3 rate whole rem dec a b af
  rate=$(ffmpeg -hide_banner -nostdin -i "$f" 2>&1 | grep -m1 -oE "[0-9]+ Hz" | head -1 | awk '{print $1}')
  whole=$((s / rate))
  rem=$((s - whole * rate))
  dec=$(awk -v w="$whole" 'BEGIN{printf "%.20g", w}')
  if [ "$l" -lt 0 ]; then
    af="atrim=start_sample=$rem,asetpts=PTS-STARTPTS"
  else
    af="atrim=start_sample=$rem:end_sample=$((rem+l)),asetpts=PTS-STARTPTS"
  fi
  a=$(ffmpeg -v error -nostdin -i "$f" -map 0:a -vn -af "atrim=start_sample=$s:end_sample=$((s+l))" -f md5 - | head -1)
  b=$(ffmpeg -v error -nostdin -ss "$dec" -i "$f" -map 0:a -vn -af "$af" -f md5 - | head -1)
  printf '  baseline : %s\n  combo    : %s\n' "$a" "$b"
  [ "$a" = "$b" ] && echo "  RESULT   : EXACT" || echo "  RESULT   : MISMATCH"
  echo
}

START=3660317
LEN=234567

F=$(first_file "*.flac")
echo "=== FLAC  combo, fractional start"; combo "$F" $START $LEN
echo "=== FLAC  combo, whole second";    combo "$F" 3660300 $LEN

A=$(first_file "*.ape")
echo "=== APE   combo, fractional start"; combo "$A" $START $LEN

echo "=== FLAC  24-bit combo"
find /home/user/music/ -iname "*.flac" -print0 2>/dev/null |
while IFS= read -r -d '' S; do
  info=$(ffmpeg -hide_banner -nostdin -i "$S" 2>&1 | grep -m1 "Stream #0:0")
  case "$info" in *"(24 bit)"*|*"24 bit"*) ;;
    *) continue ;;
  esac
  echo "  file     : ${S##*/}"
  echo "  stream   : $(printf '%s' "$info" | sed 's/  */ /g')"
  combo "$S" 2304000 192000   # 48 s @ 48k, 4 s len
  exit 0
done
