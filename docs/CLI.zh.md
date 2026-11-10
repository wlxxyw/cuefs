# cuefs 命令行使用手册

> 相关文档：[README（English）](../README.md) · [README（中文）](../README.zh.md) · [设计文档](DESIGN.md)
>
> 适用于 **0.2.0-m4.10**。内容与 `cuefs help` 及各命令的 `-h` 一致，并补上了默认值、用法和示例。
> 所有命令都**只读源目录**（设计约束 D15）：不会改动、删除或移动源音乐目录里的任何文件。生成的内容只写进 `--out`（或配置里的 `cache`）指定的目录。

## 目录

| 类型 | 命令 | 一句话说明 |
|---|---|---|
| 查看 | `probe` | 看一个 cue 文件会被当成什么、按哪种编码读 |
| 查看 | `scan` | 扫一遍目录，预览每张专辑会被怎么处理（不写任何东西） |
| 查看 | `dump` | 把 cue 解析出来的内容（曲目、时间、标签）打印出来 |
| 生成 | `split` | 把整轨专辑切成单曲，生成整套输出目录（含封面、原样保留的文件） |
| 清理 | `gc` | 删掉缓存里多出来的文件，并报告占用空间 |
| 维护 | `retag` | 重写已生成单曲的标签（不动音频） |
| 检查 | `verify` | 重新解码生成的单曲，和源文件的同一段音频比对 |
| 服务 | `mount` | 把目录挂成只读虚拟视图（仅 Linux），并自动管理缓存 |
| 调试 | `flacinfo` | 查看一个 FLAC 的内部结构（不解码） |
| 调试 | `debugmd5` | 算某一小段音频的 md5（纯 Go 解码） |
| 调试 | `splitone` | 没有 cue 文件时，按采样区间手工切一首 |
| 其他 | `version` / `help` | 版本 / 帮助 |

---

## 0. 通用规则

- **配置文件的查找顺序**：`--config` 指定 → 环境变量 `$CUEFS_CONFIG` → 当前目录的 `cuefs.yaml` → `~/.config/cuefs/cuefs.yaml`。
- **`--out` 的默认值不一样，这点要记牢**：`split`、`retag`、`verify` 不写 `--out` 时会用配置里的 `mount`（也就是挂载点）；`gc` 不写 `--out` 时用的是配置里的 `cache`（生成单曲实际存放的地方）。想写到单独的目录，一律显式写 `--out`。
- **退出码**：0 表示成功；1 表示出错（最后会打印一行 `cuefs: 原因`）；2 表示命令名不认识。
- **日志前缀**：每行开头都有固定标签，方便用 grep 筛选：`plan` / `write` / `skip` / `ok` / `FAIL` / `warning` / `error` / `cover` / `link` / `copied` / `ape` / `verify` / `mount` / `overlay` / `gc` / `gc ttl` / `gc size`，大致对应：计划 / 写入 / 跳过 / 成功 / 失败 / 提醒 / 错误 / 封面 / 硬链接 / 复制 / APE / 核对 / 挂载 / 视图层 / 缓存清理。
- **参数写法**：`-flag` 和 `--flag` 都可以。开关键参数直接写 `-force` 就行，也可以写 `-force=true`。
- **`-json` 只有三个命令真的有用**：`probe`、`scan`、`dump` 会输出 JSON；其他命令也接受这个参数，但会忽略它。

---

## 1. probe —— 单个 cue 文件诊断

### 干什么用的
先弄清一件事：**这个 cue 文件，cuefs 会把它当成什么？**能否在同目录找到对应的音频文件？按哪种编码读取？排查“某张专辑为什么没被切出来”，通常从这里开始。

### 写法
```
cuefs probe [通用参数] <专辑.cue>
```

### 参数
| 参数 | 类型 | 示例值 | 默认值 | 说明 |
|---|---|---|---|---|
| `-config` | string | `/path/cuefs.yaml` | `""`（按上面的顺序查找） | 指定配置文件 |
| `-encoding` | string | `gb18030` | `""`（自动判断） | 强制用某种编码读 cue，不再自动检测 |
| `-encoding-list` | string | `utf-8,gb18030,big5` | `""`（内置顺序） | 按顺序尝试的编码清单，逗号分隔 |
| `-json` | bool | `true`/`false` | `false` | 输出 JSON，便于脚本处理 |

