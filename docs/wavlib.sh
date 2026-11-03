#!/usr/bin/env bash
# Synthetic single-file WAV album for the pure-Go (no ffmpeg) split path.
# Usage: wavlib.sh <dir>
set -eu
D="${1:?dir}"
mkdir -p "$D"
ffmpeg -y -v error -nostdin \
  -f lavfi -i "sine=frequency=440:duration=24:sample_rate=44100" \
  -af "volume=0.4" -ac 2 -bits_per_raw_sample 16 -c:a pcm_s16le \
  "$D/album.wav"
cat > "$D/album.cue" <<'CUE'
TITLE "wavtest"
FILE "album.wav" WAVE
  TRACK 01 AUDIO
    TITLE "one"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "two"
    INDEX 01 00:08:00
  TRACK 03 AUDIO
    TITLE "three"
    INDEX 01 00:16:00
CUE
echo "--- album.wav: $(stat -c %s "$D/album.wav") bytes, $(ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 "$D/album.wav") s"
