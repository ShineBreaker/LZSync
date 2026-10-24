# LZSync

LZSync 管理同一台机器上多个 osu!lazer fork 的数据：以 SQLite 中枢为单一真相源，在各 fork 的 realm 库之间收集与投放可同步内容（曲库、收藏、皮肤、成绩、设置）。

## Language

### 核心概念

**Fork（分支客户端）**:
osu!lazer 官方客户端或其衍生客户端（lazer、DeltaLazer、Ez2Lazer、g0v0）之一，每个 fork 拥有独立的数据目录与 realm 库。
_Avoid_: 游戏、客户端（单用有歧义时）、镜像

**数据目录（Data Directory）**:
一个 fork 存放 client.realm、files/ 与各 ini 文件的根目录；同一台机器上各 fork 的数据目录天然隔离。
_Avoid_: 安装目录、游戏目录

**Hub（中枢）**:
LZSync 自有的 SQLite 数据库与内容寻址仓，是全部可同步数据的单一真相源；fork 侧的 realm 库只是它的投影。
_Avoid_: 主库、云端（并非远程）

**收集（Collect）**:
把一个 fork 的当前状态只读投影进 hub 并收仓其实体文件的方向。
_Avoid_: 导入、备份

**投放（Deploy）**:
把 hub 中的当前版本写入一个 fork 的 realm 库并为其建文件链接的方向。
_Avoid_: 同步（不指明方向时含混）、导出

**收仓（Adopt）**:
把游戏侧的实体文件移入 LZSync 自有仓、并在原位留下指向仓内源的链接，使后续投放可共享同一份字节。
_Avoid_: 迁移文件、挪文件

**实体文件仓（Blob Store）**:
LZSync 自有的 SHA-256 内容寻址文件仓，布局与官方 files/ 兼容；谱面、皮肤、回放等实体字节只在此存一份。
_Avoid_: files/（特指 fork 侧）、存储

### 对账

**内容键（Content Key）**:
判定两个实体是"同一版本"的键：集/难度/皮肤用内容哈希，收藏用名称，成绩用（BeatmapHash, Hash）组合。
_Avoid_: 主键、Guid（明确不用它做对账）

**谱面身份键（Beatmap Identity Key）**:
判定"同一谱面的不同版本"的次级键：同服务器用集 OnlineID（强键），跨服务器或本地谱面用（Artist, Title, Author）元数据（弱键，需用户确认）。
_Avoid_: 谱面 ID

**版本链（Version Chain）**:
由 superseded_by 串起的同一谱面的历史版本序列；只有链尾是当前版本，可进入投放差集，历史行永不投放。
_Avoid_: 版本历史（泛指）、旧版本列表

**Presence**:
记录某 fork 当前持有某实体的投影行；收集时整段重写。
_Avoid_: 快照（另有含义）、缓存

**Tombstone**:
收集时发现 fork 端实体消失所生成的删除记录；命中的差集项归入恢复组，永不默认投放。
_Avoid_: 墓碑、删除标记（与软删混淆）

**恢复（已删除）（Restore）**:
投放差集中命中目标端 tombstone 的项；默认不勾选，用户显式勾选才投放并顺带清除该 tombstone。
_Avoid_: 复活、重新导入

**四分类（Four-Way Diff）**:
对账结果的空间划分：源独有 / 目标独有 / 冲突（内容键相等、非键字段不同）/ 版本更新（内容键不等、谱面身份键相等）。
_Avoid_: 三分类（EZRealmSync 的旧模型，缺版本更新类）

### 写安全

**软删（Soft Delete）**:
写 DeletePending=true 的删除方式，由目标 fork 的客户端下次开库时兑现；LZSync 一切删除动作的首选（唯一例外：收藏夹整行移除，与官方一致）。
_Avoid_: 物理删除、真删

**写入白名单（Write Whitelist）**:
官方基线列 ∩ 源库实际列 ∩ 目标库实际列的三方交集；投放只写交集内的列。
_Avoid_: 全量复制

**批（Batch）**:
事务、进度与取消的原子单位；批内失败整批回滚，批间是取消点，已提交批在部分失败时保留。
_Avoid_: 事务（粒度更细的词，单独指 realm 事务时才用）

**DriftGuard**:
写事务前后对目标库结构的一致性校验；任何漂移触发整批回滚并上报工具缺陷。
_Avoid_: 校验器（泛）

**占用（Occupied）**:
游戏进程运行中或 realm 会话持有库的状态；投放写入前必须排除占用，收集只读不惧并发但开库会因锁失败。
_Avoid_: 锁定、占用锁

**伴生文件（Companion Files）**:
realm 在库旁生成的运行文件（.lock/.management/.note）；打开库必然产生，但永不同步。
_Avoid_: 临时文件

**结构档案（Structure Archive）**:
从真实库采集的某（fork, 磁盘版本）的 class/列清单；fork 专有列判定与白名单求交的运行时权威。
_Avoid_: schema 缓存（易误会为可执行缓存）

**磁盘版本（Disk Version）**:
realm 库文件头的 schema 版本号；Ez2Lazer 编码为 官方版本×1000+Ez 修订号（如 52011）。LZSync 只读不写此值。
_Avoid_: schema 版本（口语化时可用，正式文档统一用磁盘版本）