### 示例
```bash
# 某张专辑为什么没被处理？看看分类结果和提醒
cuefs probe "/srv/music/许巍-《在别处》 FLAC+CUE/在别处.cue"

# cue 编码判断错了，手动指定再看
cuefs probe -encoding gb18030 "/srv/music/老cd.gb.cue"

# 脚本里只取解析结果
cuefs probe -json album.cue | jq '.parsed'
```

---

## 2. scan —— 扫一遍目录，先看结果

### 干什么用的
遍历源目录，逐张告诉你每张专辑会被怎么处理：哪些会被切成单曲，哪些原样保留，哪些有问题。**只读，不写任何文件**，适合在正式切之前先摸底。

### 写法
```
cuefs scan [通用参数] [-all] <目录>
```

### 参数
除通用参数外，多一个：
| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-all` | bool | `false` | 连“原样保留”和“没识别成专辑”的 cue 也列出来（默认只列会被切的） |

### 示例
```bash
# 只看会被切的专辑
cuefs scan /srv/music

# 全部看一遍（含原样保留和有问题的）
cuefs scan -all /srv/music

# 统计会被切成单曲的专辑数
cuefs scan -json /srv/music | jq '.summary'
```

---

## 3. dump —— 查看 cue 解析结果

### 干什么用的
把 cue 文件解析后的内容打印出来：曲目名、每段的 INDEX 时间、REM 信息，以及从 cue 里继承的标签。

### 写法
```
cuefs dump [通用参数] [-duration 分:秒:帧] <专辑.cue>
```

### 参数
| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-duration` | string（如 `42:17:70`） | `""` | 音频总时长（分:秒:帧）。最后一段默认“一直播到文件结尾”，给出总时长才能算出最后一首的准确结束点 |
| `-json` | bool | `false` | 输出 JSON（通用参数） |

### 示例
```bash
cuefs dump "01.cue"                       # 检查曲目和索引
cuefs dump -duration 42:17:70 "01.cue"    # 最后一首有准确的结束点
cuefs dump -json "01.cue" | jq '.tracks[].title'
```

---

## 4. split —— 主命令：生成整套单曲

### 干什么用的
按当前的命名规则和配置，把源目录里的整轨专辑切成一首一首的单曲。FLAC/WAV 用内置解码器直接切；APE 交给 ffmpeg 转换。同时把封面、以及“不需要切、应该原样保留”的文件一起搬到输出目录。全部写完后会自动清一次缓存里多出来的文件（和 `gc` 做的事一样）。

### 写法
```
cuefs split [-src 目录] [-out 目录] [-only 关键词] [-force] [-link] [-skip-passthrough] [-dry] [-quiet] [-workers N] [通用参数]
```

### 先说两个最常用的目录参数
- `-src`：源音乐目录，默认用配置里的 `src`。
- `-out`：输出目录，**默认用配置里的 `mount`（挂载点）**。想写到单独的输出目录，必须显式传 `--out`。`gc` 的默认值不一样，它默认用 `cache`。

### 参数
| 参数 | 类型 | 示例值 | 默认值 | 说明 |
|---|---|---|---|---|
| `-src` | string | `/srv/music` | 配置 `src` | 源目录 |
| `-out` | string | `/srv/out` | 配置 `mount` | 输出目录（注意上面的说明） |
| `-only` | string | `乐行者` | `""`（不筛） | 只处理名字里包含这个词的专辑。只切一张时，末尾那次清理照样按全库该有的文件来算，不会误删别的专辑 |
| `-force` | bool | | `false` | 已经存在的单曲也重新生成（默认跳过已有的） |
| `-link` | bool | | `false` | “原样保留”的文件用硬链接代替复制（源和输出在同一个文件系统时才有效） |
| `-skip-passthrough` | bool | | `false` | 跳过所有“不需要切分”的文件（封面以外的日志、m3u、已切好的目录等），汇总里列出跳过了多少；输出树里就只有单曲和封面。适合在大库上做快速小规模测试。和 `-link` 同时给时跳过优先。注意：末尾的清理始终按“原样保留也该有”来算，**不会**把树里已经存在的这类文件删掉 |
| `-dry` | bool | | `false` | 只打印打算做什么，不写任何文件 |
| `-quiet` | bool | | `false` | 不打印每一首的处理行，只留最后的汇总 |
| `-workers` | int | `6` | 配置 `workers`（默认 4） | 同时处理几首；同一次运行里，每个源文件只读一遍内部结构，由多首共享 |

