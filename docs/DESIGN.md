# cuefs 设计备忘（Design Memo）

> 状态：已收敛，进入实现阶段
> 参考实现：`./trackfs`（andresch/trackfs v0.2.6，Python/FUSE，LGPL-3.0）
> 本文档是实现过程中的**决策记录**，实现与文档不一致时以本文为准，并回写更新。

---

## 0. 目标

把「整轨音频 + .cue」在 Linux 上呈现为一个**只读虚拟文件系统**，每个分轨一个文件，
交给 Navidrome（Docker 内）作为音乐库；非目标文件 100% 透传。

```
HOST:  cuefs 挂载 ~/music → /mnt/music-split
       docker: -v /mnt/music-split:/music:ro
Navidrome: 正常扫描 /music
```

---

## 1. 已确认的决策（用户拍板）

| # | 决策 |
|---|---|
| D1 | 架构走**混合方案**：持久缓存 + 只读 FUSE overlay；仅考虑 Linux |
| D2 | Navidrome 跑 Docker，cuefs 跑 host，结果目录挂进容器 |
| D3 | 格式：FLAC 为主、部分 WAV、**极少 APE**；非整轨文件一律透传，只处理与 cue 同名的整轨文件 |
| D4 | CUE 编码是核心痛点，必须支持多编码检测（`file -i` 不可靠，enca 亦不一定对） |
| D5 | 允许依赖外部工具：`ffmpeg`（必需）、可选 `enca` 等 |
| D6 | APE 允许多存一份分轨（可配置项） |
| D7 | 语言 **Go**（无 cgo） |
| D8 | 规模：50–100 张 cue 专辑（≈1500 个虚拟文件），设计留 10× 余量 |
| D9 | 配置文件格式 **YAML** |
| D10 | `ffmpeg_bin` 可配置，默认从 `PATH` 查找 |
| D11 | CUE 编码**候选列表可配置，按配置顺序尝试**；无配置或全部失败时用内建顺序 |
| D12 | 虚拟文件名用 `01 - Track.flac`（非 trackfs 的 `album.#-#.01.Title.flac`） |
| D13 | FLAC 分轨 **先用 ffmpeg**，流程通畅后再实现 Go 原生（帧级流拷贝） |
| D14 | APE 默认 **转为 FLAC**（`convert` 模式） |
| D15 | **源库只读（硬约束）**：`src` 下的任何文件一律不删除、不修改、不移动，整轨原文件必须原样保留。所有产出（缓存、分轨文件、挂载视图）只能写到 `src` **之外**；FUSE 视图必须是只读挂载，引擎对源文件只用 `open(O_RDONLY)` |

---

## 2. 从 trackfs 源码读出的事实（参考，不照抄）

| 方面 | trackfs 做法 | 我们的取舍 |
|---|---|---|
| 分轨 | `flac -d --skip --until \| flac -o`（解码重编码）；WAV 用 `wave` 模块切段再编码 | 保留"解码重编码=样本精确"的正确性，但用 ffmpeg 统一实现 |
| 缓存 | `/tmp` 临时文件 + 引用计数 + 60s TTL，**无持久缓存** | 改为**持久 LRU 缓存**，可预热 |
| `getattr` size | 未物化时返回**估算值**（原始 PCM 大小），物化后变真实值 → **size 漂移** | **绝对不允许漂移**：size 在 manifest 生命周期内固定（见 §5.3） |
| readdir | 对每个音频文件跑 mutagen 全量解析来判断有无 cue | 改为轻量嗅探 + 延迟到 Plan 阶段 |
| cue 识别 | FLAC 内嵌 `CUESHEET` 优先，否则同基名 `.cue` | 保留该优先级 |
| 时间换算 | `seconds()` 把 CD 帧 ff 当 **1/100 秒**（实为 1/75），靠 `flac_time()` ×100/75 绕回 | 用整数有理数运算，不复刻该坑（见 §4.1） |
| tag 合并 | 音频文件 tag 为基底；cue `PERFORMER→ARTIST`、`SONGWRITER→COMPOSER`、轨级 `TITLE` 覆盖；缺 `ALBUM`/`ALBUMARTIST` 时由 `TITLE`/`ARTIST` 补；`;` 分割多值 | **照抄这套规则**（已对 Navidrome 调好） |
| 忽略 tag | `CUE_TRACK.*\|COMMENT` | 保留，可配置 |
| 封面 | 内嵌 PICTURE 提取优先，否则 `folder.jpg` / 同基名 `.jpg` | 照抄；**不**往每轨塞内嵌图（20 轨 × 数百 KB 纯浪费） |
| 预加载 | 读到距轨尾 ~30s 时后台切下一轨 | 保留（仅对 cache-miss 场景） |

