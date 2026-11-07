# stable-legacy：旧版 osu!（stable）端数据形态与数据库格式

> 自包含摘录（C-1）：stable 端接入 LZSync 所需的全部格式知识固化于此。
> 来源：ppy/osu 官方 wiki「Legacy database file structure」（2026-11 取证）+
> 本机安装实测（`/home/brokenshine/Games/osu!/`，osu!.db 版本 20260924，
> 66 谱面集 / 1 成绩 / 0 收藏）。代码零引用外部文档。

## 0. 数据目录布局（本机实测）

| 路径 | 内容 |
|---|---|
| `Songs/<setID> <artist> - <title>/` | 解包谱面集：`.osu` 逐难度 + 音频/背景/音效资源，目录名前缀即 BeatmapSetID（未提交谱面无数字前缀） |
| `Skins/<name>/` | 解包皮肤：skin.ini + 元素文件（.osk 就是该目录的 zip 包） |
| `osu!.db` | 谱面索引（二进制，§1）——**LZSync 只读不写**：投放只落 Songs/ 目录，游戏启动自动扫描导入 |
| `collection.db` | 收藏（二进制，§2）——读 + 整文件原子重写 |
| `scores.db` | 成绩（二进制，§3）——二期板块，本期不碰 |
| `Data/a` `Data/bg` `Data/bt` `Data/r` | 稳定版自有缓存：音频/在线背景/缩略图/回放（`Data/r/<replayMD5>-<scoreID>.osr/.osg`）；**无任何 realm 文件** |
| `osu!.cfg` / `osu!.<user>.cfg` | 全局/用户配置（文本键值）——板块不同构，本期不同步 |
| `presence.db` | 在线状态缓存，与同步无关 |

进程：`osu!.exe`（wine 运行）——与 lazer 端 ProcessNames `"osu!"` 撞名，守卫须精确匹配消歧（见 forks/guard 施工注记）。

## 1. osu!.db

### 1.1 基础类型（全部小端；整数无符号）

| 类型 | 字节 | 说明 |
|---|---|---|
| Byte/Short/Int/Long | 1/2/4/8 | 整数 |
| ULEB128 | 变长 | LEB128 无符号变长整数 |
| Single/Double | 4/8 | IEEE 浮点 |
| Bool | 1 | 0x00 假，其余真 |
| String | 变长 | 首字节 0x00 = 空串；0x0b = 后随 ULEB128 字节长度 + UTF-8 内容 |
| Int-Float pair | 10 | `0x08` + Int + `0x0c` + Single |
| Int-Double pair | 14 | `0x08` + Int + `0x0d` + Double |
| Timing point | 17 | Double(BPM) + Double(偏移 ms) + Bool(false = 继承点) |
| DateTime | 8 | .NET ticks（自 0001-01-01 UTC 起的 100ns 数） |

### 1.2 文件头

Int 版本（如 20260924）→ Int 文件夹数 → Bool 账号已解锁 → DateTime 解锁日期 →
String 玩家名 → Int 谱面数 → Beatmap×N → Int 用户权限
（0 无 / 1 普通 / 2 Mod / 4 支持者 / 8 好友 / 16 peppy / 32 WC 工作人员）。

### 1.3 Beatmap 条目（按字段顺序）

| 类型 | 字段 |
|---|---|
| Int | 条目字节大小（**仅 v<20191106**） |
| String | Artist / ArtistUnicode / Title / TitleUnicode |
| String | Creator / Difficulty（难度名） |
| String | 音频文件名 |
| String | 谱面 MD5 |
| String | .osu 文件名 |
| Byte | Ranked 状态（0 未知 / 1 未提交 / 2 pending/wip/graveyard / 3 未用 / 4 ranked / 5 approved / 6 qualified / 7 loved） |
| Short×3 | 圆圈 / slider / 转盘数（全模式存） |
| Long | 最后修改时间（ticks） |
| Single×4（**v<20140609 为 Byte×4**） | AR / CS / HP / OD |
| Double | Slider velocity |
| Int-Float pair×4（星数表，**仅 v≥20140609**；**v<20250107 为 Int-Double pair**） | osu!/Taiko/CTB/mania（Int = mod 组合，浮点 = 星数） |
| Int×3 | Drain 时间（秒）/ 总时长（ms）/ 预览起始（ms） |
| Timing point×N | 前置 Int 计数 |
| Int×3 | DifficultyID / BeatmapID / ThreadID |
| Byte×4 | 四模式成绩等级 |
| Short | 本地偏移 |
| Single | Stack leniency |
| Byte | 模式（0=osu! 1=taiko 2=catch 3=mania） |
| String | Source / Tags |
| Short | Online offset |
| String | 标题字体 |
| Bool | 是否未游玩 |
| Long | 最后游玩时间 |
| Bool | 是否 osz2 |
| String | 相对 Songs 的文件夹名 |
| Long | 最后对照仓库检查时间 |
| Bool×5 | 忽略声音 / 忽略皮肤 / 禁用 storyboard / 禁用视频 / Visual override |
| Short | 未知（**仅 v<20140609**） |
| Int | 最后修改时间(?) |
| Byte | Mania 滚动速度 |