### 示例
```bash
# 全量生成（--out 一定要指向输出目录）
cuefs split -out /srv/out --workers 6

# 只重切一张
cuefs split -only "乐行者" --force -out /srv/out

# 先看看会做什么，不写盘
cuefs split -dry

# 原样保留的文件用硬链接，省空间
cuefs split -link -out /srv/out

# 快速本地测试：只切单曲，其他文件一个都不搬
cuefs split -skip-passthrough -only "乐行者" -out /tmp/test-out
```

### 附：`split.fake_streaminfo_md5`
见第 9 节。打开后 FLAC 切割跳过整轨解码，实际耗时约为原来的 1/8；`split` 和 `mount` 必须用同一个值。

---

## 5. gc —— 清理缓存里多出来的文件

### 干什么用的
1. **清掉多余文件**：先算出“按当前规则，这个缓存目录里应该有哪些文件”（生成的单曲 + 封面 + 原样保留的文件），然后把清单之外的文件、被删空了的目录、以及放了超过 24 小时的 `.part` 临时文件删掉。
2. **报告占用情况**：打印缓存一共多少文件、占多少空间；和 `cache_max_gb` 比一比，告诉你还有多少余量或已经超了；并说明 TTL 和“超容量清理”由 `mount` 的定时任务负责。

### 需要知道的几点
- **`gc` 不会按“多久没读”或“超出容量”去删文件**。原因是离线运行时拿不到可靠的最新访问时间：系统为了性能会把访问时间按天合并，而且文件可能正被读取。这两种清理交给 `mount` 运行时的定时任务做。
- 即使传了 `-only`，也会被忽略：必须按整个库来算清单，否则会把其他正常专辑的文件误当成多余文件删掉。
- `-out` 默认就是配置里的 `cache`（和写命令不同）。

### 写法
```
cuefs gc [-out 目录] [-dry] [通用参数]
```

### 参数
| 参数 | 类型 | 示例值 | 默认值 | 说明 |
|---|---|---|---|---|
| `-out` | string | `/srv/out` | 配置 `cache` | 要清理的目录 |
| `-dry` | bool | | `false` | 只列出会删哪些，不真的删 |
| `-src` | string | `/srv/music` | 配置 `src` | 源目录（用来推算应有文件清单），一般不用传 |
| `-force` / `-link` / `-skip-passthrough` / `-only` / `-quiet` / `-workers` | | | | 这些参数也能写，但不影响结果；`-only` 会被忽略，`-workers` 固定为 1 |

### 示例
```bash
# 先预览（推荐第一步）
cuefs gc -dry

# 确认没问题再真删
cuefs gc

# 清理另一个目录
cuefs gc -out /srv/out
```

### 输出示例
```
gc     : orphan 测试专辑/01 - orphan.flac (2 bytes)     # 多出来的文件（-dry 时前缀是 gc-dry）
gc     : stale part xxx.part (2 bytes)                  # 放了太久的临时文件
gc cache: 8378 file(s), 191234.5 MiiB                   # 一共多少文件、占多少空间
gc budget: OVER budget 20 GiB - the mount's periodic task evicts least-recently-accessed album(s)
gc ttl   : 168h0m0s (enforced by the mount's periodic task; gc cannot see access times)
```
> 程序输出的单位写作 `MiiB`，就是 MiB。后两行是英文，意思是：“已超出 20 GiB 容量，将由 mount 的定时任务清理最久没访问的专辑”和“TTL 为 7 天，由 mount 的定时任务执行；gc 看不到访问时间”。

---

## 6. retag —— 重写已生成单曲的标签

### 干什么用的
**不动音频数据**，只重写已生成单曲里的标签块（FLAC 写 Vorbis Comment，WAV 写 ID3 和 RIFF LIST INFO）。适合两种情况：升级旧版本 cuefs 生成的库，或者改了标签忽略规则后刷新已有文件。每个文件都会**完整重写一遍**（先写 `.part` 再改名），WAV 尤其如此，是 IO 很重的慢操作，**不要当成日常刷新手段反复跑**。正确的做法是切割时就带上正确的标签。