---

## 3. 架构

```
                    HOST (Linux)
┌──────────────────────────────────────────────────────────────────┐
│  cuefs（Go 单二进制，systemd 常驻）                               │
│                                                                  │
│  ┌──────────┐   ┌───────────┐   ┌──────────────┐   ┌──────────┐  │
│  │ cue 解析  │──►│ TrackPlan │──►│ SplitEngine  │──►│  Track   │  │
│  │ +编码检测 │   │ 尺寸/标签 │   │ ffmpeg 外调  │   │  Cache   │  │
│  └──────────┘   │ /帧边界   │   └──────────────┘   │ (LRU 盘) │  │
│        ▲        └───────────┘                       └──────────┘  │
│        │                  ▲                                      │
│  ┌─────┴─────────────────────────────────────────────┐           │
│  │ 只读 overlay FUSE（hanwen/go-fuse/v2 `fs` API）    │           │
│  │  track 节点 → 缓存/合成流                           │           │
│  │  其他节点   → Loopback 透传                        │           │
│  └───────────────────────┬───────────────────────────┘           │
└──────────────────────────┼───────────────────────────────────────┘
                           │ mount src → mountpoint
                    ┌──────┴───────┐
                    │  Navidrome   │  -v mountpoint:/music:ro
                    └──────────────┘
```

分层：**Plan（计划）** 与 **Engine（分轨）** 解耦，FUSE 只是前端之一。
子命令 `probe / dump / split / hydrate / mount / validate` 全部复用同一核心库。

---

## 4. 核心子系统

### 4.1 CUE 解析

- 行式解析 + 引号感知分词（不用 Lark 那种重型 grammar，cue 语法极简）。
- 支持：`REM`、`CATALOG`、`PERFORMER`、`TITLE`、`SONGWRITER`、`FILE`、`TRACK`、
  `INDEX`、`PREGAP`、`POSTGAP`、`ISRC`、`FLAGS`。
- **时间换算（整数，勿用浮点）**：
  ```
  总 CD 帧数 frames = mm*4500 + ss*75 + ff        // 75 帧/秒
  采样点 samples   = round(frames * sampleRate / 75)
                   = (frames*sampleRate + 37) / 75  // int64 有理数，无溢出
  ```
  FLAC/WAV 的样本边界用此式。**禁止** `ff/100.0` 这类近似。
- 轨区间：`start = INDEX 01`，`end = 下一轨 INDEX 01`（同 FILE 内），末轨 = 音频总时长。
  `INDEX 00` 记为 pregap 归前一轨；`PREGAP`/`POSTGAP` 需生成真实静音样本（M1 后）。
- 多 `FILE`：按 FILE 分组，一个 cue 可含多碟。
- 数据轨（`MODE1/2352` 等）跳过；HTOA（轨 0 / INDEX 00 起始）单独处理。

### 4.2 CUE 编码检测（D11）—— 已实现（M0）

实现于 `internal/cue/encoding.go`，实际执行顺序：

1. **BOM**：UTF-8 / UTF-16LE / UTF-16BE → 直接定（`origin=bom`）。
2. **严格 UTF-8**：`utf8.Valid` 通过 → `utf-8`（`origin=utf8`）。
3. **BOM-less UTF-16 嗅探**：按每对字节中一侧 NUL 的占比判定 LE/BE；
   通过则直接返回，否则继续。
4. **配置列表**（`cue.encoding_list`，按**配置顺序**）：逐个尝试，
   **第一个"有效"的获胜**（`origin=configured`）。用户断言了顺序，就不再用
   启发式去反驳它。
5. **内建顺序**（配置为空或全部失败后，`origin=builtin`）：
   `gb18030 → big5 → shift_jis → euc-jp → euc-kr → windows-1252 → windows-1251`。
   这里追加一条**chardet 平局裁决**（见下）。
