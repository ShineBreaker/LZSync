# LZSync 同步与适配体系设计

> 定位：LZSync = 管理同一台机器上多个 osu!lazer fork（官方 lazer、DeltaLazer、Ez2Lazer、g0v0）数据目录的同步工具（Go 后端 + mygo Web UI），尽量同步所有可同步内容（曲库、收藏、皮肤、成绩、设置）。
> 本文是施工蓝图：约束先于方案，每个决定标注依据。事实引用简写 `lazer-baseline` / `fork-delta` / `fork-ez2lazer` / `fork-g0v0` / `ezrealmsync-design` / `fork-comparison` / `go-realm-options`，均指 `docs/research/` 下同名调研文档（2026-10-24 完成，自包含，不依赖 `reference/` 存活）。
> 四端事实速览（fork-comparison §0/§1）：官方 lazer（schema 52，`client.realm`，目录 `osu`）、DeltaLazer（51，`client.realm`，`deltalazer`，Realm 与官方完全同构）、Ez2Lazer（**52011** = 52×1000+11，**`client_52011.realm`**，`osu-Ez2Lazer`，官方列全保留 + 10 规范列 + 7 张技能表）、g0v0（52，`client.realm`，`g0v0`，schema 零结构改动，连独立服务器 g0v0.top）。

---

## 1. 约束（先于一切）

以下为硬性约束。任何后续模块设计、实现与测试不得违背；与约束冲突的方案一律不作候选。

### 1.1 数据安全

| # | 约束 | 理由（一句） |
|---|------|------------|
| S1 | **永不写坏库**：只写目标库已有的列（写入集 = 白名单 ∩ 目标库实际 schema），全部写入走显式事务（事务边界 = 批，§3.3），事务末尾做写前/写后结构快照一致性校验（DriftGuard），任何漂移**整批回滚** | 官方客户端对无法打开的 `client.realm` 的处理是**删除重建**（lazer-baseline §1.3、§3.3），写坏库等于替用户清空数据 |
| S2 | **写前自动备份**：任何写入操作前自动生成时间戳备份（`client.realm` → 备份目录），未配置备份目录时退化为库同目录带时间戳副本；已存在同名备份绝不覆盖 | 覆盖不可逆；EZRealmSync 生产工具把「没配置目录也要留一份原件」定为不可省的退路（ezrealmsync-design §5.2） |
| S3 | **不改 schema 版本、绝不迁移**：动态模式打开（不传 schema）钉死磁盘版本；磁盘版本号只读不写；要让库升版本，引导用户用对应 fork 的客户端打开一次 | 版本号是客户端迁移的输入，擅自改写会让下一个客户端按错误路径迁移或直接拒开（ezrealmsync-design §3.2；fork-comparison R4：把 Ez 号写进官方库，官方客户端立刻 SchemaMismatch） |
| S4 | **软删优先**：删除 BeatmapSet/Score/Skin/ModPreset 一律写 `DeletePending = true`，由目标客户端下次开库时 `cleanupPendingDeletions` 兑现；唯一例外是收藏夹（无软删语义，整行 Remove，与官方一致） | 官方数据模型即软删（lazer-baseline §7.3-4；go-realm-options §1.2），物理删除绕过游戏侧清理与引用处理，风险不可控（ezrealmsync-design §5.1） |
| S5 | **目标库被占用时拒绝写入**：写入前做进程守卫（检测各 fork 可执行进程，全平台）+ 占用探测，失败即拒绝并把「请先关闭游戏」作为一等公民提示，不做等待重试写入。占用探测的**跨平台语义**：Windows 用独占打开探测（等价 EZRealmSync 的 `FileShare.None` 开-即-释，ezrealmsync-design §5.5）；**Unix 不把 `flock(client.realm)` 当防线**（无进程持该锁、恒成功=假阴性），以「尝试 realm-core 打开写入会话」为准——realm 自身的 `.management` 内部锁协调会拒绝并发打开（报 "Realm file is currently open in another process"，go-realm-options §1.3-7），即**写会话打开本身就是最强占用探测**，进程名扫描只负责提前给出友好提示 | Realm 跨进程共享同一库要求同架构且 core 版本兼容，写窗口 = 游戏进程关闭时（go-realm-options §1.3-7；ezrealmsync-design §5.5） |
| S5a | **file format 版本防线**：LZSync 携带的 librealm 钉死 20.1.x，与四端精确对齐（四端 Realm NuGet 全部 20.1.0，本仓库核验：`reference/{lazer,delta,Ez2Lazer,g0v0}/osu.Game/osu.Game.csproj` 依次 :40/:42/:44/:66 → 内置 core 20.1.1 / file format v24）；对打开失败的库（文件格式比自带 core 更新，未来某端升级 SDK 后的场景）一律拒绝读写并提示升级 LZSync，绝不尝试用更高版本 core 触碰用户库 | realm 文件格式 v10+ 的自动升级**不可逆**（无法被旧 SDK 打开，go-realm-options §1.3-2）——S1–S10 只防 schema 版本，此处补 file format 版本；同代对齐下无风险，防线针对未来分叉 |
| S6 | **File 表行与模型引用必须同一事务成对写入**；files/ 磁盘文件先于 Realm 行复制 | lazer 的 `Cleanup()` 会物理删除所有引用计数为零的 RealmFile 及其磁盘文件（lazer-baseline §1.4；fork-comparison R3）；先写行后拷文件则产生读流失败的坏引用（fork-ez2lazer §6 外部托管行即此后果） |
| S7 | **绝不写悬空行**：成绩在目标库找不到 `BeatmapHash` 对应难度即跳过并计数，绝不落地无谱面成绩；单独同步难度而目标缺父谱面集时报错阻止 | ScoreInfo.BeatmapInfo 官方允许为 null 但那是「谱面事后变化」的暂态；工具写入悬空行即制造坏数据（ezrealmsync-design §2.7、§2.8） |
| S8 | **在线 ID 不跨服务器对拷**：`Score.OnlineID/LegacyOnlineID`、`BeatmapSet.OnlineID`、`RealmUser.OnlineID` 只在两端连接同一服务器时可携带；任何端与 g0v0 之间绝不互拷（新行置 -1，更新行不触碰该列） | OnlineID 分属不同服务器的命名空间，对拷即张冠李戴（fork-g0v0 §7.5；fork-comparison R1） |
| S9 | **全库级覆盖永远是最后一步**：任何重建整库/整目录的操作先产临时产物、自检（版本号、结构、行数三查）通过后再原子替换原文件 | 前面任何失败原文件一字未动（ezrealmsync-design §4.2 的收窄链路先例） |
| S10 | **凭据与机器绑定数据绝不同步**：`game.ini` 的 `Token`/`Username`、`framework.ini`、`storage.ini`、`client.realm.lock/.management/.note`、`rulesets/`、`online.db`、各 fork 私有文件（`EzSkinSettings.ini`、`rulesets.json` 等）一律排除 | OAuth 凭据敏感且跨服务器错位、framework.ini 绑定显示器/音频设备、伴生文件是进程锁（fork-comparison §5.3 红灯清单） |
| S12 | **ini 类文件原子写**：game.ini 的任何写入走「写前备份原件 → 临时文件 → rename 原子替换」，绝不就地覆写 | ini 无事务概念，游戏关闭的写入窗口内 LZSync 崩溃会留半份文件；EZRealmSync 不做 ini，无先例可搬，须自补此纪律 |

### 1.2 自包含

- **C-1 代码与文档不得依赖 `reference/` 存在**：`reference/`（各 fork 与 EZRealmSync 的源码镜像）将来会被整体删除；全部 fork 知识已摘录进 `docs/research/`（各调研文档头部声明自包含）。落法：代码内 fork 知识（列白名单、目录名、版本编码）以 `docs/research/` 摘录为准写成内部数据/fixture；运行时结构信息以「从真实库采集的结构档案」为准（见 §4.3），不从 `reference/` 读取任何东西。

### 1.3 平台与分发

