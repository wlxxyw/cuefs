# cuefs

A read-only virtual filesystem for Linux that presents **single-file album
releases** (one big audio file + a `.cue` sheet) as **individual track files**,
so that Navidrome — or any other player that only understands real files — can
index them normally.

```
HOST                                   Navidrome container
/music/Artist/Album/album.flac   ──┐
/music/Artist/Album/album.cue    ──┤   cuefs (Go, systemd)
                                   ├──► /mnt/music-split/Artist/Album/
                                       ├── 01 - First.flac      ← virtual
                                       ├── 02 - Second.flac     ← virtual
                                       ├── cover.jpg            ← passed through
                                       └── album.cue            ← passed through
                                   -v /mnt/music-split:/music:ro
```

Everything that is not a cue-backed album file is passed through untouched.

> **Status: M1 (splitting + verification).** `split` already produces a
> library that Navidrome can index; the FUSE mount (M2) removes the need for
> the materialised copy. The full design and the decision log live in
> [`docs/DESIGN.md`](docs/DESIGN.md).

## Why not just use trackfs?

[`trackfs`](https://github.com/andresch/trackfs) is the reference implementation
for this idea (its source is vendored under `trackfs/` for study). cuefs keeps
its proven rules — cue tag merging, cover art lookup, next-track preloading —
and changes the parts that hurt:

| | trackfs | cuefs |
|---|---|---|
| `st_size` before materialisation | **estimated**, then changes | never changes (manifest is authoritative) |
| cache | `/tmp` file, 60 s TTL, rebuilt every play | persistent LRU cache, pre-warmable |
| cue encoding | `chardet` best guess only | BOM → UTF-8 → **configured order** → built-in order → scored fallback, with a full report |
| APE | unsupported | `convert` to FLAC (configurable) |
| readdir cost | parses every audio file | cheap sniff, plan built lazily |
| implementation | Python + python-fuse | Go, no cgo |

## Materialising a library (M1)

`split` produces a real directory tree Navidrome can index right away:

```console
$ cuefs split --src /music --out /mnt/music-split --workers 6
plan   : 33 album(s), 388 track(s)
write  : 许巍.../01 - 我的秋天.flac
cover  : 许巍.../cover.jpg
...
33 album(s), 388 track(s): written 388, skipped 0, failed 0, cover(s) 0; passthrough 7568 file(s)
generated 10.0 GiB, passthrough 169.7 GiB, 0 warning(s), 0 error(s)
```

* The output tree mirrors the source tree: split candidates become
  `NN - Title.flac`, the album file itself and its `.cue` are left out, and
  everything else (covers, logs, m3u, already-split albums) is passed through.
* **`--link`** hardlinks the passthrough files instead of copying them -
  zero extra disk when source and output share a filesystem. Caveat: an
  output hardlink **is** the source file, so never write to the output tree.
  Without the flag everything is copied (mode and mtime preserved).
* Idempotent: existing non-empty outputs are skipped, so re-running a
  388-track library takes seconds. `--force` rebuilds.
* `--only <substring>` restricts a run to matching albums (plus all their
  ancestor directories) - handy for testing on a large library.
* Writes are atomic (`*.part` + rename), so an interrupted run never leaves a
  truncated FLAC behind.

Then point Navidrome at it:

```yaml
# docker-compose.yml (excerpt)
volumes:
  - /mnt/music-split:/music:ro
```

## Verifying (M1 acceptance)

`verify` re-decodes every produced track and compares its PCM md5 against the
source window the plan claims it came from:

```console
$ cuefs verify --src /music --out /mnt/music-split
ok     : 许巍.../02 - 在别处.flac
...
checked 388 track(s): OK 388, FAILED 0, MISSING 0
```

That is the strongest guarantee this tool can make: **sample-identical** to
the source, including 24-bit sources and APE transcodes.

## Requirements

* Linux with FUSE (for `mount`; parsing commands are portable)
* `ffmpeg` on `PATH`, or `ffmpeg_bin` set in the config (decision D10)

## Configuration

YAML (decision D9). Search order: `--config <path>` → `$CUEFS_CONFIG` →
`./cuefs.yaml` → `~/.config/cuefs/cuefs.yaml`. Missing file ⇒ defaults.

See [`cuefs.example.yaml`](cuefs.example.yaml) for every key, and
[`docs/DESIGN.md` §5](docs/DESIGN.md) for the schema.

Two settings are worth calling out:

```yaml
cue:
  encoding: auto            # or force one: gb18030 / big5 / ...
  encoding_list: []         # tried in order; empty = built-in order
```

`encoding_list` is the escape hatch for cue files whose encoding cannot be
determined automatically (decision D11). `cuefs probe` prints every candidate
that was evaluated, its score and why it was rejected — use it to decide what
belongs in that list.

## Usage (M0)

```console
$ cuefs scan --all testdata
passthrough tracks=2   big5.cue                                       <- -
passthrough tracks=3   eac-ascii.cue                                  <- -
passthrough tracks=3   gbk.cue                                        <- -
passthrough tracks=2   shiftjis.cue                                   <- -
passthrough tracks=3   utf8.cue                                       <- -

5 cue file(s): 0 split candidate(s), 5 passthrough, 0 error(s), 13 track(s) to expose
```

`scan` classifies every cue file under a directory and is **strictly
read-only** — it stats and reads cue files, nothing else. By default only
split candidates are listed; add `--all` for the full table or `--json` for
machine output.

```console
$ cuefs probe testdata/gbk.cue
file       : testdata/gbk.cue
source     : (none - album file with this basename does not exist)
kind       : passthrough (no-source-audio)
encoding   : gb18030 (origin=builtin, confidence=0.68)
candidates :
  gb18030        builtin    valid      95.9
  big5           builtin    valid      95.9
  shift_jis      builtin    invalid     0.0  contains replacement characters (U+FFFD)
  ...
warnings   :
  - chardet prefers "euc-kr" (conf 84) but the built-in order chose "gb18030" (conf 55); kept the order pick
parsed     : 1 disc(s), 3 track(s) (3 audio)

$ cuefs dump --duration 12:00:00 testdata/gbk.cue
cue    : testdata/gbk.cue
encoding: gb18030 (origin=builtin, confidence=0.68)
album  : 测试专辑
artist : 测试乐队
...
FILE "album.flac" (WAVE)
  duration: 12:00:00 (720.00s)
    01  AUDIO  00:00:00 -> 03:15:20  (195.27s)  第一首歌  [测试乐队]
    02  AUDIO  03:15:20 -> 07:44:05  (268.80s)  第二首歌  [测试乐队; 客串歌手]
    03  AUDIO  07:44:05 -> 12:00:00  (255.93s)  第三首歌
```

Flags go **before** the file argument (standard Go flag parsing):

```console
$ cuefs probe --encoding-list gb18030,big5 --json testdata/big5.cue
```

### Split candidate rule (DESIGN D3)
A cue file is a split candidate only when **both** hold:

1. a sibling audio file shares the cue's basename (extension taken from
   `split.extensions`, matched case-insensitively), and