6. 仍全部失败 → **打分兜底**：在全部候选中按分数取最高（`origin=fallback`），
   无论是否有效都返回，并记 warning。

**"有效"判据**（任一不满足即失败）：
- 解码结果不含 `U+FFFD`；
- 不含 NUL；
- 控制字符（除 `\t\n\r`）占比 ≤ 2%；
- 原文含 ≥0x80 字节时，解码结果必须含 ≥0x80 的 rune；
- 必须能识别出 cue 结构关键字（`TRACK` / `INDEX` / `FILE` 之一）。

**打分**（用于第 6 步兜底与 `probe` 展示）：
`cue 关键字命中 ×10 + 结构关键字 ×5 + 非 ASCII 字符数（上限 400）/40`
（纯 ASCII 文本固定 +20），并对"疑似损坏 UTF-8"的源文施加
单字节西文码页 **−60** 惩罚。

**chardet 平局裁决（实测驱动的设计）**

内建顺序单独使用时，GBK / Big5 已能各自判对（两者互相解码都会产生 U+FFFD），
但 **Shift-JIS / EUC-JP 文本会被 GB18030 "合法地"解成乱码**，顺序必然选错；
反过来 chardet 单独使用时，其 EUC-KR 识别器对中文文本**过度触发**
（实测 GBK 样本：EUC-KR=88 vs GB-18030=58），而对真正的 EUC-KR 又只给 10。

因此只在两种情况下让 chardet 推翻顺序：

```
chardet 对某候选的置信度 ≥ 60   且   顺序首選的 chardet 置信度 ≤ 40
```

实测结果：GBK → 顺序保持（58 > 40，不推翻）；Shift-JIS → chardet 推翻（100 vs 10）；
EUC-JP → 推翻（100 vs 10）；Big5 → 顺序首選即 big5，无需推翻。
若 chardet 更偏爱别的候选但未达推翻条件，会以 **advisory warning** 输出，
`probe` 的 candidates 表同时给出全部候选与分数。

逃生舱：`cue.encoding: <name>` **强制指定**（跳过检测）；
目录级 override `cue_overrides: {"/path": {encoding: big5}}`。

- 依赖：`golang.org/x/text`（decode/encode）、`github.com/saintfish/chardet`（裁决）。
- 已知局限：GBK / Big5 / EUC-* 在字节层面有天然歧义，短文本可能错判 ——
  因此**配置顺序 + `probe` 可视化**是主要手段；EUC-KR 自动识别不可靠，
  需要时用 `encoding` 或 `encoding_list` 显式指定。

### 4.3 分轨引擎（三档）—— M1(audio)+M4(元数据/索引) 已实现并实测

**FLAC/WAV/FLAC-out 的音频统一走 ffmpeg**，最终命令形态（逐字节验证过）：

```
ffmpeg -ss <floor(seconds)> -i SRC        -map 0:a:0 -vn -map_metadata -1 -map_chapters -1        -af "atrim=start_sample=R[:end_sample=R+LEN],asetpts=PTS-STARTPTS"        -t <LEN/rate + 1s>        -c:a flac -f flac -compression_level 5        <out>.part        # 写完改名，中断不会留下半截文件
```

其中 `R = start - floor(seconds)*rate`，`LEN = end - start`（末轨无 end，
`atrim` 只给 `start_sample`，自然切到 EOF）。**tag 不由 ffmpeg 写**：cuefs 自己
在 Go 里重写元数据（见下），这样可以表达真正的多值 Vorbis Comment。

**已实测的精确性结论**（远端 ffmpeg d90e3a1 / lavf 62.12.102）：

| 检查 | 结果 |
|---|---|
| `atrim=start_sample/end_sample`（整数采样）+ FLAC 重编码 | **逐采样精确**（PCM md5 与源区间一致） |
| 16-bit/44.1k FLAC | 精确；约 4 s / 4 分钟轨 |
| 24-bit/48k FLAC（`s32 (24 bit)`） | 精确，**位深保持** |
| 输入侧 `-ss`（整数秒 / **分数秒**）与 `-t`（分数长度） | 精确（FLAC 与 APE 均验证） |
| APE 解码 | 可用（native）；70 min 整轨 → FLAC 用时 74 s，**体积 45 MB → 470 MB**（此样本 APE 压缩率异常高，值得复查文件本身） |