- **P-1 分发形态 = 每平台产物目录 = Go 二进制 + 1 个 realm-core 共享库（`librealm.so`/`.dylib`/`realm.dll`）+ 资源**，不做「严格单二进制」承诺。理由：purego 只能 Dlopen 动态库，realm-core 无官方预编译产物，必须自建三平台构建 CI 并把版本钉在与客户端对齐的 20.1.x（go-realm-options §3、§7-1）；仓库产物已是 `build/linux-amd64/` 目录形态（go-realm-options §1.1）。目标平台：linux-amd64、windows-x64、macos-universal。
- **P-2 native 库加载失败时应用可启动**（进入降级模式，只显示诊断与只读探测不可用），不闪退。理由：sidecar 缺失/架构不符是真实分发场景（go-realm-options §8-3 对 mygo sidecar 机制标注未确认）。
- **P-3 数据目录按平台规则解析**：Linux `$XDG_DATA_HOME`（默认 `~/.local/share`）、Windows `%APPDATA%`、macOS `~/Library/Application Support`（lazer-baseline §1.1；Windows/macOS 的确切顺序未实证，运行时以「目录存在 + 库文件存在」探测兜底，见 §3.2）。

### 1.4 mygo 架构

- **M-1 Go 后端承载全部系统能力**（Realm 读写、文件搬运、进程守卫、备份），Web 前端只经 typed bindings 调用、不直接触碰文件系统或 native 库。理由：现有骨架即此形态（main.go:39 `mygo.Bind(Greeter{})`；mygo.config.ts:13 `bindings: "src/mygo.ts"`；`mygo generate` 把 Go 方法生成为 src/mygo.ts 中的类型化函数——main.go:11-12 注释、src/mygo.ts:1）。
- **M-2 长任务进度经 mygo 事件推送**（`mygo.NewEvent` 模式，main.go:36 的 `Tick` 即先例），同步执行不阻塞 UI 线程。
- **M-3 前端为 vanilla TypeScript + Vite**（src/main.ts 直接 DOM 操作，无框架依赖；tsconfig.json strict + noUncheckedIndexedAccess），不引入重框架。
- **M-4 UI 服务接口做成「真实后端 / Mock 后端」可切换**。理由：EZRealmSync 的 `--ui-test` Mock 后端结构让 UI 可在没有 native 库的环境开发与冒烟（ezrealmsync-design §1.1、§6.1-9）。

### 1.5 范围

**做**（按 fork-comparison §5 绿灯/黄灯清单收敛）：

1. 四端数据目录发现、注册、身份识别（fork 指纹 + 磁盘 schema 版本探测，含 Ez 的 `official×1000+ez` 编码解码）。
2. 只读对比预览：任意两库间按内容键做集合差异（源独有 / 目标独有 / 冲突）。
3. 单向同步（用户显式选源→目标；计划切批执行，事务边界 = 批，§3.3）：谱面集与难度（含 files/ 成对搬运）、收藏夹、用户皮肤、本地成绩、键位（RealmKeyBinding）、规则集设置（RealmRulesetSetting）、ModPreset、game.ini 白名单键。
4. 差异适配：Ez 追加列保护与丢弃、Ez 7 张技能表/外部托管谱面集/BMS 规则集过滤、g0v0 市场规则集行过滤、成绩 mod 白名单与 StatisticsJson 未知键过滤、OnlineID 命名空间规则、delta gimmick 谱面检测告警——**每一条被丢弃/跳过的数据必须计数并给出原因，不静默吞掉**（ezrealmsync-design §4.4 的核心承诺）。
5. 写前备份、软删、进程守卫、执行报告与备份恢复入口。

**明确不做**（及理由）：

| 不做 | 理由 |
|------|------|
| schema 迁移 / 版本升降 | 各 fork 客户端自己的职责；工具做迁移必然与客户端竞速（S3；ezrealmsync-design §3.2） |
| 双向自动同步、后台监听、定时调度 | EZRealmSync 无此物且同步窗口要求游戏关闭（S5）；自动双向在无冲突解决语义下会来回覆盖（ezrealmsync-design §6.3「没有的东西」清单） |
| 服务器间数据对拷（在线 ID、排行榜） | 命名空间隔离（S8） |
| 改写 `.osu` 文件内容（含 gimmick section 的注入/剥离） | 文件内容是哈希体系的根，改写即破坏内容寻址与成绩锚点；delta gimmick 只做检测与告警（fork-delta §7.2：以 delta 侧为权威源） |
| stable 数据库（scores.db/collection.db）导入导出、`.osr` 批量导出 | EZRealmSync 的周边功能，与同步内核解耦（ezrealmsync-design §6.3）；需要时后续单列 |
| 库修复（非法字符修复、僵尸文件扫描） | 同上，周边功能 |
| 三方合并 / 多源仲裁 | N 路同步迟早需要，但 M1–M3 不做；冲突一律「用户在预览中拍板」（ezrealmsync-design §6.2-4 指出其覆盖策略永远是源覆盖，LZSync 同期不超越） |
| 网络/远程同步 | 单机工具（任务定义） |

---

## 2. Realm 技术选型

**结论：读 + 写都走 purego 绑定 realm-core C API 的动态模式**（go-realm-options §0/§6.2 推荐路线②），复刻 EZRealmSync 已在生产验证的手法：动态打开（不传 schema）→ 钉死磁盘 schema → 只写目标库已有列 → 事务写入 + 结构漂移校验。

选型依据（go-realm-options §6.1 总表）：

| 维度 | 评估 |
|------|------|
| 读写能力 | 动态模式读写俱全，与 EZRealmSync 的 `IsDynamic = true` 打开一一对应（go-realm-options §3） |
| 上游健康度 | realm-core 是全生态公共基座且仍活跃（v20.1.5，2026-06）；realm-dotnet 停摆恰反证直连引擎层的抗风险性（go-realm-options §1.3-5、§6.2） |
| 工程先例 | EZRealmSync 把「动态模式安全读写 lazer 系 .realm」完整方法论跑通在生产工具（ezrealmsync-design 全文）；realm-dart 是 FFI 消费 realm-core C API 的官方先例（go-realm-options §1.3-6） |
| 基建成本 | purego v0.11.1 已在 LZSync 依赖树（go.mod:8，mygo 带入），零新增；绑定子集约 30–60 个函数（go-realm-options §3 工作量估计）。**工作量修正**：30–60 函数只是核心标量读写面；完整写路径还需动态 list 操作（`Beatmaps`/`Files`/`BeatmapMD5Hashes`/`Pauses` 全是列表列）、embedded 对象创建（`BeatmapDifficulty`/`BeatmapUserSettings`/`RealmUser`/`RealmNamedFileUsage`）、对象链接写入、以及成绩挂接所需的目标库按 `BeatmapHash` 查询（C API 查询语言或全表扫描 + 自建哈希索引）——排期按核心面的 1.5–2 倍留余量（macos-universal 的 CI 构建机成本另见待定 T9） |
| 许可 | realm-core Apache 2.0，允许随产物再分发，仅需归属声明（go-realm-options §3） |

**被否方案**：

1. **纯 Go 解析 .realm 二进制**（路线①）——`.realm` 无公开格式规范（realm-core issue #3691：格式「只应经 realm-core 操作」）；公开纯解析器全部停在 v9/v9.9，而 lazer 系在 file format v24，隔 15 个格式版本；写路径基本不可行且做错即产出客户端打不开的库（go-realm-options §2）。
2. **.NET 子进程助手**（路线③）——realm-dotnet 主仓库一年多无提交、CI 损坏（issue #3729），且官方不支持 NativeAOT 而工具场景必需的 Dynamic API 恰是被建议避免的（issue #3417）；分发变为双运行时（go-realm-options §4）。
3. **完全不碰 .realm**（路线④）——读不了库内现状、做不了对比与差异同步，能力天花板是「推送导入」（go-realm-options §5）。**保留为降级路径**：生成 `.osz/.osk/.osr` 容器拉起客户端导入（lazer 支持启动参数导入，`OsuGame.Import(ImportTask[])`，go-realm-options §5-1），在用户环境无法部署 native 库或只需推送谱面/皮肤时使用（M3 实现）。

**必须处理的事实约束**（go-realm-options §3）：

- 打开库需在库旁创建 `.lock`/`.management/`/`.note`，「只读」也只是逻辑只读 → 打开必配可写临时目录（FallbackPipePath 对应配置）；伴生文件不同步（S10）。
- 写窗口与游戏进程互斥 → 进程守卫（S5）。
- 句柄生命周期纪律：所有 `realm_*_new` 返回物必须 `realm_release`；事务一律 `begin_write → 操作 → commit/cancel`；打开必带显式只读/读写意图。
- 避开 C 回调：动态读写字集不需要 sync/migration/notification 回调，绕开 purego 函数指针的薄冰区。