### 写法
```
cuefs retag [-src 目录] [-out 目录] [-only 关键词] [-force] [-quiet] [-workers N] [通用参数]
```

### 参数
| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-src` | string | 配置 `src` | 源目录（同 `split`） |
| `-out` | string | 配置 `mount` | 输出目录（同 `split`，注意默认值） |
| `-only` | string | `""` | 只处理名字里包含这个词的专辑 |
| `-force` | bool | `false` | 已存在的文件也重写（默认只处理缺失的） |
| `-quiet` | bool | `false` | 不打印每个文件的处理行 |
| `-workers` | int | 内部固定 1 | 代码里固定一次只处理一个文件：重写标签是 IO 型任务，并行没有收益 |
| `-ref` | string | | 这个参数会被接受但忽略（它只对 `verify` 有用） |
| `-json` | bool | `false` | 会被接受但忽略 |

### 示例
```bash
# 全库重写一次（安静模式）
cuefs retag -quiet

# 只改一张
cuefs retag -only "在别处"
```

---

## 7. verify —— 检查生成的单曲是否正确

### 干什么用的
把每一首生成的单曲重新解码，取它应该对应的源音频那一段，比较两边的 PCM md5；同时按计划检查标签是否一致。两种参考解码方式：
- `--ref ffmpeg`：用 ffmpeg 独立解码来比对，是最强的检查。
- `--ref pure`：用内置的纯 Go 解码器（和切割用的是同一套代码），适合没有 ffmpeg 的环境，主要能抓数据层面的问题。

APE 源**只能**用 `--ref ffmpeg`（没有纯 Go 的 APE 解码器）。

### 写法
```
cuefs verify [-ref auto|ffmpeg|pure] [-src 目录] [-out 目录] [-only 关键词] [-quiet] [-workers N] [通用参数]
```

### 参数
| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-ref` | string | `auto` | `auto`：配置了 ffmpeg 就用 ffmpeg，否则用纯 Go；`ffmpeg`：强制用 ffmpeg；`pure`：强制用纯 Go |
| `-src` | string | 配置 `src` | 源目录 |
| `-out` | string | 配置 `mount` | 要检查的输出目录（注意默认值） |
| `-only` | string | `""` | 只检查名字里包含这个词的专辑 |
| `-quiet` | bool | `false` | 不打印每首的通过行，只留汇总和失败项 |
| `-workers` | int | 配置 `workers` | 同时检查几首 |
| `-force` | bool | `false` | 会被接受但没有作用：`verify` 只读不重建 |
| `-json` | bool | `false` | 会被接受但忽略 |

### 示例
```bash
# 最严格的检查（有 ffmpeg 时推荐）
cuefs verify -ref ffmpeg -quiet

# 没有 ffmpeg，用内置解码器自检
cuefs verify -ref pure -quiet

# 只查一张
cuefs verify -ref ffmpeg -only "在别处"
```

### 输出示例
```
checked 10 track(s): OK 10, FAILED 0, MISSING 0
FAIL   : <文件>: <原因>    # 每一首失败列一行
```

---

## 8. mount —— 只读目录视图 + 自动管理缓存

### 干什么用的
把源目录挂成一个只读的虚拟目录：FLAC/WAV 整轨专辑在视图里变成一首首单曲，APE 按配置转换或原样保留。同时还会跑一个定时任务来管理缓存（m4.9）：

1. **源文件变了就重新生成**：cue 文件或音频的修改时间变了，就清掉这张专辑在缓存里的旧文件；但要等这张专辑的所有单曲都超过 5 分钟没被读过才动手，以免正在播放时被删。宽限期内会先不动，下一轮再看。
2. **太久没读就清掉**：整张专辑超过 `cache_ttl` 没人访问，就把它的缓存删掉。
3. **超出容量就清掉**：缓存总量超过 `cache_max_gb` 时，从最久没访问的专辑开始删，直到降到上限以内。`0` 表示不做容量清理（TTL 仍然生效）。
4. **访问记录太多时减半**：内部记的“哪首最近被读过”超过 8192 条时，砍掉最旧的一半；被砍掉的记录之后只能按文件修改时间来推断冷热。