实现要点：

1. **`atrim` 是精确性的权威**，`-t`（+1s 冗余）只是让管线提前停止，
   避免中段轨道解码到文件尾 —— 有它没它输出字节完全一致，但耗时差 ~5x；
2. 必须显式 `-f flac`：输出先写成 `.part`，扩展名猜不出 muxer；
3. `-map 0:a:0` 只取第一条音轨，`-vn` 丢弃内嵌封面（封面走目录透传）。

> 为什么 FLAC 不用 `-c copy`：cue 时间是 CD 帧（1/75 s = 588 @44.1k），
> 与 FLAC 帧边界不对齐；`-c copy` 会 seek 到最近帧边界，轨边界误差可达一帧
> （4096 采样 ≈ 93 ms），连播会有可闻的重叠/缺口。另外 M1 曾用 ffmpeg 的
> `-metadata` 写 tag；M4 起改为全由 Go 写（见 §4.3.1），多值键才能真正表达。

#### 4.3.1 M4：纯 Go FLAC 引擎（元数据手术 + 帧索引器）

`internal/audio`：

- **`ReadMetaBlocks` / `WriteVorbisComment`**：元数据手术。只重写元数据块、
  原样拷贝帧数据；多值键（`ARTIST/GENRE/...`，见 `plan.MultiValueKeys`）写成
  多条 Vorbis Comment；`encoder: Lavf` 这类噪音消失；PADDING 收敛到 4 KiB
  （保留以备未来就地改 tag）；原子重写（.part + rename）。
  → `cuefs retag` 只重写元数据（不碰音频），实测 388 轨 / 10 GiB 用时 6:49
  （I/O 为主），之后 `verify` 388/388 通过（PCM md5 + **tag 逐条比对**）。
- **`IndexFlacFrames`**：无解码的帧索引器。解析帧头（CRC-8 校验）→ 用整帧
  CRC-16 确认帧边界 → 严格递推的帧链（复杂度 O(file)，实测 30,850 帧 / 306 MB
  约 5 s）。附带 STREAMINFO 解析、RF64/RIFF 的 WAV 解析与纯 Go 切分
  （`internal/audio/wav.go`，按 BlockAlign 字节切片，**零解码零 CPU**，
  单测覆盖；本库没有 WAV 整轨样本）。
  帧头语义与 mewkiz/ffmpeg 解码器逐表核对（M4.5 修正）：
  - block size code：`0001=192`、`0010-0101=576·2^(n-2)`、`0110=8bit 字段`、
    `0111=16bit 字段`、**`1000-1111=256·2^(n-8)`（无字段）**，`0000=reserved`
    不出现。早期版本把 1000-1111 当"取自 STREAMINFO"是错误的——真实文件里
    code 12 ↔ 4096、code 10 ↔ 1024 恰好让该误读在 Σ 校验下偶发吻合，
    尾部差异可达一个 block（3072 采样）；
  - UTF-8 编码的编号字段：fixed 策略=帧号（ffmpeg 从 0 起）、variable=样本号；
  - 计数 Σ 与 STREAMINFO.total_samples 允许不一致（CRC 锚定的链是磁盘事实，
    muxer 记账可能有 ≤1 block 的出入），flacinfo 会打印差异但不算错误。

#### 4.3.2 M4.5：FLAC 分轨全程纯 Go（去 ffmpeg）

`flacsplit.go` + `flacwrite.go`（`WriteFLACTrack`）：

1. **字节拷贝**：窗口内完整帧原样拷贝——帧头按「fixed 策略=帧号」时重写为
   「variable 策略=样本号」（首 6 位变成 sync+样本号），CRC-8/CRC-16 重算，
   音频字节零改动（源文件帧号在新流里无意义，且 ffmpeg 拒绝重编号）。
2. **边界帧 VERBATIM**：至多两个跨窗口边界的帧整帧用 mewkiz 解码后以
   VERBATIM 子帧重编码（可变阻塞 + 显式 16bit 块长字段，块长取帧内窗口实际
   采样数），帧序号写输出流内的样本位置。RAW PCM 数据内假同步密度实测可达
   1/70 字节（单帧内 134 个），所以用 CRC-16 逐候选锚定边界，不用启发式。