**PoC 门槛（先过再铺开，go-realm-options §7-7）**——两道验收线，**任一失败都同等触发回落评估**：

1. **只读链路**：用 purego 打开本机一份真实 `client.realm`（只读）→ 枚举全部 class → 读一行 BeatmapSetInfo → 安全关闭，且打开前后文件头字节不变。
2. **显式 schema 建库链路**：用 C API 按 schema 镜像 fixture（`realm_config_t` 的 schema 字段 + `realm_schema_t` 构造）建一个空库并成功写入一行——**整个测试体系单点依赖此能力**（§8.2 全部「对构造库断言」以它为前提）。本仓库无 realm-core 源码可核验 realm.h 是否暴露 schema 构造面（EZRealmSync 的 `OpenDynamicWithSchema` 落新库只证明 realm-dotnet/C++ 层有此能力，推不出 C API 暴露了它——如实标注：**无法从调研判定，只能靠 PoC 实测**）。

purego × realm C API 无长期生产先例（go-realm-options §8-6）。**PoC 失败的回落序**（注意：路线③已在本节被否——realm-dotnet 停摆 + NativeAOT 不支持工具必需的 Dynamic API——**不作为回落目标**）：

- 只读链路过、建库/写链路不过 → 砍写路径保读路径（对比预览/诊断可用），写路径降级到路线④；
- 只读链路也不过 → 回落路线④（容器导入推送，能力阉割：无库内读取、无对比、仅推送导入），并重新评估项目范围是否仍成立。

---

## 3. 总体架构

### 3.1 模块划分（Go 包 + 前端）

| 模块 | 职责 |
|------|------|
| `internal/realmc` | purego 绑定子集与动态会话：`OpenForRead/OpenForWrite`（不传 schema、钉死磁盘版本）、class/property 枚举、动态属性读写、事务、磁盘版本探测、结构档案采集、句柄生命周期、last_error 解码 |
| `internal/forks` | ForkProfile 注册表（四端元数据：数据目录名、库文件名模式、进程名、版本编码、扩展列/表、专有规则集/皮肤、服务器指纹）+ 库身份识别（指纹判定）+ 结构档案仓库（`结构档案/{fork}-{diskVersion}.json`，从真实库顺手采集、同版本只留首份、坏文件按不存在处理——ezrealmsync-design §4.1） |
| `internal/neutral` | 中立数据模型（DTO）+ 读取投影（库 → DTO，跳过软删行/Hidden 难度等）+ 写入器（DTO → 库：就地更新或新建、列白名单交集、fork 专有列捕获回写、软删） |
| `internal/diff` | 对比引擎：两侧投影按内容键建索引，三分类（源独有/目标独有/冲突）+ 冲突摘要 |
| `internal/adapter` | 目标端兼容适配：按 (源 fork, 目标 fork) 生成过滤/转换规则（丢弃集、mod 白名单、StatisticsJson 键过滤、OnlineID 规则、gimmick 检测），产出「计划项 = 动作 + 跳过原因」 |
| `internal/engine` | 同步编排：计划 → 执行（守卫 → 备份 → files 先行 → 单事务写入 → DriftGuard）→ 结果报告（applied/skipped+原因/备份路径） |
| `internal/filestore` | files/ 目录解析（三级回退）+ CopyMissing（按哈希集合、目标已有跳过、同根 no-op）+ 复制后哈希复核 |
| `internal/guard` | 进程守卫（进程名 + 排他锁双重探测）、时间戳备份与目录管理、写入后 mtime 戳（mmap 写入不更新 mtime 的坑，ezrealmsync-design §5.6） |
| `internal/inisettings` | game.ini 白名单键同步（共有键拷贝、剔除红灯键） |
| `internal/appsvc` | mygo Bind 的 UI 服务层（LibraryService / CompareService / SyncService / SettingsService）+ 进度事件；可切换 Mock 实现（M-4） |
| `src/`（Web UI） | 四页前端（§6），经 src/mygo.ts typed bindings 调用 |
| `tools/build-librealm`（CI） | realm-core 三平台构建流水线，产物钉 20.1.x，作为受管资产随版本发布（go-realm-options §7-1） |

依赖方向：`appsvc → engine → {diff, adapter, neutral, filestore, guard} → {realmc, forks}`；`engine` 之上无业务逻辑。`realmc` 不 import 任何领域包（纯绑定层）。

### 3.2 数据流

```mermaid
flowchart TD
    A[发现库<br/>扫描默认路径 + 手动注册] --> B[解析<br/>探测磁盘 schema 版本 / fork 指纹识别<br/>files/ 路径解析 / 进程占用状态]
    B --> C[对比<br/>两侧只读投影 → 内容键索引<br/>三分类: 源独有/目标独有/冲突]
    C --> D[计划<br/>目标端适配规则过滤<br/>产出动作清单 + 跳过原因预演]
    D --> E[预览<br/>UI 呈现清单, 用户勾选/调整/拍板冲突]
    E --> F[执行<br/>守卫复查 → 自动备份 → files/ 先行复制<br/>单事务写入(含 File 表行) → DriftGuard]
    F --> G[校验与报告<br/>结构零漂移确认 / 行数核对<br/>applied·skipped+原因 / 备份路径]
```

- **发现**：按 ForkProfile 内置目录名扫描平台数据根（§1.3 P-3），Ez 侧额外识别 `client_52011.realm` 及其前驱搜索链 `client_52010.realm → … → client.realm`（fork-ez2lazer §2.2）；用户可手动注册自定义路径（storage.ini 自定义目录场景，lazer-baseline §1.3）。
- **解析**：磁盘版本探测先走动态只读开库读 handle 版本（对应 EZRealmSync `ReadSchemaVersionFromHandle`）；**开库探测在游戏运行时会因 realm-core 进程锁失败**（go-realm-options §1.3-7），降级链依次为：文件名推断（仅 Ez 的版本化文件名 `client_52011.realm` 有版本可退；官方/delta/g0v0 的 `client.realm` 无版本可退，ezrealmsync-design §3.1-3）→ **结构档案缓存**（该库路径的历史采集记录——「碰过哪个版本就有哪个版本的快照」，ezrealmsync-design §4.1）→ 显示「未知（目标运行中）」并标注取值为缓存而非现场。`≥1000` 时解码 `upstream = v/1000, ez = v%1000`（fork-ez2lazer §2.1）。fork 身份 = 版本编码 + 结构指纹（专有表/列存在性，如 Ez 的 `JudgeMode` 列、7 张 `Ez*` 表；`HasEzFingerprint` 思路泛化，ezrealmsync-design §6.2-2）。
- **对比/计划/预览**：见 §5。
- **执行** = 切批流水线（事务边界 = 批，含批内「守卫已过 → 批 files 先行 → 单批事务 → DriftGuard」与批间取消点/部分完成语义），完整执行模型见 §3.3。

### 3.3 执行模型：事务边界 = 批；错误与回滚

EZRealmSync 的单事务 upsert 先例（ezrealmsync-design §2.4）是 GUI 勾选行的小批量；LZSync 面对整曲库（千级谱面集 / 万级行 + File 行），巨事务的 mmap 写时复制内存不可控、commit 时长长、且任一写入异常整笔回滚会让几千谱面因一行坏数据全失败。因此：

- **一次同步（计划）→ 切批执行；一个批 = 一个事务**。批是原子单位（S1 的回滚承诺按**批**兑现）、进度单位、取消单位。
- **批切分策略（按对象类型，默认值可在设置页高级项调整，M2 以构造库实测校准后固化）**：谱面集 ≤ 50 集/批（含难度、Metadata、RealmNamedFileUsage 与对应 File 行）；成绩 / 收藏 / 皮肤 / 键位 / 规则集设置 / ModPreset ≤ 500 行/批。按行数而非字节计——Realm 事务内存 ≈ 修改集大小，千行级可控。
- **批内顺序**（S5→S2→S6 纪律）：守卫在同步启动时一次通过 → **备份在首批前一次性生成**（全计划共用同一份备份，作为「恢复到同步前」的基准）→ 每批先 CopyMissing 该批涉及的 files/ 哈希 → 再开写事务（File 表行与模型行同事务，S6）→ DriftGuard → commit。
- **批间是取消点**：用户取消 → 停止后续批，已提交批保留，报告部分完成；批事务进行中不可打断（保证批原子性）。
- **部分失败语义**：批 N 失败（写入异常 / DriftGuard / files 复制不符）→ 批 N 整体回滚并停止；**批 1..N-1 已提交的结果保留**（部分完成是合法终态，不强制整体撤销）；报告口径区分「已提交批（applied 计数）/ 失败批（整批回滚 + 原因）/ 未执行批（清单）」，并提供**从失败处重续**入口——计划项按内容键幂等（§4.2），重跑已提交项自然判为「已存在 → 跳过」，重续安全。
- **执行期现场复核**：预览与执行之间目标库可能漂移（用户搁置后开过游戏、跑过别的同步）。每个计划项在执行时**以目标库现场状态重跑身份匹配与 adapter 判定**（EZRealmSync 的 upsert 是执行时 Find 的同款纪律，ezrealmsync-design §2.4）；现场判定与计划不符 → 按现场判定执行并计入报告「执行期改判」分类（如目标已出现同键行 → 由「新建」改判「就地更新」或「跳过」），绝不按计划快照盲目写入。

