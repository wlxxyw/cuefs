#!/usr/bin/env bash
# Compare cuefs' pure-Go decode of a sample window with ffmpeg's, i.e. the
# analogue of verify's REF (pure) versus REF (ffmpeg) for one source file.
#
# usage: cuefs.sh-root...) md5cmp.sh <cuefs> <file.flac> [start end] ...
#   e.g. md5cmp.sh ./cuefs album.flac "0 4096" "4096 8192"
set -u
CUEFS="${1:?path to cuefs binary}"; shift
F="${1:?flac file}"; shift
if [ $# -eq 0 ]; then
  set -- "0 4096" "4096 8192"
fi
# -v error prints ffmpeg's decode complaints, if any, without polluting md5.
for w in "$@"; do
  S=$(echo "$w" | cut -d' ' -f1)
  E=$(echo "$w" | cut -d' ' -f2)
  A=$("$CUEFS" debugmd5 --start "$S" --end "$E" "$F" | tail -1 | awk '{print $3}')
  B=$(ffmpeg -v error -nostdin -i "$F" -map "0:a:0" \
      -af "atrim=start_sample=$S:end_sample=$E,asetpts=PTS-STARTPTS" -f md5 - |
      sed 's/^MD5=//')
  mark="OK"
  [ "$A" = "$B" ] || mark="MISMATCH"
  printf '[%11d,%11d)  go=%s  ffmpeg=%s  %s\n' "$S" "$E" "$A" "$B" "$mark"
done