3. **STREAMINFO**：输出流的真实 MD5 在解码窗口时同步计算写入（和 ffmpeg
   `-f md5` 同口径：interleaved LE、Bits/8 字节），验证端可闭环。
4. **引擎分派**（`engine/writer.go` `WriteTrack`）：`flac→flac`、`wav→wav`
   纯 Go；`ape→flac` 走 ffmpeg（**ffmpeg 从此只服务 APE**）；其余组合报错。
   `flac→flac` 若遇到覆盖窗口之外的帧链死点（个别源文件中段有局部损坏，
   ffmpeg 靠重同步容错），自动落 ffmpeg fallback 保产出正确。
5. **verify 参考模式**（`--ref auto|ffmpeg|pure`）：`auto` 有 ffmpeg 用 ffmpeg
   （独立实现，最强检查）；`pure` 走 mewkiz 自检——REF 解源窗口，产端把输出
   剪到与参考相同的 span 再比（外部写入可以覆盖更多样本）；APE 没有 pure
   解码器，`--ref pure` 直接给出明确拒绝信息。
- **实证**：《许巍 - 在别处》（306 MB / 30,850 帧 / 41 分钟）：

```
  case size   : 3696..4096 samples       <- 末帧是短帧(bsCode 7, 显式 3696)
  total samples: 126361200 (frame chain) == 126361200 (STREAMINFO)   <- 一致
trailer : 0 byte(s) after the last frame
  01  start          0 -> frame 1/30850   aligned
  02  start   12083400 -> frame 2951  off by 200 sample(s) (4.5 ms)
  ...
  10  start  114887556 -> frame 28049  off by 2948 sample(s) (66.8 ms)
```

  帧链 Σ == STREAMINFO 且 trailer == 0 → 索引器逐帧解析正确（M4.5 修正后：
  1000-1111 按 `256·2^(n-8)` 取样本数，Σ 与多种 muxer 的输出都严格一致）。

**M4 的关键结论（决定 FLAC 路线）**：同一报告同时给出
"cue 边界是否落在帧边界"，实测 **10/10 个 cue 边界都不落在 FLAC 帧边界上**
（偏差 1144..3472 采样 ≈ 25–79 ms）。这正是"纯字节拷贝切 FLAC 不可行"的
数据证据：不对边界帧重编码的话，轨内会带入前/后一轨 25–79 ms 的音频。

M4 当时以为 FLAC 必须保留 ffmpeg 解码重编码；M4.5 推翻：边界帧无须压缩——
用 **VERBATIM 子帧**（规范内最平凡、占字节最多但完全合法）重编码即可，
ffmpeg 从 FLAC 路径退役为 APE 专用。帧索引器继续作为 FUSE 流式合成的地基
（M2 复用）。

配置项 `split.ape_mode: convert | hide | passthrough`（默认 `convert`），
`split.compression_level: 0-8`（默认 5）。

### 4.4 Plan 与尺寸一致性（trackfs 最大硬伤的修正）

原则：**一个虚拟文件的 size 在其 manifest 生命周期内绝不变化。**

- `TrackPlan` 在挂载/扫描时建立，包含：每轨样本区间、输出字节数、完整 tag 集、封面路径、
  `sourceHash = f(src mtime+size, cue hash, schemeVersion)`。
- FLAC/WAV：输出字节数在 Plan 阶段即可确定（M1 落盘后回填；M4 改为解析式预算）。
- APE：`getattr` 阻塞 hydrate（数量少，首次扫描阻塞可接受），加锁防并发重复转码。
- `mtime = max(src mtime, cue mtime)` 的确定性映射，**不用当前时间**。
- `schemeVersion` 变更 → 全量失效重算，size 一起翻新 → Navidrome 自动重扫。

### 4.5 缓存与预热

```
<cache>/
  manifest/<key>.json     # TrackPlan + schemeVersion + sourceHash
  tracks/<key>/NN-Title.flac
```

- LRU：`cache_max_gb` + `cache_ttl`（Go duration）；APE 桶不参与 LRU 驱逐。
- `cuefs hydrate --workers N` 批量预热（100 张专辑 ≈ 1500 轨，可全预热）。
- 单轨 `singleflight` 防重复；读到距轨尾 ~30s 预热下一轨（沿用 trackfs 思路）。