| 层级 | 策略 |
|------|------|
| 批事务内 | 任何对象写入异常 → `cancel` 回滚**整批**；DriftGuard 发现写前/写后结构差异 → 回滚 + 上报「工具缺陷，请报告」（该错误只可能是 LZSync bug，ezrealmsync-design §3.2） |
| files 阶段（批内先行） | 复制失败/哈希复核不符 → 本批与后续全部中止（多余的已复制文件无害：未引用文件会被目标客户端 Cleanup 清理，fork-ez2lazer §7-F） |
| 备份 | 先于首批生成（S2）；报告页提供「从备份还原」入口（校验后覆盖，还原前可再做安全备份，ezrealmsync-design §5.2） |
| 用户可解错误 | 收敛为四值枚举：`FileInUse`（占用）/ `PathConflict` / `NativeLibMissing` / `SchemaModelMismatch`，每个带用户动作指引（ezrealmsync-design §5.5 三值枚举 + P-2 的 native 缺失） |
| 崩溃面 | Realm 事务原子性保证崩溃不留半批；恢复语义同「部分失败」——已提交批保留、崩溃批与未执行批可重续；最坏情况用备份人工恢复 |

---

## 4. 通用数据模型

### 4.1 中立表示（跨 fork 公共投影）

**中立模型 = 官方 v51 基线列集合上的强类型 DTO**。依据：四端共有列的权威定义即 EZRealmSync 官方基线白名单（与官方 schema V51 对齐，ezrealmsync-design §2.3 全文摘录），且 fork-comparison C2 判定「官方 v51 列集合是四端都完整具备的最大公共列集」（delta 停在 51 无 v52 的 `RealmOnlineAsset`；Ez 在 52 之上只加不删；g0v0 与 52 全等）。

DTO 清单（字段 = C2 白名单逐列，结构定义见 lazer-baseline §2）：

```
NeutralBeatmapSet    { ID: Guid, OnlineID, DateAdded, DateSubmitted?, DateRanked?,
                       StatusInt, DeletePending, Hash, Protected,
                       Beatmaps: []NeutralBeatmap, Files: []NeutralNamedFile }
NeutralBeatmap       { ID, DifficultyName, RulesetShortName, StatusInt, OnlineID,
                       Length, BPM, Hash, StarRating, MD5Hash, OnlineMD5Hash,
                       LastLocalUpdate?, LastOnlineUpdate?, Hidden,
                       EndTimeObjectCount, TotalObjectCount, LastPlayed?,
                       BeatDivisor, EditorTimestamp?,
                       Difficulty: NeutralBeatmapDifficulty, Metadata: NeutralMetadata,
                       UserSettingsOffset }
NeutralMetadata      { Title, TitleUnicode, Artist, ArtistUnicode,
                       Author{OnlineID, Username, CountryString}, Source, Tags,
                       UserTags, PreviewTime, AudioFile, BackgroundFile }
NeutralCollection    { ID, Name, BeatmapMD5Hashes: []string, LastModified }
NeutralSkin          { ID, Name, Creator, InstantiationInfo, Hash, Protected, DeletePending, Files }
NeutralScore         { ID, BeatmapHash, RulesetShortName, ClientVersion, Hash, DeletePending,
                       TotalScore, TotalScoreWithoutMods, TotalScoreVersion, LegacyTotalScore?,
                       BackgroundReprocessingFailed, MaxCombo, Accuracy, Date, PP?, OnlineID,
                       LegacyOnlineID, User{...}, ModsJson, StatisticsJson, MaximumStatisticsJson,
                       Pauses, RankInt, Combo, IsLegacyScore, Files }
NeutralRulesetRef    { ShortName, OnlineID, Name, InstantiationInfo, Available }  // 仅解析引用，不做同步载体（见 §5.1）
NeutralNamedFile     { Hash, Filename }
```

投影规则（读取端，ezrealmsync-design §2.2 的跳过集）：

- 跳过 `DeletePending == true` 的 BeatmapSet/Skin/Score、`Hidden == true` 的难度、父集已软删的难度；
- 跳过 `Protected == true` 的系统谱面集（系统保护集不可删、多为端自备，同步无收益——lazer-baseline §2.4）与 `Protected == true` 的内置皮肤行（内置六皮肤固定 Guid 四端自备、天然幂等，lazer-baseline §2.10 / fork-comparison C6——同步它们无收益）；
- 跳过 fork 专有皮肤（ForkProfile 声明）；
- 跳过 Ez 外部托管谱面集（`Hash` 以 `ext:set:` 前缀或 `HostingKind == External` 判定，fork-ez2lazer §5.1）；
- 目标库缺某列（版本更老）时该列不进 DTO（列缺失自然降级，ezrealmsync-design §3.3）。

### 4.2 身份与对账：分层键，不以 Guid 为身份

EZRealmSync 按 `(EntityKind, Guid)` diff 成立的前提是 A/B 同属一个库系；**LZSync 跨 fork 独立安装库，同一 `.osz` 各端导入产生不同随机 Guid**（lazer-baseline §6.3「ID 是随机 Guid，每台机器不同」；fork-comparison R5）。内容哈希键还有一个内生性质必须显式处理：**BeatmapSet/Beatmap 的 Hash 是内容哈希，谱面更新后必变**（lazer-baseline §6.1）——只用内容键对账，会把「同一谱面的新版本」拆成互不相干的两个条目、永远进不了冲突分支。因此对账分两层：

**第一层：内容键（判定「同一版本」）**

| 实体 | 内容键 | 依据 |
|------|--------|------|
| BeatmapSet | `Hash`（集内全部 .osu 拼接 SHA-256）——语义是**版本指纹**而非谱面身份 | lazer-baseline §6.1 |
| Beatmap | `Hash`（归一化 .osu SHA-256）——版本指纹 | 同上 |
| Score | `(BeatmapHash, Hash)`（.osr 内容），`Date` 消歧 | lazer-baseline §7.3-6 的跨库去重组合 |
| Collection | `Name`（用户心智名）；`BeatmapMD5Hashes` 排序指纹作冲突判据 | 官方收藏即设计为跨库可移植（lazer-baseline §2.8；ezrealmsync-design §2.2 的 CollectionHashFingerprint） |
| Skin | `Hash`（非空时）；为空的行跳过（无法判定身份） | lazer-baseline §6.1 |
| KeyBinding | `(RulesetName, Variant, ActionInt)` | lazer-baseline §2.12 逻辑键 |
| RulesetSetting | `(RulesetName, Variant, Key)` | lazer-baseline §2.13 逻辑键 |
| ModPreset | `(RulesetShortName, Name)` | lazer-baseline §2.11 无内容哈希，取业务键 |

内容键相等 → 同一版本，进入「一致/冲突」判定。**冲突字段只取内容键之外的字段**（BeatmapSet 比 `OnlineID`/`DateAdded`/`StatusInt`/`Title`；Beatmap 比 `DifficultyName`/`RulesetShortName`/`StarRating`；Score 比 `Date`/`TotalScore`/`RankInt`；Skin 比 `Name`/`Creator`）——**内容键本身（如 Hash）不作冲突字段：键匹配时它必相等，比它是死代码**；字段清单数据化于 adapter，随 fork 演进维护（ezrealmsync-design §6.2-4）。

**第二层：谱面身份键（判定「同一谱面的不同版本」）**

对内容键不等的 BeatmapSet，用次级键识别「同谱面新版本」；识别成立的对进入 diff 的第四类**版本更新**（而非「源独有 + 目标独有」两个孤立条目）：

