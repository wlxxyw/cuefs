# cuefs

[English](README.md) | **简体中文** | [完整命令行文档](docs/CLI.zh.md)

一个只运行在 Linux 上的只读虚拟文件系统：把**单文件专辑**（一整轨音频 + 一个 `.cue` 文件）呈现为一首首**独立的单曲文件**，让 Navidrome 或其他只认真实文件的播放器能正常索引。

```
HOST                                   Navidrome 容器
/music/Artist/Album/album.flac   ──┐
/music/Artist/Album/album.cue    ──┤   cuefs（Go，systemd）
                                   ├──► /mnt/music-split/Artist/Album/
                                       ├── 01 - First.flac      ← 虚拟单曲
                                       ├── 02 - Second.flac     ← 虚拟单曲
                                       ├── cover.jpg            ← 原样保留
                                       └── album.cue            ← 原样保留
                                   -v /mnt/music-split:/music:ro
```

不属于 cue 专辑的单轨音频文件，都会原样保留。

> **状态：0.2.0-m4.10，M0–M4 已全部完成。**
> `cuefs mount` 会把整个库挂成一个只读的 FUSE 目录，每首单曲由切分缓存提供，挂载期间还会自动管理缓存。完整的设计与决策记录见 [`docs/DESIGN.md`](docs/DESIGN.md)，所有命令和参数见 [`docs/CLI.zh.md`](docs/CLI.zh.md)。

## 为什么不直接用 trackfs？

