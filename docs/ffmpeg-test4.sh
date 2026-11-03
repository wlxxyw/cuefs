#!/bin/bash
#!/usr/bin/env bash
# D2: is input -ss/-t sample-exact for FRACTIONAL (non frame-aligned) values?
set -u
SCRATCH="${1:?scratch dir required}"
mkdir -p "$SCRATCH"

first_file() {
  find /home/user/music/ -iname "$1" -print0 2>/dev/null |
    while IFS= read -r -d '' p; do printf '%s\n' "$p"; break; done
}

check() { # file start_samples len_samples
  local f=$1 s=$2 l=$3 rate
  rate=$(ffmpeg -hide_banner -nostdin -i "$f" 2>&1 | grep -m1 -oE "[0-9]+ Hz" | head -1 | awk '{print $1}')
  local dec_start dec_len
  dec_start=$(awk -v s="$s" -v r="$rate" 'BEGIN{printf "%.20g", s/r}')
  dec_len=$(awk -v l="$l" -v r="$rate" 'BEGIN{printf "%.20g", l/r}')
  local a b c d
  a=$(ffmpeg -v error -nostdin -i "$f" -map 0:a -vn -af "atrim=start_sample=$s:end_sample=$((s+l))" -f md5 -)
  b=$(ffmpeg -v error -nostdin -ss "$dec_start" -t "$dec_len" -i "$f" -map 0:a -vn -f md5 -)
  printf '  rate      : %s\n' "$rate"
  printf '  atrim md5 : %s\n' "$a"
  printf '  -ss/-t md5: %s\n' "$b"
  if [ "$a" = "$b" ]; then
    echo "  RESULT    : EXACT"
  else
    echo "  RESULT    : MISMATCH"
  fi
  echo
}

START=3660317      # arbitrary, not frame-aligned
LEN=234567         # arbitrary, not a whole number of seconds

F=$(first_file "*.flac")
echo "=== FLAC fractional -ss/-t   (start=$START len=$LEN)"
check "$F" $START $LEN

A=$(first_file "*.ape")
echo "=== APE  fractional -ss/-t   (start=$START len=$LEN)"
check "$A" $START $LEN

echo "=== end-of-stream sanity: exact last samples"
echo "  (track end must land exactly on the last sample; using a virtue of atrim: no explicit end)"