2. the cue contains exactly one `FILE` statement.

Otherwise it is passed through untouched. This deliberately ignores the names
inside `FILE` statements: those are often written in the cue's own encoding
(GBK bytes on disk where the filename is UTF-8) and frequently point at `.wav`
while the real file is `.flac`. Per-track cues — one `FILE` per already-split
track — fall into the passthrough bucket automatically.

Supported encodings: `utf-8`, `utf-16le`, `utf-16be`, `gb18030`, `gbk`,
`big5`, `shift_jis`, `euc-jp`, `euc-kr`, `windows-1252`, `windows-1251`,
`iso-8859-1`, plus common aliases (`gb2312`, `cp936`, `sjis`, `latin1`, ...).

## Development

```console
$ go test ./...          # unit tests, incl. synthesised cue files in 6 encodings
$ go run testdata/gen.go # regenerate the committed multi-encoding fixtures
```

Cross-compilation is the normal workflow: build locally, ship the binary, and
never ask the target machine to compile (it is far too slow for that).

```console
$ GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
      -o dist/cuefs-linux-arm64 ./cmd/cuefs
```

(`make build-arm64` does the same when `make` is available.)

### Roadmap

| Milestone | Contents |
|---|---|
| **M0** ✅ | cue parser, encoding detection, `probe` / `scan` / `dump` |
| **M1** ✅ | `cuefs split` via ffmpeg + tag/cover writing, sample-exact verification (`verify`) |
| M2 | read-only FUSE overlay (passthrough + tracks), consumable by Navidrome |
| M3 | `hydrate` pre-warming, persistent LRU cache, systemd unit, Navidrome bring-up |
| M4 | pure-Go FLAC frame-level splitting and built-in WAV splitting (zero CPU, zero extra space) |

Acceptance criteria for each milestone are listed in
[`docs/DESIGN.md` §6](docs/DESIGN.md).