### 写法
```
cuefs mount [-src 目录] [-mount 挂载点] [-cache 目录] [-synthesize] [-allow-other] [-fuse-opts "选项"] [-workers N] [-quiet]
```

### 参数
| 参数 | 类型 | 示例值 | 默认值 | 说明 |
|---|---|---|---|---|
| `-src` | string | `/srv/music` | 配置 `src` | 源目录（内部会先解析符号链接再遍历） |
| `-mount` | string | `/srv/music-view` | 配置 `mount` | 挂载点 |
| `-cache` | string | `/srv/out` | 配置 `cache` | 生成的单曲放哪；和 `split` 的输出是同一个结构，指向同一个目录就共用 |
| `-synthesize` | bool | | `false` | 缓存里没有时，允许调用外部 ffmpeg 当场生成；不开的话只有 FLAC/WAV 能靠内置引擎临时生成，APE 会报错 |
| `-allow-other` | bool | | `false` | 给 fusermount3 传 `allow_other`（需要 `/etc/fuse.conf` 里有 `user_allow_other`）；Navidrome 用另一个系统用户访问时需要 |
| `-fuse-opts` | string | | `""` | 原样传给 fusermount3 的其他选项，多个用逗号隔开；与内置选项重复时会自动去重，`fsname=xxx` 会覆盖默认值 |
| `-workers` | int | `4` | 配置 `workers` | 同时生成几首的上限 |
| `-quiet` | bool | | `false` | 不打印每个文件的处理行 |

### 示例
```bash
# 基本挂载（配置里都配好时）
cuefs mount

# Navidrome 用另一个用户跑，需要 allow_other
cuefs mount -allow-other

# 缓存没有时允许 ffmpeg 当场生成（APE 必须开，FLAC 也可以让 ffmpeg 来生成）
cuefs mount -synthesize
```

### 权限模型（另一系统用户访问需过两道闸）

`-allow-other` 只放行 fusermount3 这一层（"允许非挂载执行人的用户访问该挂载点"）。真正的读写裁决由内核经 `default_permissions` 按**挂载视图回报的 uid/gid/mode** 做经典 Unix 检查，而 cuefs 是 loopback 视图，回报的就是**源目录文件自己的属主与权限位**。所以另一个系统用户（例如 Navidrome 所用的容器用户）能读挂载点，两件事缺一报 EACCES：

```bash
# 1. fusermount3 层放行（等于 -allow-other，未有 user_allow_other 时挂载会报错）
grep user_allow_other /etc/fuse.conf     # /etc/fuse.conf 改完不需重启，下一次挂载即生效
cuefs mount -allow-other

# 2. 源库对那个用户读得动 —— cuefs 不做自己的权限判断，只透传源文件的权限位
chmod -R a+rX /srv/music         # a+rX 只补读 + 目录进入位，不乱加可执行
# 改完先验证最终效果：
sudo -u navidrome ls /srv/music  # 生产里用 Navidrome 的部署方式自检一下
```

挂载点 `ls` 出来的 mode/owner 就是源文件的原样映射——权限出了问题先看源目录而不是挂载点；改的是源的权限，也无需重新挂载即可生效。

### 输出示例（定时任务的日志）
```
mount : ...
gc     : album table registered (72 album(s) tracked)        # 已登记 72 张专辑的源文件修改时间
gc     : /srv/music/xx/xx.cue changed (mtime ...) - pruning cache ... (regenerated on demand)
gc ttl : /path: untouched for 168h - pruning cache ...        # 超过 7 天没读，清掉
gc size: evicted /srv/out/老专辑 (300.0 MiiB, last touched ...)  # 容量超了，清掉最久没读的
gc size: evicted 21 album(s), freed 7000.0 MiiB; cache now X MiiB of budget 20 GiB
```

**使用建议**：
- **容量上限要给够**：`mount` 长期运行、而 `cache_max_gb` 比整个库还小时，定时任务每轮都会不停地删专辑（下次读取时会自动重新生成，但反复做无用功）。稳妥的做法是把 `cache_max_gb` 设成不小于库的总体积，或者设成 `0`（不做容量清理，TTL 仍然有效）。
- 被清掉的单曲**下一次被读取时会自动重新生成**；如果想马上把库补齐，可以在打开 `fake_streaminfo_md5: true` 的前提下跑一次 `split`（否则就是慢速重写）。