### 4.6 分轨触发判定（D3/D15）—— 已实现（M0）

实现于 `internal/library`，命令 `cuefs scan`。

**规则**：一个 cue 文件同时满足以下两条才是分轨候选，否则**完全透传**：

1. 同目录下存在**同基名**的音频文件，扩展名取自 `split.extensions`（大小写不敏感）；
2. cue 里**恰好一条 `FILE` 语句**。

**必须忽略 `FILE` 语句里的文件名**，实测原因有三：

- `FILE` 里的名字用 **cue 自身的编码**写（GBK 字节），而磁盘文件名是 UTF-8，
  两者字节不同，无法直接匹配；
- `FILE` 常写 `.wav` 而实际文件是 `.flac`；
- 已分轨的目录里，每个轨一个 `FILE`，指向根本不存在的源文件。

因此源文件一律以**同名兄弟文件**为准，`FILE` 只用于确定轨边界。

**只读保证（D15）**：`scan` / `probe` / `dump` 只 `ReadDir` + `ReadFile`，
绝不写入源库；有专门的单元测试 `TestScanIsReadOnly` 对扫描前后整棵目录树
做内容快照比对，防止回归。

**真实库实测**（47 个 cue 文件）：

| 结果 | 数量 |
|---|---|
| 分轨候选 | **33**（28 个 FLAC、1 个 APE、其余 WAV→实际为 FLAC） |
| 透传（每轨一 FILE 的已分轨 cue） | 14 |
| 解析失败 | 0 |
| **待生成的虚拟轨** | **388**（33 张专辑）+ 已分轨 137 轨（不需要处理） |
| 编码分布 | gb18030 ×42、utf-8 ×8 |
| chardet 推翻内建顺序的次数 | **0**（47/47 全部由顺序规则判定正确） |

> 历史注记：早期版本把已分轨 cue 的轨数也计入了"待暴露"，得出 525；
> 现在 `Summary.Add` 只统计分轨候选的轨数（这才是 cuefs 要生成的东西）。

另外观察到的两件事，留给后续里程碑：

- 部分专辑 tag 含多余空白（如 `ALBUM = "她说   CD2"`）→ M1 写 tag 时归一化；
- 一轨一个 `FILE` 的 cue 数量不少（14/47），说明"分轨判定"比"有没有 cue"
  更接近真实需求，规则必须保持窄。

### 4.9 物化模式（M1 已实现）：`split` + `verify`

FUSE 之前的可用交付物：`cuefs split --src SRC --out OUT` 产出一个**可直接给
Navidrome 的真实目录树**。

布局规则：OUT 完整镜像 SRC 的目录结构，然后

- 分轨候选的整轨文件 → 生成 `NN - Title.flac`（388 轨实测全过 verify）；
- 该目录里的 cue 文件 → **不复制**（Navidrome 不认，只会造成困惑）；
- 其他所有文件（已分轨音频、cover/log/m3u/booklet/txt）→ **透传**：
  默认**复制**，`--link` 时改硬链接（同文件系统零额外空间）；
- 目录里没有任何图片但整轨 FLAC 内嵌了封面 → 额外生成 `cover.jpg`
  （每专辑一次 `ffmpeg -map 0:v:0 -frames:v 1`）。

行为细节：

- **幂等**：已存在且非空的输出文件直接跳过（重跑 388 轨全库 5.9 s），
  `--force` 强制重建；
- **原子写**：先写 `<name>.part` 再 rename，中断不会留下半截 FLAC；
- **`--only <子串>`**：只处理匹配的专辑，且自动保留其全部祖先目录 ——
  用于在大库上做小范围验证；不指定时镜像整棵树；
- **`--link` 的代价与风险**：透传文件与源文件是**同一个 inode**，
  修改/删除 OUT 里的文件会等价地改到 SRC。OUT 只读使用时这是最优解；
  需要绝对安全时用默认的复制模式（实测全库透传约 170 GiB）；
- 复制保留源文件的 mode 与 mtime（扫描器指纹一致）。

`cuefs verify` 用同一套 plan 逐轨重解码，对比 `OUT 文件 PCM md5` 与
`SRC 对应窗口 PCM md5` —— 这是 M1 的验收命令，实测 **388/388 通过**。