| 适用范围 | 谱面身份键 | 可靠性与默认动作 |
|---------|-----------|----------------|
| 同服务器两端（lazer↔delta↔Ez，`ServerRealm` 相同） | 集级 `OnlineID > 0` 且相等 | 强键：同一 beatmapset 在官方服务器的稳定 id（lazer-baseline §2.4），且 S8 本就允许同服务器同步 OnlineID → 默认按版本更新执行 |
| 跨服务器（任一端为 g0v0），或两端任一 `OnlineID = -1`（本地谱面） | 元数据键 `(Artist, Title, Author.Username)`（取首难度 Metadata）相等 → **疑似**同谱面 | 弱键：同名同作者不同谱面存在误判可能 → 归入「需用户确认」组、默认不勾选（ask 策略，fork-comparison §3），用户拍板后方可按版本更新执行 |

难度层不做独立版本对账：集级身份成立后**整集按一个新版本处理**——BeatmapInfo 无软删列、无独立清理通道（软删仅在 Set/Score/Skin/ModPreset，lazer-baseline §2.5、fork-comparison C8），难度行的生命周期必须挂在可软删的父集上管理（处置语义见 §5.4）。

**diff 四分类**（在 EZRealmSync 三分类上加第四类，ezrealmsync-design §2.2）：**源独有 / 目标独有 / 冲突**（内容键相等、非键字段不同）/**版本更新**（内容键不等、谱面身份键相等）。

**Guid 与行复用原则（写入器）**：

- 内容键相等（同版本）→ **就地更新目标行白名单列**（保目标 Guid 与目标端本地引用）；同 Hash 的谱面集不替换 `Beatmaps` 列表（内容等价，集 Hash = 全部 .osu 拼接，同 Hash 必同难度内容集合）。
- 目标缺该实体 → 以源 Guid 新建行（保持目标库内跨表引用一致）。
- **绝不删除目标端可能被本地引用的行**；跨表关系（Beatmap↔Set、Score→Beatmap、Files）在目标库内现场解析重建，不搬源库 Guid 引用（ezrealmsync-design §4.2 Copier 的 `targetBySource` 映射思想）。
- **BeatmapMetadata 是独立 RealmObject 表（每难度一行），非 EmbeddedObject**（lazer-baseline §2.5）：新建难度时新建 Metadata 行；就地更新难度时**复用其既有 Metadata 行就地更新**——lazer 的 Cleanup 只清 RealmFile、不清孤儿 Metadata（lazer-baseline §1.4），不复用会无限积累垃圾行。
- ModPreset 的 `Ruleset` 是关系列（lazer-baseline §2.11）：写入前走目标端规则集解析（同 §5.2-4 逻辑：目标已有同名行则复用；官方四规则集缺行可新建；fork 专有规则集 → 该 ModPreset 跳过并计数）。KeyBinding 的 `RulesetName` 是字符串列非关系列（lazer-baseline §2.12），无需解析，仅 ActionInt 枚举域过滤（§5.4）。

### 4.3 ForkProfile：差异适配层（N 个 fork 的泛化）

EZRealmSync 的 `KnownProperties`/`EzOnlyPropertyNames`/`ez_only_rulesets`/`IsEzOnlySkin` 全是硬编码的 1→1 假设（ezrealmsync-design §6.2-1）；LZSync 泛化为「每 fork 一份声明 + 运行时结构档案求交」：

```go
type ForkProfile struct {
    ID            string        // "lazer" | "delta" | "ez2lazer" | "g0v0"
    DataDirName   string        // osu / deltalazer / osu-Ez2Lazer / g0v0（fork-comparison §1.1）
    RealmFilePat  string        // client.realm；Ez 为 client_<diskVersion>.realm + 前驱链
    ProcessNames  []string      // 进程守卫名单（lazer/ez 已知，delta/g0v0 待定 §8）
    DecodeVersion func(uint64) (upstream int, forkRev int)  // Ez: v>=1000 ? v/1000,v%1000 : v,0
    ServerRealm   string        // 服务器命名空间指纹（"ppy" vs "g0v0.top"），S8 判定用
    ExtColumns    map[string][]string  // 本 fork 在官方基线之外追加的列（Ez 的 10 列，fork-ez2lazer §2.4）
    ExtTables     []string      // 本 fork 专有表（Ez 7 张，fork-ez2lazer §2.5）
    OwnRulesets   []string      // 专有规则集 ShortName（Ez: bms[、diva 待定]；g0v0: OnlineID>=10 市场）
    OwnSkins      []SkinFingerprint // 专有皮肤指纹
    IniKeys       IniKeyPolicy  // game.ini 键策略（共有白名单 + 本端专有键，fork-comparison D12/D14/D15）
}
```

两个运行时数据源叠加在静态 Profile 之上：

1. **结构档案（schema 快照）**：对每个碰过的 (fork, diskVersion) 从真实库采集 class/列/类型清单，落 `结构档案/` 目录；同版本只留首份、采集失败只记日志（ezrealmsync-design §4.1 整套机制）。用途：① 写入集 = 官方 v51 白名单 ∩ 源库实际列 ∩ 目标库实际列；② fork 专有列 = 目标库实际列 − 同代官方基线档案（权威，优先于静态 `ExtColumns` 名单——EZRealmSync 用官方快照 diff 判定 Ez 加列的理由：权威且不用手维护，ezrealmsync-design §2.5）；③ fork 身份指纹识别。
2. **目标端兼容规则（adapter 输入）**：见 §5.4。

设计原则：**内核不知道任何具体 fork**；所有「Ez 怎样、g0v0 怎样」的知识只存在于 ForkProfile 与 adapter 规则数据中（ezrealmsync-design §6.3：「Ez 特有丢弃规则只应进 fork 元数据包，不进内核」）。新增第五个 fork = 新增一份 Profile + 规则，不改内核。

---

## 5. 同步引擎

### 5.1 对象复制白名单（SyncTables，分阶段启用）

| 阶段 | 表 | 说明 |
|------|----|----|
| M1 | `BeatmapCollection` | 最安全首写路径：无 files、无跨表关系、无 Guid 依赖（纯 MD5 列表，官方设计即跨库可移植，C4） |
| M2 | `BeatmapSet` + `Beatmap`（含 File 表行 + files/）、`Skin`、`Score` | 与 EZRealmSync 的 SyncTables 一致（ezrealmsync-design §2.3）；Ruleset 行不做同步载体（官方四规则集由目标端 RulesetStore 首启播种，lazer-baseline §2.9），仅在成绩/难度写入时解析引用：目标已有同名行复用、缺行且属官方四规则集则新建（`Available=true`）、fork 专有规则集不凭空插（ezrealmsync-design §2.7-4） |
| M3 | `RealmKeyBinding`、`RealmRulesetSetting`、`ModPreset`、game.ini | EZRealmSync 未纳入的增量面（fork-comparison C10 指出）；键位 ActionInt 按两端枚举域交集过滤（D20，见 §5.4） |

排除在白名单外（永不作为同步载体）：`RealmOnlineAsset`（在线缓存，无业务数据，D18）、Ez 7 张技能表（派生分析缓存，可由 Ez 重算，D4）、各 fork 专有表。

### 5.2 成绩：按 BeatmapHash 挂载与跳过策略

官方契约：`ScoreInfo.BeatmapHash`（Indexed）是成绩与谱面的强锚点，关系列允许 null（lazer-baseline §6.3）。写入端策略（ezrealmsync-design §2.7 全套先例）：

1. 目标库无 Score 表（版本过旧）→ 跳过「目标库没有 Score 表」；
2. `BeatmapHash` 为空 → 跳过；
3. **目标库 BeatmapHash 索引查不到 → 跳过并计数**（「目标库缺少对应谱面 Hash xxxxx」）；索引按目标全部非隐藏难度的 `Hash` 构建，**惰性构建于谱面/难度写入之后**（`??=` 语义），本次刚同步进来的难度也能挂上；
4. 规则集解析失败（fork 专有规则集在目标端不可解析）→ 跳过；
5. 挂接成功：`BeatmapInfo` 关系链到查到的目标难度对象 + `BeatmapHash` 列存字符串，回放 `.osr` 经 linkFiles 随行复制。

同规则集重复成绩去重：目标已有同内容键 `(BeatmapHash, Hash)` 行 → 就地更新白名单列（OnlineID 列不触碰，S8）。谱面**版本更新**（§5.4）时，目标端挂在旧版本难度上的本地成绩按官方谱面更新语义解绑（关系置 null、`BeatmapHash` 保留）——解绑发生在版本替换的同一批事务内。

### 5.3 files/ 复制与去重

- 机制：SHA-256 内容寻址、两级目录 `files/<h[0]>/<h[0:2]>/<hash>`、`RealmFile` 主键即哈希、全局去重（四端逐字符一致，fork-comparison C1）→ **文件层天然幂等无冲突**。
- 搬运 = CopyMissing：按计划涉及的全部哈希集合（集合 Files + 难度 Hash + 皮肤 Files + 成绩 Files），源 files/ → 目标 files/ 逐文件复制，`overwrite: false`（目标已有该哈希跳过；源 = 目标同一数据根时 no-op——ezrealmsync-design §2.6）。
- **顺序铁律（S6）：先 CopyMissing files/，后写 Realm 行**；`RealmFile` 行与引用它的模型行在同一事务内写（`Find(File, hash) ?? Create`，跨谱面共享 blob 行不重复建——ezrealmsync-design §2.6）。
- 复制后按哈希复核字节（lazer 的写入器即使文件已存在也会重读校验，lazer-baseline §1.4，对齐该纪律）。
- files/ 目录解析三级回退：库所在目录 `files/` → 所在目录名是 `data` 时取兄弟 `files/` → 父目录 `files/`（ezrealmsync-design §2.6）。
- 排除：Ez 外部托管 set 的路径哈希 `RealmFile` 行（files/ 无对应字节，带入即坏引用）随 set 连带丢弃（D5）。

### 5.4 冲突处理方向

- **方向模型**：一次同步 = 单向（用户显式选源与目标）。冲突行（内容键相等、**非键**字段不同——冲突字段清单见 §4.2，已剔除「比内容键」的死代码）默认动作 = **源覆盖目标（就地更新白名单列）**，预览页可逐项改为跳过。
- **本地行为字段默认不覆盖**：`LastPlayed`/`EditorTimestamp`/`UserSettings.Offset`/`Hidden` 默认保留目标值（多端同步会互相覆盖，D22）；设置页提供 LastWrite-wins 选项。`PP` 列同策略（g0v0 回填的 PP 与官方端语义不同，D10——两项调研均标「需用户确认」，故默认保守保留目标值）。
- **删除不传播**：软删行在读取投影中当作不存在（ezrealmsync-design §5.1）；源端删了某谱面不导致目标端删除（单向覆盖语义下无「墓碑」）。**注意区分**：下述「版本更新的旧版本软删」不是删除传播——它是「同一谱面换新版本」这一原子动作的一半，目标行因**版本被替换**而软删，不是因为源端删了它。
- **版本更新的处置（§4.2 第四类的动作语义）**——默认动作 = **整集原子替换**，单批事务内完成三步：
  1. **写入新版本整套**：新集行以源 Guid 新建 + 全部难度/Metadata/Files/RealmFile 行；未变难度的 blob 已在目标 files/，CopyMissing 自然跳过（内容寻址幂等，§5.3）。
  2. **软删目标旧版本集**：`DeletePending = true`（BeatmapSetInfo 有软删列，lazer-baseline §2.4；fork-comparison C8）。
  3. **解绑旧难度上的本地成绩**：目标端 `BeatmapInfo` 关系指向旧集难度的 Score 行，关系列置 null（`BeatmapHash` 字符串列保留作历史锚点）——这是官方 `UpdateLocalScores`「谱面变化时先解绑全部成绩」的保守子集（lazer-baseline §6.3）；不解绑则客户端兑现软删、清理旧难度行时会撞上悬挂关系。
  - 旧难度行与旧文件引用的物理清理由目标客户端 `cleanupPendingDeletions` 兑现，级联是客户端职责——与 S4 同一委托模型（EZRealmSync 数据页删除谱面集即只写标记、不管子行，ezrealmsync-design §5.1）。
  - **为什么不「把新难度并入旧集行就地更新」**：BeatmapInfo 没有 DeletePending 列（lazer-baseline §2.5），从旧集 `Beatmaps` 列表移除旧难度关系只会制造无清理通道的孤儿行；整集替换让全部旧行的生命周期挂在可软删的父集上。
  - 目标旧版本上的目标端本地成绩：按官方谱面更新语义成为「谱面缺失」成绩（`BeatmapHash` 指向旧 Hash、关系为 null）——lazer 谱面更新的内生行为，非坏数据（lazer-baseline §6.3）；源端新版本的成绩随同步按新 `BeatmapHash` 挂上新难度（§5.2）。
  - 元数据键「疑似」版本更新（§4.2 弱键）：默认动作 = 跳过，预览呈现由用户三选一拍板：版本替换 / 双版本并存（不软删旧版本，提示目标端将见两套谱面）/ 忽略。
- **目标端兼容过滤（「能少不能错」，ezrealmsync-design §4.3）**：向低兼容目标写入前按 (源 fork, 目标 fork) 应用规则，被拒的行**计数 + 人话原因**：

| 目标 | 过滤规则 |
|------|---------|
| 非 Ez 端 | Ez 专有规则集（bms 等）的 Ruleset 行与成绩；`JudgeMode/HealthMode ≠ 0` 的成绩（官方重算会算错，fork-comparison R7）；外部托管 set 连带行（D5）；Ez 专有皮肤（D21）；StatisticsJson/MaximumStatisticsJson 中目标端 HitResult 枚举解析不了的键（过滤后写，解析失败的 mod 名录外的成绩整条跳过，D7）；mods acronym 不在目标端名录的成绩（白名单制，宁拒勿杀，ezrealmsync-design §4.3） |
| 任意端 | g0v0 市场规则集行（OnlineID ≥ 10）及指向它的成绩（D8）；跨服务器 OnlineID（S8）；键位 ActionInt 超出两端枚举域交集的行（D20）；ModPreset 的 ModsJson 过目标端 mod 白名录（D7） |
| delta 谱面流出 | 检测 `.osu` 内 `[BeatmapSectionGimmicks]`/`[BeatmapHitObjectGimmicks]` 头存在 → 预览页显著告警「此谱面经非 delta 端编辑保存会静默丢失 gimmick 数据」（R6），默认仍可同步（官方端只读不编辑则安全，fork-delta §5.2），双向回传链路（delta→官方→编辑→回 delta）由告警阻断 |

### 5.5 谱面状态语义（任务假设的核验结果）

任务背景称「私服常把所有铺面当 approved 以允许上传成绩」。**调研在四端客户端代码中均未发现任何强制 approval / 绕过在线状态检查的改动**：`BeatmapOnlineStatus` 枚举（-4…+4）在 delta/Ez/g0v0 三侧逐字节/零差异（fork-comparison C7；fork-delta §4.1、fork-ez2lazer §4.1、fork-g0v0 §5.1），成绩提交本身不受本地状态约束、由服务端裁决（lazer-baseline §5）。g0v0 的差异全在服务器侧（g0v0.top），**其服务器是否放宽谱面状态语义未确认**（fork-g0v0 §8、fork-comparison U9）。

因此设计采取保守立场，**不建立在「状态被篡改」的假设上**：

1. `StatusInt` 列随白名单原样搬运，LZSync 绝不解释、不改写状态值（同步工具没有裁决「这个谱面该是什么状态」的信息；Ez 外部托管 set 入库时标 `LocallyModified` 即是 fork 自己的语义，随行带走）；
2. 状态差异不作为同步过滤条件（不因「源端 Ranked、目标端 None」而改写或跳过——在线元数据由目标端客户端自行更新，lazer-baseline §2.5 `ResetOnlineInfo`）；
3. **待定**：若后续取得 g0v0-server 证据表明其谱面状态语义与官方不同（如全部标 Ranked/Approved 以计 PP），再评估是否在 g0v0↔其他端规则中加入状态列的处置策略。所需证据：g0v0-server 仓库的谱面元数据下发逻辑，或 g0v0 库中 `StatusInt` 分布的实测取样。

---

## 6. UI 规划（mygo 页面结构）

四页导航（左侧栏常驻：库状态灯 + 全局「游戏运行中」警示条）。每页核心交互：

### 6.1 库管理（Libraries）

- **自动发现**：一键扫描平台数据根 → 按 ForkProfile 识别四端，列出候选（目录、库文件、大小）。
- **库卡片**（每个注册库一张）：fork 身份与图标、数据根路径、磁盘 schema 版本（Ez 显示 `52011 = 官方 52 + Ez 11` 解码；**版本值带来源标注**——现场开库探测 / 文件名推断 / 结构档案缓存 / 「未知（运行中）」，游戏运行时开库探测会失败，降级链见 §3.2）、files/ 解析结果与体积、进程运行状态（实时）、上次备份时间、健康检查（可读性探测 / 伴生文件状态 / 剩余空间）。
- **手动注册/移除**：自定义路径（storage.ini 场景）；移除仅解除管理，不碰磁盘。
- 核心交互：**身份识别失败时**（未知 schema / 识别歧义）显示原始版本号并要求用户指认 fork，绝不猜。

### 6.2 对比预览（Compare）

- 顶部：源库 → 目标库选择器（方向在此确立，箭头可视化）。
- 数据类型 Tab（谱面集 / 难度 / 收藏 / 皮肤 / 成绩 / 键位 / 规则集设置 / ModPreset）+ 集合运算筛选（默认 Difference=源独有；可切目标独有 / 全部 / 仅冲突 / **仅版本更新**——纯结果筛选，diff 只算一次，ezrealmsync-design §2.2）。
- **差异列表**：内容键、人类可读摘要（曲名/难度名/日期/分数）、差异分类徽标（新增 / 冲突 / **版本更新**（标明识别键：OnlineID 强键 / 元数据弱键「疑似」）/ 将被跳过）、**跳过原因预演**（adapter 规则在预览期即跑：如「Ez 判定语义，目标端会算错 PP」）。
- **「需用户确认」分组**：元数据弱键识别的疑似版本更新（默认不勾选，三选一拍板：替换 / 并存 / 忽略，§5.4）与字段级合并策略可选项集中在此呈现。
- 核心交互：行级勾选（默认全选安全项）；冲突行展开字段级对比；gimmick 谱面行显著告警（§5.4）；底部统计条（将同步 N 条 / 跳过 M 条·k 类原因）。
- 「全部」类型默认不含成绩（对齐 EZRealmSync 的 EntityKindFilter.All 语义，ezrealmsync-design §2.2——成绩量大且常需单独审视）。

### 6.3 同步执行（Sync）

- **执行前检查面板**：守卫状态（游戏进程/锁）、备份目标路径与预估空间、计划摘要（来自预览的勾选结果）、最后确认按钮（需显式点击，不可误触）。
- **执行进度**：批级进度条（总批数 / 当前批 / 批内阶段：files/ 复制字节进度 → 事务写入对象计数 → DriftGuard）；首批前的守卫与备份各是一次性阶段；经 mygo 事件实时推送（M-2）；**可取消，取消点在批间**（已提交批保留，批事务中不可打断，§3.3）。
- **结果报告**：按 §3.3 批口径——已提交批的 applied 计数（按类、按原因分组，原因去重展示——SkipCollector 模式，ezrealmsync-design §2.4）、失败批（整批回滚 + 原因）、未执行批清单、**执行期改判**计数（现场复核与计划不符的项，§3.3）、备份路径（一键打开目录）、校验结论；提供「从失败处重续」入口；失败的分类与用户动作指引（§3.3 四值枚举）。
- **恢复入口**：从备份目录选时间戳还原（还原前自动再备份当前库）。

### 6.4 设置（Settings）

- 备份目录与保留策略；默认冲突方向（源覆盖 / 跳过）与本地行为字段合并策略（§5.4 两项开关）；批大小高级项（§3.3 默认值）；game.ini 同步键白名单管理（默认集 + 用户增删，红灯键硬锁定不可勾选）；进程守卫名单（内置 + 用户补充，应对 delta/g0v0 进程名待定——注意 g0v0 的进程名非常规：程序集名 `g0v0!` **带感叹号**（fork-g0v0 §1.1 `osu.Desktop.csproj` 的 `<AssemblyName>g0v0!</AssemblyName>`），名单与自定义提示须按实际进程名匹配、不可想当然去掉 `!`）；native 库路径与版本显示；日志查看器。
- 核心交互：一切影响写安全 defaults 的项（备份、守卫）不可被完全关闭——备份最多改位置不可禁用（S2）。

---

## 7. 里程碑

每个里程碑给出**可验证完成标准**（命令或行为级）。所有里程碑共用门禁：`go build ./...`、`go test ./...`、`bun run typecheck`、`bun run build` 全绿（package.json scripts 确认：`build:web = tsc --noEmit && vite build`、`typecheck = tsc --noEmit`、`build = mygo build`）。**排期注**：§2 工作量修正（动态 list/embedded/链接/查询面 + 1.5–2 倍余量）适用于 M1–M2 的绑定与写入器施工，里程碑时间估算按此留余，勿以「30–60 函数」核心面直估。

### M1 最小可用：访问层 + 库管理 + 只读对比 + 收藏夹同步

1. **purego PoC 两道验收线全部通过**（§2）：① 只读链路（打开真实 `client.realm` → 枚举 class → 读一行 → 安全关闭、文件头不变）；② **显式 schema 建库链路**（按 schema 镜像 fixture 建空库并写入一行）——任一失败触发 §2 回落序，M1 其余项冻结。
2. realm-core 三平台构建 CI 产出 librealm 并被应用 Dlopen 成功（至少 linux-amd64 实机验证）。
3. 库管理页：自动发现并正确识别四端（含 Ez 的 `client_52011.realm` 与 52011 解码显示、版本来源标注与运行中降级显示 §3.2）；识别不了的库显示原始版本并要求指认。
4. 对比预览页：任意两库、全部类型的**四分类**差异（源独有/目标独有/冲突/版本更新）+ 跳过原因预演；对构造库断言分类正确性，**构造库含谱面版本变更夹具**（同 OnlineID 不同 Hash 的两个集 → 必须归入「版本更新」而非两个孤立条目；元数据弱键对 → 必须归入「需用户确认」组）。
5. **收藏夹单向同步落地**：写路径完整走过「守卫 → 自动备份 → 单批事务写入 → DriftGuard」；对构造库同步后重读投影与预期逐字段相等；目标库结构档案写前后零差异。
6. 真实库手工验收清单：对本机官方 lazer 库同步一个收藏夹 → 官方客户端正常打开且收藏夹出现（开发者手动执行，记录于验收单）。

### M2 核心写路径：曲库 + files/ + 皮肤 + 成绩（基础过滤）

1. 谱面集/难度同步（官方列子集内、非 fork 专有数据）+ files/ 成对搬运：构造库含 N 个合成谱面集，同步后目标库行数/内容键集合与预期一致、files/ 目标侧哈希文件齐全且字节复核通过。**版本更新场景断言（B1 修复的验收）**：构造「目标库有旧版本（挂本地成绩）、源库有同 OnlineID 新版本」夹具 → 同步后目标库恰有一个未软删版本（新 Hash）、旧集 `DeletePending=true`、旧难度上的成绩关系为 null 且 `BeatmapHash` 保留、无孤儿难度行残留（除旧集软删行及其子行外行数符合预期）。
2. 皮肤同步（跳过 Protected 与 fork 专有皮肤）。
3. 成绩同步：BeatmapHash 惰性挂接；构造库覆盖全部跳过分支（无 Score 表 / 空 BeatmapHash / 缺难度 / 规则集不可解析）逐分支断言计数与原因。
4. 护栏全链验证：注入结构漂移 → DriftGuard 触发**整批**回滚；模拟占用（Windows 持独占锁 / Unix 由辅助进程持有 realm 会话使写入会话打开失败，§1.1-S5 语义）→ 写入被拒并提示；备份文件生成且还原可用。
5. **批模型断言（B2 修复的验收）**：构造多批计划 + 中途批失败夹具 → 失败批整批回滚、已提交批保留、报告三段式（已提交/失败/未执行）；重续入口重跑 → 已提交项判「已存在→跳过」、原失败批成功补齐。
6. 真实库手工验收：官方 ↔ delta 双向各同步一批谱面/成绩，两端客户端均正常打开、内容可见、已有本地引用（成绩挂接）不丢；另做一次谱面版本更新同步（源端更新一张谱面再同步）→ 目标端单一新版本、旧成绩仍在（谱面缺失态）。

### M3 差异适配与外围：全 fork 覆盖 + 设置 + 降级

1. Ez 库参与全类型同步：10 个 Ez 列在覆盖写入时原值保留（构造库断言）、7 张技能表/外部托管 set/BMS 规则集连带行全部按规则丢弃且计数告知、StatisticsJson 未知键过滤、mod 白名单拦截（构造含 Ez 判定档/未知键/非官方 mod 的成绩夹具逐条断言）。
2. g0v0 库参与：市场规则集行（OnlineID ≥ 10）过滤；OnlineID 命名空间规则生效（跨服务器方向新行置 -1、更新行不动该列，构造库断言）。
3. 键位（ActionInt 枚举域交集过滤）/规则集设置/ModPreset/game.ini 白名单键同步。
4. delta gimmick 检测告警在预览页呈现（构造含 gimmick section 的 .osu 夹具）。
5. 降级路径：无 native 库时应用可启动并提示；`(.osz/.osk/.osr 生成 + 拉起客户端导入)` 至少对 .osz 跑通。
6. 真实库手工验收：Ez ↔ 官方全类型双向一轮，双方客户端正常打开。

---

## 8. 测试与验收

### 8.1 门（每里程碑全跑）

| 门 | 命令 | 覆盖 |
|----|------|------|
| Go 构建 | `go build ./...` | 全部包编译 |
| Go 测试 | `go test ./...` | 单测 + 构造库集成测试（§8.2） |
| 前端类型 | `bun run typecheck` | `tsc --noEmit`（strict + noUncheckedIndexedAccess） |
| 前端构建 + 打包 | `bun run build` | `mygo build`（含 `build:web = tsc --noEmit && vite build` 与前端 embed） |

补充门（native 相关，M1 起）：purego 绑定层契约测试（`go test ./internal/realmc`）要求在持有 librealm 的环境执行，CI 缺库时跳过并显式标记 SKIP（不得静默绿）。

### 8.2 无真实用户库时的测试库构造方案

原则：**测试库是 LZSync 自产的合成产物，不是用户库**；构造路径可以显式传 schema 建库（对应 EZRealmSync 的 OfficialWrite 工程角色：「官方 schema 镜像与官方库写 Worker，只服务测试」，ezrealmsync-design §1.1），产品路径仍严格钉死动态打开（S3 不因测试而破例）。

1. **schema 镜像 fixture**：每 fork × 关键版本一份结构档案 JSON（官方 51/52、Ez 52011 = 官方 52 + 10 列 + 7 表、delta 51、g0v0 52），内容从调研的字段级摘录生成（C2 白名单、fork-ez2lazer §2.4/§2.5）。三重用途：建库模板、白名单数据源、golden 比对基准（防绑定层写错列名/类型）。
2. **建库**：经 realm-core C API 显式 schema + 版本号新建空库并播种行——**该能力以 M1 PoC 第二道验收线实证为准（§2）**；C API 是否暴露 schema 构造面无法从本仓库调研判定（EZRealmSync 的 `OpenDynamicWithSchema` 只证明 realm-dotnet 层有此能力，ezrealmsync-design §1.2，推不出 realm.h 暴露了它）。**回落方案**（若 PoC 证伪）：测试库改为「手工种子库 fixture」——由开发者在装有对应 fork 客户端的机器上生成小型真实库（导入少量合成内容后关闭客户端），作为二进制 fixture 提交进 LZSync 仓库（仓库自有资产，不违反 C-1——C-1 只禁止依赖 `reference/`），测试在 fixture 副本上运行。
3. **种子数据自洽图**：任意字节充当 `.osu`/`.osr`/音频内容 → SHA-256/MD5 按官方算法定义现场计算（内容寻址不要求「能玩的谱面」，只要求哈希与行一致）；BeatmapSet.Files → RealmFile(hash) → files/<h0>/<h01>/<hash> 落盘；BeatmapInfo.Hash/MD5Hash、Score.BeatmapHash 按 lazer-baseline §6.1 哈希表构造一致关系；内置皮肤/官方四规则集行按固定 Guid/ShortName 播种（lazer-baseline §2.9/§2.10）。
4. **差异夹具**：Ez 库夹具含 JudgeMode≠0 成绩、未知 HitResult 键、非官方 mod、外部托管 set（`ext:set:` hash + 路径哈希 RealmFile 行）、7 张技能表行、Ez 专有皮肤；g0v0 夹具含 OnlineID ≥ 10 规则集行与指向它的成绩、带 PP 的成绩；delta 夹具含 gimmick section 的 .osu。每条对应 adapter 规则的断言用例。
5. **往返测试**：同步写入 → 重新只读投影 → 与预期 DTO 逐字段相等；结构档案写前/写后 `FindDifferences` 为空。
6. **破坏性场景**：DriftGuard 注入（写事务中篡改结构预期）、占用拦截（测试内持有排他锁再发起写入）、备份生成/同名冲突/还原、事务中途强制错误后目标库仍可打开且零变更。
7. **typed parity 的替代**：EZRealmSync 的 typed-vs-dynamic 对照（ezrealmsync-design §5.6）在 Go 无 typed Realm 可用；替代为「golden 结构档案 + 种子行 JSON 快照 + 往返相等」三层断言，另以 M1/M2/M3 的真实库手工验收（客户端能打开、内容可见）作为最终「目标客户端打得开」级验收（ezrealmsync-design §6.1-9 的 DllVerifier 精神；无法进程外自动化，列为人工项）。
8. **真实库可选集成**：本机 lazer 库（`~/.local/share/osu/`，lazer-baseline §1.2 已实证存在）的只读冒烟测试以 build tag（如 `localdb`）保护，默认不在 CI 运行。

### 8.3 待定项汇总（含所需证据）

| # | 待定 | 需要什么证据 | 当前缓解 |
|---|------|------------|---------|
| T1 | g0v0 服务器侧谱面状态语义（任务假设「私服强制 approved」未获证实，fork-comparison U9） | g0v0-server 仓库谱面元数据下发逻辑，或 g0v0 真实库 StatusInt 分布取样 | 不建立在该假设上；StatusInt 原样搬运（§5.5） |
| T2 | delta / g0v0 进程名（U13）。已知线索：g0v0 程序集名为 `g0v0!`（**带感叹号**，fork-g0v0 §1.1）；delta 可执行名未记录 | 目标机运行时进程枚举 | 守卫名单做成用户可扩展（§6.4，含 g0v0! 非常规名提示） |
| T3 | librealm 体积/符号面、mygo 对 sidecar 共享库的分发机制（U14）；**C API 显式 schema 建库能力面**（§2 PoC 第二道验收线的验证对象） | M1 构建实验与 PoC（前置）；建库若证伪 → §8.2 手工种子库 fixture 回落 | P-2 降级模式；产物目录放置 |
| T8 | **file format 版本探测手段**（S5a 配套）：打开前能否不经 realm-core 裸读 `.realm` 文件头的格式版本号、或 C API 有无「禁止自动格式升级」的打开选项 | M1 PoC 附加观察项（realm.h 头文件审读 + 实验）；无公开规范佐证裸读布局（go-realm-options §8-2） | 当前四端同在 v24 无风险面；防线生效场景（未来某端升级 SDK）远后于 M1，届时再补探测 |
| T9 | macos-universal 的 realm-core 交叉构建 CI 成本（构建机/签名/产物体积，§2 工作量修正的配套） | M1 构建实验（linux-amd64 先行，macos 随后评估） | 平台清单保持三端，macos 可推迟到 M2 后而不阻塞 M1/M2 |
| T4 | lazer `.osu` 归一化编码是否随客户端版本变化（U1，影响跨代端 BeatmapInfo.Hash 锚点一致性） | 跨版本客户端导入同一 .osz 的对比实验 | 成绩挂接按 BeatmapHash 现场匹配，失配即跳过计数（不产生坏数据） |
| T5 | Ez 库实际自定义 StatisticsJson 键 / 独有 mod 集合（U7） | 真实 Ez 库取样 | 过滤机制按白名单建好，取样只影响名单内容 |
| T6 | catch 规则集 ShortName `fruits` vs `catch` 调研冲突（U15） | 目标库实际行探测 | 设计规避：不硬编码四规则集短名，运行时以目标库 Ruleset 行探测为准 |
| T7 | DIVA 规则集 ShortName/OnlineID（U6） | 目标机 `rulesets/` 目录验证 | 归入 ForkProfile.OwnRulesets 可配置项，未知规则集默认按「目标端不可解析即跳过」处理 |