[`trackfs`](https://github.com/andresch/trackfs) 是这个思路的参考实现（源码已放在 `trackfs/` 目录里供研究）。cuefs 保留了它已经验证过的规则 —— cue 标签合并、封面查找、提前预读下一首 —— 并改掉了那些用起来难受的地方：

| | trackfs | cuefs |
|---|---|---|
| 生成前的文件大小（`st_size`） | 先估算，之后还会变 | 始终不变（以生成计划为准） |
| 缓存 | 放 `/tmp` 的文件，60 秒过期，每次播放都重建 | 可以持久保存，按最近使用情况自动清理，也可以提前生成 |
| cue 编码 | 只靠 `chardet` 猜 | BOM → UTF-8 → 配置的顺序 → 内置顺序 → 打分择优，并给出完整报告 |
| APE 支持 | 不支持 | 可转换为 FLAC（可选格式） |
| 列目录的开销 | 要解析每个音频文件 | 只做轻量探测，用到时才生成计划 |
| 实现 | Python + python-fuse | Go，无 cgo |

## 生成整库单曲（split）

`split` 会直接生成一棵 Navidrome 能索引的目录树：

```console
$ cuefs split --src /music --out /mnt/music-split --workers 6
plan   : 33 album(s), 388 track(s)
write  : 许巍.../01 - 我的秋天.flac
cover  : 许巍.../cover.jpg
...
33 album(s), 388 track(s): written 388, skipped 0, failed 0, cover(s) 0; passthrough 7568 file(s)
generated 10.0 GiB, passthrough 169.7 GiB, 0 warning(s), 0 error(s)
```

* 输出目录会镜像源目录的结构：能切的专辑变成 `NN - 标题.flac`，专辑整轨文件和它的 `.cue` 不再出现，其余文件（封面、日志、m3u、本来就已经切好的专辑）都原样保留。
* **`--link`** 对原样保留的文件使用硬链接，而不是复制 —— 当源目录和输出目录在同一个文件系统上时，不会额外占用空间。注意：输出的硬链接**就是**源文件，所以千万不要往输出目录里写东西。不加这个参数时，所有文件都是复制（保留权限和修改时间）。
* **`--skip-passthrough`** 跳过所有“不需要切分”的文件（封面之外：日志、m3u、已经切好的目录等），并在汇总里列出跳过了多少（写法是 `passthrough 0 file(s) (N skipped)`），输出树里只留下单曲和封面。适合在大库上只切一小部分做快速本地测试；和 `--link` 同时给时跳过优先。注意：末尾的缓存清理仍然按完整的镜像规则来算，**不会**删除之前完整模式写入的原样保留文件。
* **标签由 cuefs 自己写**，不经过 ffmpeg，所以多值键（`ARTIST`、`GENRE` 等）会写成真正的多值 Vorbis Comment，也不会混进 `encoder: Lavf...` 这类无用信息。
* 可以重复执行：已经存在且非空的输出会跳过，所以 388 首的库再跑一遍只要几秒。需要重建时加 `--force`。
* `--only <关键词>` 只处理名字里包含该词的专辑（连同它们的上级目录），在大库上测试时很方便。
* 写入是原子的（先写 `*.part` 再改名），中途被打断也不会留下损坏的 FLAC。
* 切分完成后会自动清理一次缓存里多出来的文件（规则和 `gc` 相同）。

## 只更新标签，不重新切音频

```console
$ cuefs retag --src /music --out /mnt/music-split      # 只改元数据，不动音频
33 album(s), 388 track(s): written 388, skipped 0, failed 0, cover(s) 0; passthrough 0 file(s)
```

## 挂载（mount）

`cuefs mount` 把库挂到指定的挂载点。它读取的源目录和 `split` 一样，会把每张可切专辑的整轨音频和 cue 文件隐藏起来，然后从缓存目录（也就是 `split` 的输出结构）里提供单曲。缓存里还没有的单曲，会在被读取时按需生成到缓存目录：FLAC/WAV 由内置引擎直接生成，APE 则需要开 `--synthesize` 并配置好 `ffmpeg_bin`。

**默认情况下，挂载不会调用任何外部程序**：WAV 单曲完全用 Go 切（根本不需要解码），FLAC 用内置的纯 Go 引擎；只有 APE 需要 ffmpeg，而且必须在 `--synthesize` 且配置了 `ffmpeg_bin` 时才会调用。

```console
$ cuefs mount --config cuefs.yaml        # 用配置里的 src/mount/cache
mounted : /mnt/music-split -> /srv/music (cache /var/lib/cuefs/cache)
$ cd /mnt/music-split && ls "许巍.../在别处..."
01 - 我的秋天.flac   04 - 树.flac         ...
在别处.jpg           许巍 - 在别处.log     <== 原样保留
```

挂载参数：`--allow-other`（需要 `/etc/fuse.conf` 里有 `user_allow_other`）、`--workers N`（同时生成几首的上限）、`--fuse-opts "…"`。挂载出来的视图是只读的：`mkdir`、`unlink`、`rmdir`、`create`、`symlink`、`rename` 都会被拒绝。

然后把 Navidrome 指向这个挂载点：

```yaml
# docker-compose.yml（片段）
volumes:
  - /mnt/music-split:/music:ro
```

### 缓存的自动管理（m4.9）

挂载期间会运行一个定时任务，按下面的规则管理缓存：

* **源文件变了就重新生成**：cue 文件或音频的修改时间变了，就清掉这张专辑在缓存里的旧文件；但要等这张专辑的所有单曲都超过 5 分钟没被读过才动手，避免正在播放时被删。读取时会按需重新生成。
* **太久没读就清掉**：整张专辑超过 `cache_ttl` 没人访问，就把它的缓存删掉。
* **超出容量就清掉**：缓存总量超过 `cache_max_gb` 时，从最久没访问的专辑开始清，直到降到上限以内。设成 `0` 表示不做容量清理（TTL 仍然生效）。
* 被清掉的单曲，下次被读取时会自动重新生成。想离线清理一遍，用 `cuefs gc`（先加 `-dry` 预览）。

具体参数和日志说明见 [`docs/CLI.zh.md`](docs/CLI.zh.md) 的 `gc` 和 `mount` 两节。

## 检查生成结果（verify）

`verify` 会把每一首生成的单曲重新解码，按计划取它对应的源音频那一段，比较两边的 PCM md5；然后再按计划逐条比对 Vorbis Comment 标签：

```console
$ cuefs verify --src /music --out /mnt/music-split
ok     : 许巍.../02 - 在别处.flac
...
checked 388 track(s): OK 388, FAILED 0, MISSING 0
```

这是这个工具能给出的最强保证：**和源文件逐采样一致**，24 位源和 APE 转出来的单曲也一样。

## 不解码也能查看 FLAC 结构（flacinfo）

```console
$ cuefs flacinfo --cue <x.cue> <x.flac>   # 元数据块 + 帧链
$ cuefs flacinfo --frames 6 <x.flac>      # 再加上前 N 帧的字节范围
```

帧链的采样数总和必须和 STREAMINFO 里记录的总采样数对得上（索引器就是靠这个性质验证的）。在真实专辑上它还会告诉你：**cue 里的切点从来不会正好落在 FLAC 帧边界上** —— 这正是边界帧必须重新编码、不能直接按字节拷贝的原因。

## 运行要求

* Linux + FUSE（只有 `mount` 需要；解析类命令在其他平台也能跑）
* `ffmpeg` 在 `PATH` 里，或者在配置里指定 `ffmpeg_bin`（决策 D10）。只有 APE 转换和封面提取会用到它。

## 配置

使用 YAML（决策 D9）。查找顺序：`--config <路径>` → `$CUEFS_CONFIG` → `./cuefs.yaml` → `~/.config/cuefs/cuefs.yaml`。文件不存在时使用默认配置。

所有配置项见 [`cuefs.example.yaml`](cuefs.example.yaml)，格式说明见 [`docs/DESIGN.md` §5](docs/DESIGN.md)。

有两个设置值得单独说明：

```yaml
cue:
  encoding: auto            # 或强制指定：gb18030 / big5 / ...
  encoding_list: []         # 按顺序尝试；空 = 内置顺序
```

`encoding_list` 是给“自动判断不出编码”的 cue 文件留的应急手段（决策 D11）。`cuefs probe` 会打印每一个尝试过的编码、得分以及被排除的原因，可以用它来决定这里该写什么。

## 基本用法（解析与诊断）

```console
$ cuefs scan --all testdata
passthrough tracks=2   big5.cue                                       <- -
passthrough tracks=3   eac-ascii.cue                                  <- -
passthrough tracks=3   gbk.cue                                        <- -
passthrough tracks=2   shiftjis.cue                                   <- -
passthrough tracks=3   utf8.cue                                       <- -

5 cue file(s): 0 split candidate(s), 5 passthrough, 0 error(s), 13 track(s) to expose
```

`scan` 会给目录下的每个 cue 文件分类，**严格只读** —— 只会读取 cue 文件，不碰其他内容。默认只列出会被切分的专辑；加 `--all` 看完整表格，加 `--json` 输出机器可读的结果。

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

参数要写在文件名**前面**（Go 标准参数解析的规则）：

```console
$ cuefs probe --encoding-list gb18030,big5 --json testdata/big5.cue
```

### 什么样的 cue 会被切分（设计 D3）

只有下面两个条件**同时成立**，cue 文件才会被当成可切分的专辑：

1. 同目录下有一个和 cue 同名的音频文件（扩展名来自 `split.extensions`，匹配时不区分大小写）；
2. cue 里正好只有一个 `FILE` 语句。

否则原样保留。这样规定是故意**不理会 `FILE` 语句里的文件名**：那些名字经常是用 cue 自己的编码写的（磁盘上是 GBK 字节，而实际文件名是 UTF-8），而且经常写着 `.wav`、实际文件却是 `.flac`。每首一个 `FILE` 的“逐轨 cue”（专辑已经切好的情况）会自动归到原样保留那一类。

支持的编码：`utf-8`、`utf-16le`、`utf-16be`、`gb18030`、`gbk`、`big5`、`shift_jis`、`euc-jp`、`euc-kr`、`windows-1252`、`windows-1251`、`iso-8859-1`，以及常见别名（`gb2312`、`cp936`、`sjis`、`latin1` 等）。

## 开发

```console
$ go test ./...          # 单元测试，包含 6 种编码的合成 cue 样本
$ go run testdata/gen.go # 重新生成提交到仓库里的多编码测试样本
```

跨平台交叉编译是常规流程：在本地编译，把二进制传到目标机器，**不要**让目标机器自己编译（它太慢了）。

```console
$ GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
      -o dist/cuefs-linux-arm64 ./cmd/cuefs
```

（有 `make` 时，`make build-arm64` 做的事一样。）

### 里程碑

| 里程碑 | 内容 |
|---|---|
| **M0** ✅ | cue 解析、编码检测、`probe` / `scan` / `dump` |
| **M1** ✅ | `cuefs split`、按采样精确比对的 `verify` |
| **M4** ✅ | 纯 Go FLAC 元数据引擎 + 不解码的帧索引器（`flacinfo`、`retag`），纯 Go WAV 切分 |
| **M2** ✅ | 只读 FUSE 目录（`mount`），由切分缓存提供单曲，缺什么就按需生成 |
| **M4.5–M4.9** ✅ | FLAC 全流程纯 Go、标签合并（以源文件为基底）、WAV 标签容器、索引共享、`fake_streaminfo_md5`、`gc` 与挂载时的缓存任务 |
| M3 | `hydrate` 预热、持久化 LRU 缓存、systemd 单元、Navidrome 联调 |

各里程碑的验收标准见 [`docs/DESIGN.md` §6](docs/DESIGN.md)。