### 4.10 FUSE（仅 Linux）

- 库：`github.com/hanwen/go-fuse/v2`，`fs` API。
- 目录节点 = 源目录 readdir ∪ 分轨虚拟文件；文件节点分两类：
  - **透传**：`LoopbackFile`，stat/open/read 直接走源路径；
  - **track**：读走缓存文件（`LoopbackFile`）或合成流（M4）。
- 挂载选项：只读、`allow_other`、`default_permissions`。
- 运维：**先 mount 再启容器**（Docker 默认 `rprivate` 传播，容器起来后再 mount 不可见）；
  若需运行期 catch mount 则用 `:rslave`。
- systemd unit + `Restart=always`；cuefs 挂了挂载点会 hang。

### 4.11 命名（D12）

- 模板：`{num:02d} - {title}{ext}`（默认，`{ext}` 由 `output_format` 决定），`title_max_len: 60`。
- 清洗：去 `/` 与控制字符、NUL → `_`、去尾部 `.`/空格、空标题回退 `NN.flac`、重名加 `(2)`。
- `keep_album: false`（可开）保留整轨原文件。
- **命名模板一旦上线不再变更**（改名 = Navidrome 全库重建，播放记录/收藏要重挂）。

---

## 5. YAML 配置（D9/D10/D11）

```yaml
# ~/.config/cuefs/cuefs.yaml
src: /srv/music                      # 源库根（mount 必需）
mount: /mnt/music-split              # 挂载点（mount 必需）
cache: ~/.local/share/cuefs/cache    # 持久缓存根
cache_max_gb: 20
cache_ttl: 168h                      # Go duration

ffmpeg_bin: ffmpeg                   # D10：可配绝对路径，默认 PATH 查找
ffprobe_bin: ffprobe                 # 同上（可选，仅诊断用）
workers: 4                           # hydrate/分轨并行度

name_template: "{num:02d} - {title}{ext}"
title_max_len: 60
keep_album: false

split:
  extensions: [".flac", ".wav", ".ape"]
  output_format: flac                # flac | wav（wav 输出仅支持 WAV 源，纯 Go 零 CPU）
  ape_mode: convert                  # convert | hide | passthrough
  compression_level: 5               # FLAC 压缩等级 0-8（交给 ffmpeg）

cue:
  encoding: auto                     # auto | gb18030 | big5 | ... （强制指定）
  encoding_list: []                  # D11：按顺序尝试；空 = 用内建顺序
  cue_overrides: {}                  # 目录级编码覆盖 {"/path": {encoding: big5}}
  ignore_tags: ["CUE_TRACK*", "COMMENT"]

log_level: info                      # debug | info | warn | error
```

配置优先级：CLI flag > YAML > 默认值。查找顺序：
`--config <path>` → `$CUEFS_CONFIG` → `./cuefs.yaml` → `~/.config/cuefs/cuefs.yaml`。

---

## 6. 里程碑与验收

| 阶段 | 内容 | 验收标准 |
|---|---|---|
| **M0** ✅ 已完成 | cue 解析 + 编码检测 + `cuefs probe` / `scan` / `dump` | ✅ 6 种编码的合成样本全部解析正确（GBK/Big5/Shift-JIS/UTF-8/UTF-16 BOM/UTF-16 无 BOM/纯 ASCII）；✅ **真实库回归**：47 个 cue 全部解析成功（33 分轨候选 / 14 透传 / 0 错误 / 525 轨），编码判定 42×gb18030 + 5×utf-8，chardet 零次误推翻；✅ 扫描前后源库文件数与内容逐字节不变（`TestScanIsReadOnly`）。**待补**：其余里程碑遇到的坏 cue 样本持续回归 |
| **M1** ✅ 已完成 | `cuefs split` / `verify`（ffmpeg 路线）+ tag/封面写入 | ✅ **388/388 轨逐采样一致**（PCM md5 与源区间完全相等，含 24bit 与 APE）；✅ 16/44.1、24/48 位深保持；✅ 幂等（重跑 5.9 s，0 重写）；✅ 源库 7648 文件前后逐字节不变；✅ tag 键大写原样写入（`TRACKNUMBER`/`TRACKTOTAL`/`ALBUMARTIST`…） |
| **M2** ✅ 已完成 | FUSE 挂载（缓存驱动 + 按需合成，无 ffmpeg 依赖） | 原有测试通过 + 新增 FUSE 层的逐字节一致性检查（见 §4.7 表） |
| **M3** | `hydrate` 预热 + LRU 持久缓存 + systemd + Navidrome 联调 | 二次扫描 0 重算；全命中缓存首扫 < 60s || **M4** ✅ 已完成 | 纯 Go FLAC 引擎（元数据手术 + 无解码帧索引器）+ 纯 Go WAV 切分 + `retag` / `flacinfo` | ✅ 帧链索引器在真实 306 MB 专辑上 **Σ == STREAMINFO 且 trailer 为 0**（同时实证 10/10 个 cue 边界不与 FLAC 帧边界对齐，证明"纯字节拷贝切 FLAC 不可行"）；✅ `retag` 388 轨元数据重写后 `verify` 388/388（PCM md5 + tag 逐条比对）；✅ Go 写的 tag 干净（无 `encoder: Lavf`）、支持多值 Vorbis Comment；✅ WAV 纯 Go 切分带单测 |