版本分叉汇总：`v<20140609`（Byte 难度四元组 + 无星数表 + 末尾未知 Short）；
`v<20191106`（条目字节大小 Int）；`v<20250107`（星数 Int-Double，之后 Int-Float）。
本机 20260924 → 无字节大小 / Single 难度 / Int-Float 星数。

## 2. collection.db

Int 版本 → Int 收藏数 → 每条：String 名称 → Int 成员数 → String×N（谱面 MD5）。
成员即 MD5 列表，**可跨安装转移**，与 lazer `BeatmapCollection.BeatmapMD5Hashes`
一一对应（LZSync 收藏板块的天然同构载体）。写路径 = 整文件重写（S9 临时→自检→rename）。

## 3. scores.db（二期）

Int 版本 → Int 谱面数 → 每谱面：String 谱面 MD5 → Int 成绩数 → Score×N。

Score 条目（与 .osr 回放格式一致）：Byte 模式 → Int 回放版本 → String 谱面 MD5 →
String 玩家名 → String 回放 MD5 → Short×5（300 / 100（taiko 150 / catch+mania 100）/
50（mania 与 catch 小果同位）/ Geki（mania Max300）/ Katu（mania 200））→
Short Miss → Int 分数 → Short 最大连击 → Bool 完美 → Int mods 位组合 →
String（恒空，回放格式遗留）→ Long 时间戳 → Int（恒 -1）→ Long OnlineScoreID →
Double（仅 Target Practice 启用时存在）。

## 4. 与 lazer 端的对齐锚点（跨端判定依据）

| 锚点 | stable 侧 | lazer 侧 | 结论 |
|---|---|---|---|
| 集内容哈希 | 集 .osu 内容按文件名排序拼接的 SHA-256（对 Songs/ 目录复算） | `BeatmapSetInfo.Hash` 同算法（lazer-baseline §6.1） | **同一谱面两端同键**（实测 4/5 共有集精确一致，见下「stale 集哈希」） |
| 难度内容键 | .osu 文件 MD5（= osu!.db 谱面 MD5 字段） | `BeatmapInfo.MD5Hash` | 同键（真机 208/208 一致） |
| 难度 Hash | .osu 文件 SHA-256 | `BeatmapInfo.Hash` | 同键（真机 6/6 逐字节一致，147962 全难度） |
| 集身份键 | `online:<Songs 目录名数字前缀>`（BeatmapSetID） | `online:<BeatmapSetInfo.OnlineID>` | 同键（S8：stable 连 ppy 官方服，同域） |
| 收藏 | collection.db (名称, MD5 列表) | BeatmapCollection (Name, BeatmapMD5Hashes) | 同构 |
| 皮肤 | 目录名 / skin.ini `[General] Name` | SkinInfo.Name；但 Hash = .ini+.json 拼接 SHA-256，stable 无 .json，**无法复算** | 内容键不同构 → 跨端配对走 Name 弱键（同谱面身份键第二层模式） |

### 4.1 实测注记（2026-11-07 真机对拍，collect 侧已按此施工）

- **lazer 集哈希是导入时缓存**：谱面在线更新后不重算。实测 147962 两端
  全部 .osu 字节一致（逐文件 SHA-256 相同）但集哈希不同。对策：判定层
  在集哈希不等时以**难度 Hash（或 MD5）集合比对**兜底，集合全同视为同
  版本不投放；收集层两端各自如实报键。
- **同内容重复目录**：真机 Songs/ 66 目录去重后 65 集（同一谱面装成两
  个目录），收集侧按内容键去重保留首个（与 realm 投影同防线）。
- **NaN 净化**：osu!.db 星数表与难度值存在 NaN（真机实测），JSON 无法
  序列化，收集侧一律归零（均为展示字段、非判定键）。
- **目录名规范化**：stable 目录名剥离 artist 中的句点（`feat.` →
  `feat`）等字符；投放构造目录名时 `<setID> <artist> - <title>` 按同规则
  规范化（P3）。
- **Windows 进程同名歧义**：stable 与 lazer 在 Windows 上镜像同名
  `osu!.exe`，进程名无法消歧；Linux（wine）下进程名带 `.exe` 与原生
  lazer 精确匹配天然区分。

## 5. 占用与写安全

- 无 realm `.lock`：占用探测平移为进程守卫（`osu!.exe`）+ 对 osu!.db / collection.db
  的独占打开探测（Windows 语义等价：开-即-释）。
- stable 启动时自动扫描 Songs/ 新目录并导入（改写 osu!.db）——投放落目录后
  游戏侧导入是游戏自己的事，LZSync 快照以 Songs/ 目录扫描为准、osu!.db 只供
  元数据，投放后闭环不依赖游戏跑过一遍。