---

## 9. 配置文件（cuefs.yaml）

```yaml
src: /srv/music              # 源目录；可以是符号链接
mount: /srv/music-view       # FUSE 挂载点；也是 split 不写 --out 时的默认输出目录！
cache: /srv/out              # 生成的单曲放在这里；gc 默认清理这里；mount 也在这里读写缓存

cache_max_gb: 20             # mount 定时任务的容量上限，单位 GiB；0 = 不做容量清理（建议不小于库总体积）
cache_ttl: 168h              # 超过这么久没读就清理，写法同 Go 的时间写法（168h = 7 天）

ffmpeg_bin: ffmpeg           # APE 转换、提取封面；也是 mount -synthesize 用的转换器
ffprobe_bin: ffprobe         # 读取音频信息（时长、位深等）
workers: 4                   # split / verify / mount 默认同时处理几个
name_template: "{num:02d} - {title}{ext}" # 单曲文件名模板；{num}、{num:NN}、{title}、{ext}；ext 必须写
title_max_len: 60            # 标题最长多少字符，超出的部分会被截断
keep_album: false            # true 表示 cue 文件也放进输出目录（默认隐藏 cue）

split:
  extensions: [".flac", ".wav", ".ape"] # 哪些格式的音频会被当成可切的整轨（要求同目录有同名 cue）
  ape_convert_format: flac   # APE 转换后的格式：flac 或 wav（ape_mode: convert 时生效）
  ape_mode: convert          # convert | hide | passthrough
                              #   hide: 源 APE 和它的 cue 都不出现在视图里
                              #   passthrough: APE 和 cue 原样保留
  compression_level: 5       # FLAC 压缩级别 0-8
  fake_streaminfo_md5: false # true：STREAMINFO 里的 md5 写 0（FLAC 规范里表示“未知”），
                              #   跳过整轨解码，FLAC 切割速度接近直接读写
                              #   （306 MB 的专辑实测：12 分钟 → 1 分 41 秒）
                              #   Navidrome、taglib、verify 都不受影响；
                              #   注意 split 和 mount 必须用同一个值

cue:
  encoding: auto             # auto，或强制指定（gb18030 / big5 / utf-8 ...）
  encoding_list: []          # 按顺序尝试的编码清单；空 = 用内置顺序（设计 D11）
  cue_overrides: {}          # 按目录指定 cue 编码，例如 {"/path": {encoding: gb18030}}
  ignore_tags: ["CUE_TRACK*", "COMMENT"] # 生成标签时要忽略的键，支持通配符

log_level: info              # info 或 debug
```

### 常用默认值
| 配置项 | 默认值 | 说明 |
|---|---|---|
| `cache_max_gb` | 20 | 0 = 不做容量清理 |
| `cache_ttl` | 168h（7 天） | |
| `workers` | 4 | |
| `split.ape_mode` | convert | |
| `split.compression_level` | 5 | |
| `split.fake_streaminfo_md5` | false | 照常计算真实 md5，需要整轨解码 |
| `cue.ignore_tags` | `["CUE_TRACK*","COMMENT"]` | |
| `log_level` | info | |

---

## 10. 调试小工具

> 这组命令（`flacinfo`、`debugmd5`、`splitone`）**不读配置文件，也不接受通用参数**，各自接收自己的参数。

### 10.1 flacinfo —— 查看 FLAC 内部结构（不解码）

```bash
cuefs flacinfo <文件.flac>
cuefs flacinfo -frames 4 <文件.flac>                 # 打印前 4 帧的偏移和长度
cuefs flacinfo -cue "<专辑.cue>" <专辑.flac>         # 检查 cue 里的切点和帧边界差多少
```
| 参数 | 类型 | 示例值 | 默认值 | 说明 |
|---|---|---|---|---|
| `-cue` | string | `<专辑.cue>` | `""` | 顺便检查 cue 的每首切点和 FLAC 帧边界是否对得上（排查切错位置很有用） |
| `-frames` | int | `4` | `0` | 打印前 N 帧的位置信息 |