**通用测试矩阵**：24bit/96kHz、多声道、HTOA、`INDEX 00`、`PREGAP`、末轨不足 75 帧、
多 FILE 多碟、GBK/Big5/UTF-16 编码 cue、坏 cue 不崩溃（报 warning 并透传原文件）。

**开发环境提示**：本机为 Windows；M0/M1 核心为纯 Go，可跨平台开发测试；
M2 的 FUSE 部分需 Linux（WSL2 / Docker）验证。

---

## 7. Go 项目骨架与依赖

```
cuefs/
  cmd/cuefs/main.go          # 子命令分发（stdlib flag，无 cobra）
  internal/
    cue/                     # M0 ✅ 解析 + 编码检测
      parser.go  time.go  encoding.go  types.go
    library/                 # M0 ✅ 分轨判定 + 只读扫描（scan）
    config/                  # M0 ✅ YAML schema
    audio/                   # M1 起
    plan/                    # M1 起
    engine/                  # M1 起（ffmpeg runner + cache）
    fs/                      # M2 起（FUSE）
  docs/DESIGN.md
  testdata/                  # 多编码 cue 样本（gen.go 再生成）
  Makefile                   # make build-arm64 / test / fixtures
```

| 用途 | 依赖 | 阶段 |
|---|---|---|
| FUSE | `github.com/hanwen/go-fuse/v2` | M2 |
| 编码 | `golang.org/x/text` | M0 |
| 编码交叉校验 | `github.com/saintfish/chardet` | M0 |
| FLAC 低层解析 | `github.com/mewkiz/flac` | M4 |
| YAML | `gopkg.in/yaml.v3` | M0 |
| ffmpeg | 外部二进制，`os/exec` | M1 |

### 7.1 构建与部署工作流

**本地开发 + 交叉编译，目标机只跑二进制**（目标机性能不足以支撑编译）：

```console
$ go test ./...                                     # 本地跑测试
$ GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
      go build -trimpath -ldflags "-s -w" -o dist/cuefs-linux-arm64 ./cmd/cuefs
$ # 把 dist/cuefs-linux-arm64 拷到目标机直接运行
```

- 目标机是 **ARM64 Linux**，本机是 Windows，因此交叉编译是默认路径；
- 二进制无 cgo、静态链接（`CGO_ENABLED=0`），不依赖目标机的 Go 工具链；
- 远端测试一律只读地跑 `scan` / `probe` / `dump`，绝不写源库。

---

## 8. 已知风险

1. **GBK/Big5 短文本歧义**：靠配置顺序 + `probe` 排查，无法做到 100% 自动。
2. **Docker 传播属性**：挂载顺序错误会导致容器内看不到挂载点，需写进部署文档。
3. **ffmpeg 重编码耗时**：1500 轨全冷启动约 15–40 分钟（一次性）；用 `hydrate` 提前预热规避。
4. **Navidrome 路径指纹**：改命名模板/`schemeVersion` 会触发全库重建。
5. **cuefs 进程崩溃**：挂载点 hang，需 systemd 自动重启。