### 10.2 debugmd5 —— 算一小段音频的 md5（纯 Go）

```bash
cuefs debugmd5 -start 0 -end 4096 <文件.flac>          # 前 4096 个采样
cuefs debugmd5 -start 1024 -end 4096 -bits 24 <文件.flac>
```
| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-start` | int64 | 0 | 起始采样（含） |
| `-end` | int64 | 0 | 结束采样（不含） |
| `-bits` | int | 16 | md5 使用的位宽，必须和源文件一致 |

### 10.3 splitone —— 没有 cue 时手工切一首

```bash
cuefs splitone -start 0 -end -1 -rate 44100 -ch 2 -bits 16 \
  -tags "TITLE=x,ARTIST=y" /src/x.flac /out/01.flac
```
| 参数 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `-start` | int64 | 0 | 起始采样（含） |
| `-end` | int64 | `-1`（一直到结尾） | 结束采样（不含） |
| `-rate` | int | 44100 | 采样率 |
| `-ch` | int | 2 | 声道数 |
| `-bits` | int | 16 | 位深 |
| `-tags` | string | `""` | 要写的标签，`键=值` 用逗号隔开 |

---

## 11. 常见工作流程

### 第一次建库（完整检查，默认方式）
```bash
cuefs split -out /srv/out -workers 6
cuefs verify -ref ffmpeg -quiet          # 期望看到 checked X track(s): OK X, FAILED 0
```

### 快速生成（打开 fake md5 后）
在 `cuefs.yaml` 里加 `split.fake_streaminfo_md5: true`，然后：
```bash
cuefs split -out /srv/out --workers 6    # FLAC 切割从约 10 分钟一张变成约 1.5 分钟一张
```
代价是 STREAMINFO 里的 md5 写成 0（FLAC 规范含义是“未知”），播放器不能再用它自检，其他功能没有影响。

### 日常清理
- 挂载运行时由 `mount` 的定时任务自动完成；想离线检查一遍，就先 `cuefs gc -dry`，再 `cuefs gc`。

### 本地快速测试（不搬多余文件）
```bash
cuefs split -skip-passthrough -only "乐行者" -dry     # 先看计划：会跳过多少原样文件
cuefs split -skip-passthrough -only "乐行者" -out /tmp/test-out
```
输出树里只有单曲和封面，汇总行会写成 `passthrough 0 file(s) (N skipped)`。

### 改了命名模板或编码规则
```bash
cuefs split -dry -only <某张>        # 先看看新名字
cuefs split -force -out /srv/out     # 确认后重新生成
cuefs gc -dry                        # 列出旧名字留下的多余文件
cuefs gc                             # 确认后删掉
```

### 只升级老库的标签（不重新切）
```bash
cuefs retag -quiet
cuefs verify -ref ffmpeg -quiet
```

---

## 12. 容易踩的坑

1. **`split` 不写 `--out` 会写进挂载点**：它的默认值是配置里的 `mount`，不是 `cache`。所以切割时一定要显式写 `--out`。`gc` 的默认值才是 `cache`。
2. **`verify -ref pure` 不支持 APE 源**：没有纯 Go 的 APE 解码器，程序会直接提示你改用 `--ref ffmpeg`。
3. **WAV 的标签有两套**：ID3v2.4 和 RIFF LIST INFO 都会写。`retag` 遇到已经有标签的文件会整个重写（`.part` 再改名），IO 成本很高，所以 WAV 最好切割时就一次写好标签，不要靠 `retag` 反复刷。
4. **`cache_max_gb` 别设得比库还小**：`mount` 长时间运行时，定时任务会不停清掉再重建。把上限设成不小于库总体积，或者设成 `0` 关掉容量清理（TTL 仍然有效）。
5. **`split --force` 和 `gc` 是两回事**：`split --force` 是重建已经存在的单曲；`gc` 是按“应该有那些文件”的清单删除多余文件，不管 `--force`。改了命名模板后，先用 `gc -dry` 看清楚再删。
6. **不要在 `cache` 目录里放别的文件**：`gc` 只保留清单里的内容（单曲 + 封面 + 原样保留的文件），其他文件一律当多余文件删掉。
