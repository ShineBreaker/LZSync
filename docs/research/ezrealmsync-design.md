# EZRealmSync（Ez2Lazer 官方 lazer↔Ez 同步工具）设计调研

> 调研对象：`reference/EZRealmSync`（C# / .NET 10 / WPF，独立 Windows 程序）。
> 本文自包含：仓库的 `reference/` 目录将来会被整体删除，凡引用源码处均摘录关键事实原文（类名、字段、枚举值、默认值、路径常量），不依赖原文目录也能完整理解。
> 调研方式：通读 `README.md`、`docs/` 全部 5 篇文档、`lib/README.md`，及核心工程 `osu.Game.EzRealmSync` 的同步 / 修复 / 导出 / 收窄源码（约 30 个文件逐行阅读），并核对了守卫测试与发布脚本。所有引文均给出「文件 → 行号」锚点（行号以调研时的快照为准）。

---

## 0. 它是什么

一句话：**单机、单数据目录、双库（A/B）的 Realm 维护工具**。在同一个 osu! 数据目录下管理多份 `client.realm`，对任意两份做按 GUID 的集合运算 Diff，把谱面集 / 难度 / 收藏夹 / 皮肤 / 成绩从一边复制到另一边——**全程不升级 schema、不写 Ez 专用列、不加载 `osu.Game.dll`**；另提供「转回官方版」（把 Ez 库收窄成官方 schema 的新库）、浏览 / 软删、备份还原、`collection.db` / `scores.db` / `.osr` 导出。

版本事实（ROADMAP.md:5-13）：M1（WPF 五 Tab UI）→ M2（数据）→ M3（导出/legacy db）→ M4（**全面动态化**，下线 `osu.Game.dll`）均完成；M2 的 P2.5b「手工验收」未做。项目当前仅支持 Windows 10/11 + .NET 10 Desktop Runtime（README.md:19-28）。

与 LZSync 的关系：它是「1→1（官方↔Ez）专用同步器」，LZSync 要做「N 个 fork 的通用同步体系」。第 6 节专门讨论哪些设计可搬、哪些必须泛化。

---

## 1. 总体架构：分层与「不加载 osu.Game.dll」

### 1.1 工程分层

README.md:117-127 的仓库结构表：

| 工程 | 职责 |
|------|------|
| `osu.EzRealmSync.Desktop` | WPF + WPF-UI 界面（五个页签：导入 / 数据 / 同步 / 修复 / 导出） |
| `osu.EzRealmSync.AppModel` | 界面状态：`RealmAppPresenter`、`EzRealmSyncAppSettings`（settings.json 持久化）、本地化（`Loc` / `Strings.Designer.cs` / `AppLanguage`） |
| `osu.Game.EzRealmSync` | **核心库**：Realm 读写、同步、修复、导出、官方兼容策略（全部业务逻辑） |
| `osu.Game.EzRealmSync.Contracts` | Worker 与宿主共享的 DTO（`RealmReadJob` / `RealmBrowseJob` / `OfficialConvertJob` / `ExceptionFormatting`） |
| `osu.Game.EzRealmSync.OfficialSchema` / `OfficialWrite` | 官方 schema 镜像与官方库写 Worker；**只服务测试**，产品不引用、发布包不带 |
| `osu.Game.EzRealmSync.DllVerifier` | 用真实官方包（`ppy.osu.Game`）在进程外验收产物能否被对应版本客户端打开（`DllOpenCompatibilityTest`）；全仓库唯一引用真官方包的工程 |
| `osu.Game.EzRealmSync.Tests` | 测试；**唯一允许加载 `osu.Game.dll` 的地方**（typed parity 对照） |

核心库内部再分两层：

- **服务层**：`RealmServiceHost`（持有 `RealmServiceSession`，可在 UI-Test Mock 与真实后端之间运行时切换，RealmServiceHost.cs:9-38）暴露四个接口（`Abstractions/`）：`IEzRealmSyncService`（同步）、`IRealmDataService`（浏览/数据）、`IRealmFixService`（修复）、`IRealmExportService`（导出）。真实实现为 `RealmEzRealmSyncService` / `RealmRealmDataService`（partial 类，分 `BrowseExport` / `BrowseMutate` / `ConvertOfficial` / `FixExport` 多文件）等；Mock 实现 `MockEzRealmSyncService` 供 `--ui-test` 使用。
- **引擎层**：`Realm/` 下的动态读写引擎（`DynamicRealmSession`、`DynamicRealmAccess`、`DynamicSchemaReader`、`DynamicValueCodec`、`DynamicRowAccess`）、Diff 引擎（`RealmDiffEngine`）、官方基线读写（`DynamicBaselineReader` / `DynamicBaselineWriter`）、全库搬运（`DynamicRealmCopier`）、官方收窄（`DynamicOfficialConverter`）、schema 快照（`RealmSchemaSnapshotStore`）。

统一入口是 `RealmAccessGateway`（RealmAccessGateway.cs:10-77），静态类，把「怎么打开库」收敛为一组语义明确的方法：

```csharp
public static int? ProbeSchema(string realmFilePath)              // 只读文件头，不开库
public static RealmDiffSnapshot ReadDiffSnapshot(...)             // 同步对比：官方基线列
public static RealmSnapshot ReadBrowseSnapshot(...)               // 数据页浏览
public static DynamicRealmSession OpenDynamicForRead(string, out RealmSchemaSnapshot)
public static DynamicRealmSession OpenDynamicForWrite(string, out RealmSchemaSnapshot)
```

`OpenDynamicForRead/Write` 都会在打开后顺手做两件事：`RealmSchemaSnapshotStore.Default.TryCapture(session)`（碰过哪个版本就落哪个版本的 schema 快照）与 `DynamicSchemaReader.Read(session)`（读出结构供后续判定）；失败即 Dispose 抛出。

### 1.2 为什么读写全走 DynamicRealm 而不用 typed Realm

**根因：typed Realm 打开 = 加载 `osu.Game.dll` = 把工具锁死在某一个游戏版本上。** 具体链条：

1. **版本耦合**。osu!lazer 的 Realm 模型类（`BeatmapSetInfo`、`ScoreInfo` 等）随官方 schema 版本演进，Ez2Lazer 又在其上加扩展列与扩展表。typed 打开要求「DLL 里的模型 = 磁盘 schema」，否则 Realm 报 `SchemaMismatch` 并要求迁移。工具若带 DLL，就只能读它编译时对准的那几个版本，任意版本用户库（尤其比工具新的）直接打不开。
2. **迁移副作用**。typed 打开传 schema 时 realm-core 会执行 `update_schema`，等于被动替用户做 `performSchemaMigration`。DATA-OPERATIONS.zh.md:74-78「禁止」一节把这条列为头号红线：*「1. 对用户库被动 performSchemaMigration: true。2. 在产品进程里加载任何版本的 osu.Game.dll……3. 用『错误版本的工具』当借口拒绝读写：动态路径本就不吃版本号。」*
3. **发布体积**。带 `osu.Game.dll` 意味着带上整套游戏渲染/音频闭包；README.md:28 明确发布包「体积上会裁掉游戏渲染/音频等无关 DLL」。

**DynamicRealm 的机制**（DynamicRealmSession.cs:9-14）：

```csharp
// 打开时不指定 schema 版本：动态打开的版本号是惰性的（realm-core 只在 schema 非空时
// 才调用 update_schema），因此不存在按版本号拒绝或迁移的路径；版本只作为识别/展示信息
// 从已打开的库上读回。
```

`OpenDynamic`（同文件 :39-82）构造的配置是：`IsDynamic = true`、`Schema = Array.Empty<Type>()`、可选 `IsReadOnly`、`FallbackPipePath` 指向工具 `temp/lazer/`（Realm 命名管道目录，模仿 lazer 客户端布局）。打开后用**反射**从 `RealmInstance.SharedRealmHandle`（非公开字段）调 `GetSchemaVersion()` 把磁盘版本号读回来，仅存入 `DiskSchemaVersion` 供显示（`ReadSchemaVersionFromHandle`，:144-157；`isUsableSchemaVersion`：`version > 0 && version <= int.MaxValue`）。

对比之下，唯一的例外是 `OpenDynamicWithSchema`（:90-142）：**显式传 schema 打开**，文件不存在时按该 schema 新建、已存在时做一次 schema 更新（会触发迁移）。它只用于「转回官方版」——因为Realm 拒绝把文件头版本调低（注释原文：`Provided schema version 51 is less than last set 52010`），唯一产出官方号文件的办法是按官方 schema **落新库**。

### 1.3 守卫机制：三层防回流

「产品不加载 osu.Game.dll」不是口头约定，而是三层机器守卫（README.md:126）：

1. **csproj 结构**：产品工程只引 `ez2lazer.Framework`（osu.Framework）+ `Realm`，不引 `ez2lazer.Game`（osu.Game.EzRealmSync.csproj 的 PackageReference 段）。运行时依赖由 `EzRealmSyncRuntimeLibLoader` 解析，预载顺序只有 `{"osu.Framework", "Realm"}`，并强制校验 `realm-wrappers.dll` 存在（EzRealmSyncRuntimeLibLoader.cs:35-39、122-136）。
2. **单元测试守卫**（RealmAccessGatewayTest.cs:89-158）：`Production_services_do_not_reference_typed_osu_game_access` 扫描 4 个产品工程的全部 `.cs`（排除 obj/），命中 typed 标记（`RealmAccess.OpenWithoutMigration`、`OfficialRealmAccess`、`RealmAccessGateway.OpenFor`、`RealmReaderRegistry`）即失败；`Product_projects_do_not_pull_in_the_typed_game_package` 用正则 `PackageReference[^>]*ez2lazer\.Game` 扫 4 个 csproj；另有 `LoadRealmSnapshot_does_not_call_OpenForMutation` 单点盯防。
3. **CI 发布脚本**：`scripts/prune-publish.ps1`（随 publish 自动执行）从发布目录删 `osu.Game.dll` 等无关产物；`scripts/smoke-publish.ps1` 反向断言发布目录**不得**出现 `osu.Game.dll`（`Forbidden file in publish output: ...（产品不得依赖 osu.Game.dll）`），并要求 `osu.Framework.dll` / `Realm.dll` / `realm-wrappers.dll` 必须在，最后用 `--ui-test` 拉起 exe 冒烟 8 秒。

---

## 2. A/B 同步算法

### 2.1 总流程

同步页：选 A、B 两份库 → 选数据类型 Tab（`EntityKindFilter`）→ 选集合运算 → 勾选结果行 → 选操作（添加/删除）与操作目标端（A 或 B）→ 执行。

`RealmEzRealmSyncService.compareCore`（RealmEzRealmSyncService.cs:67-96）：

```
ReadDiffSnapshot(A, kinds) → ReadDiffSnapshot(B, kinds) → RealmDiffEngine.Compare → RealmSetCompareHelper.ApplyOperation(集合运算)
```

### 2.2 两份库怎么比：GUID 为键的集合运算

`RealmDiffEngine.Compare`（RealmDiffEngine.cs:10-82）：把两份快照各按 `(EntityKind, Guid Id)` 建 Dictionary，然后三分类：

- 仅 A（`SourceOnly`）：A 有、B 无；
- 仅 B（`TargetOnly`）：B 有、A 无；
- 不一致（`Conflicted`）：两边同 ID 但关键字段不同，差异摘要由 `RealmDiffConflictRules.GetConflictSummary` 生成。

每类实体比的关键字段（RealmDiffConflictRules.cs:23-98）：

| EntityKind | 比对字段（不同才报「不一致」） |
|---|---|
| BeatmapSet | `Hash`、`OnlineId`、`Title` |
| Beatmap | `Hash`、`Ruleset`、`DifficultyName` |
| Score | `Hash`、`Ruleset`、`Date` |
| BeatmapCollection | `Name`（Title）、谱面数、`CollectionHashFingerprint`（成员 MD5 排序后 `'|'` 拼接的指纹，DynamicBaselineReader.cs:489-493） |
| Skin | `Hash`、`Name` |

集合运算在 `RealmSetCompareHelper.ApplyOperation`（RealmSetCompareHelper.cs:10-25）上做**纯结果筛选**（Intersection 只留 Conflicted；Union 全留；SymmetricDifference 只留 SourceOnly+TargetOnly；Difference 即默认全量 diff）——Diff 本身只算一次。

`EntityKind`（SyncEnums.cs:15-22）：`BeatmapSet, Beatmap, Score, BeatmapCollection, Skin`。注意 `EntityKindFilter.All` 映射到的是 **不含 Score** 的四类（RealmSetCompareHelper.cs:27-35 的 `_ =>` 分支），README.md:47 也写明「类型选『全部』时不含成绩，要比成绩请选『成绩』」。

读取端跳过（不算进 diff）的行（DynamicBaselineReader.cs）：`DeletePending == true` 的 BeatmapSet/Skin/Score（:130、:205、:232）、`Hidden == true` 的难度（:155）、Ez 专有皮肤（`OfficialBaselineSchema.IsEzOnlySkin`，:211）、父集已 `DeletePending` 的难度（:158-160）。

### 2.3 复制什么：官方基线白名单

**字段白名单 = `OfficialBaselineSchema.KnownProperties`**（OfficialBaselineSchema.cs:82-107），一份「类 → 列名集合」的静态表，与官方 schema V51 对齐。全文抄录：

```csharp
public static readonly IReadOnlyDictionary<string, IReadOnlySet<string>> KnownProperties =
{
    ["BeatmapSet"]        = {"ID","OnlineID","DateAdded","DateSubmitted","DateRanked","Beatmaps","Files",
                             "Status","DeletePending","Hash","Protected"},
    ["Beatmap"]           = {"ID","DifficultyName","Ruleset","Difficulty","Metadata","UserSettings","BeatmapSet",
                             "Status","OnlineID","Length","BPM","Hash","StarRating","MD5Hash","OnlineMD5Hash",
                             "LastLocalUpdate","LastOnlineUpdate","Hidden","EndTimeObjectCount","TotalObjectCount",
                             "LastPlayed","BeatDivisor","EditorTimestamp"},
    ["BeatmapMetadata"]   = {"Title","TitleUnicode","Artist","ArtistUnicode","Author","Source","Tags","UserTags",
                             "PreviewTime","AudioFile","BackgroundFile"},
    ["BeatmapDifficulty"] = {"DrainRate","CircleSize","OverallDifficulty","ApproachRate","SliderMultiplier","SliderTickRate"},
    ["BeatmapUserSettings"] = {"Offset"},
    ["RealmUser"]         = {"OnlineID","Username","CountryCode"},
    ["BeatmapCollection"] = {"ID","Name","BeatmapMD5Hashes","LastModified"},
    ["Skin"]              = {"ID","Name","Creator","InstantiationInfo","Hash","Protected","Files","DeletePending"},
    ["File"]              = {"Hash"},                       // 主键即 Hash
    ["RealmNamedFileUsage"] = {"File","Filename"},          // 嵌入类
    ["Ruleset"]           = {"ShortName","OnlineID","Name","InstantiationInfo",
                             "LastAppliedDifficultyVersion","Available"},
    ["Score"]             = {"ID","BeatmapInfo","ClientVersion","BeatmapHash","Ruleset","Files","Hash","DeletePending",
                             "TotalScore","TotalScoreWithoutMods","TotalScoreVersion","LegacyTotalScore",
                             "BackgroundReprocessingFailed","MaxCombo","Accuracy","Date","PP","OnlineID","LegacyOnlineID",
                             "User","Mods","Statistics","MaximumStatistics","Pauses","Rank","Combo","IsLegacyScore"},
};
```

同步涉及的表（`SyncTables`）：`BeatmapSet, Beatmap, BeatmapCollection, Skin, File, Ruleset, Score`。**这份名单如何定**：它是「官方基线」——官方客户端本来就有的列；Ez 加列被显式列在 `EzOnlyPropertyNames`（见 2.5）。白名单的含义是：**源端只读这些列、目标端只写这些列**，两边 schema 版本差异（多出的列）天然被忽略，因此「跨版本可读」。

### 2.4 中间表示与写入：DTO → 删行重建 upsert

执行链（RealmEzRealmSyncService.applyCore，:101-148）：

```
校验请求 → 进程占用检查（ComprehensiveCheckAsync）→ 可选时间戳备份
→ DynamicBaselineReader.ExportByIds(源, ids)   // 产出 RealmSyncApplyBundle（纯 DTO）
→ DynamicBaselineFileCopier.CopyMissing(源, 目标, bundle)  // 先补 files/ 实体
→ DynamicBaselineWriter.Apply(request, bundle, 目标)       // 单事务 upsert
```

DTO（`RealmSyncApplyBundle`，内含 `BeatmapSets / Beatmaps / Collections / Skins / Scores` 五个列表）是源库的**官方基线投影**：例如 `OfficialScoreDto` 携带 `ID, BeatmapHash, RulesetShortName, ClientVersion, Hash, TotalScore(long), TotalScoreWithoutMods(long), TotalScoreVersion(int), LegacyTotalScore(long?), BackgroundReprocessingFailed(bool), MaxCombo(int), Accuracy(double), Date(DateTimeOffset), PP(double?), OnlineID(long), LegacyOnlineID(long), User(RealmUser), ModsJson(string, 来自 "Mods" 列), StatisticsJson, MaximumStatisticsJson, RankInt, Combo, IsLegacyScore, Pauses(List<int>), Files(List<NamedFileDto>), ManiaHitMode/ManiaHealthMode(int, 仅 Ez 源库存在时读出，默认 0)`（DynamicBaselineReader.cs:254-300）。

`DynamicBaselineWriter.Apply`（DynamicBaselineWriter.cs:14-113）要点：

1. 打开目标库**可写动态会话**，写前抓 `schemaBefore`；
2. `officialTarget = RealmSchemaSafety.IsOfficialDiskSchema(session.DiskSchemaVersion)`（磁盘版本 `<1000` 即官方，RealmSchemaSafety.cs:9）——官方目标要叠加官方兼容过滤（见 2.6）；
3. `EzColumnResolver.Resolve(session)` 判定目标库的 Ez 扩展列集合（见 2.5）；
4. **一个显式事务**（`BeginWrite` … `Commit`，注释解释不用 `Realm.Write(闭包)` 只是因为 IDE 的 AccessToDisposedClosure 噪声）内完成全部 upsert；
5. 事务末尾 `RealmSchemaDriftGuard.EnsureUnchanged(schemaBefore, 读回)`——**只填单元格**的硬校验，schema 出现任何差异即抛错整笔回滚（RealmSchemaDriftGuard.cs:5-27）；
6. upsert 语义 = **删行重建**：`Find(类, ID)` 找旧行 → 捕获 Ez 列原值 → `Realm.Remove(旧行)` → `Create(类, ID)` → 逐列 Set → 把捕获的 Ez 列原值写回（`captureEzOnlyValues` / `restoreEzOnlyValues`，:214-255）。

跳过项集中进 `SkipCollector`（:647-668）：计数 + 去重原因列表（最多保留 5 条不同原因），结果 `ApplyResult { AppliedCount, SkippedCount, SkipReasons, BackupPath }`。

### 2.5 Ez 列保留：EzColumnResolver

问题：覆盖同步是删行重建，如果目标库是 Ez 库（有官方没有的扩展列），重建会把这些列抹成默认值。解法（EzColumnResolver.cs:3-11 注释原文）：

> 优先用**同版本的官方 schema 快照**：官方 N 没有的列就是 Ez 加列，权威且不需要手维护名单（`Score.Passed`、`SessionAccuracyCutoffA` 这类就没进过静态名单）。没有快照时回退到内容判据 `RealmSchemaSnapshotClassifier.IsEzOnlyProperty`。目标是官方库时集合为空。

静态名单 `EzOnlyPropertyNames`（OfficialBaselineSchema.cs:33-44）：`XxyStarRating, PerformancePoints, HasVideo, HasStoryboard, HostingKind, ExternalContentRoot, LastAppliedXxySrVersion, ManiaHitMode, ManiaHealthMode`。内容判据（RealmSchemaSnapshotClassifier.cs:58-65）：列名在显式名单里，**或**该类在 `KnownProperties` 里但列名不在已知集合中（兜住 `Score.Passed` 之类的漏网加列）。

写回的值是**同一行删除前的原值**（DATA-OPERATIONS.zh.md:45：覆盖同步不会顺手清掉 xxySR 或外部托管路径；单独同步难度走 `upsertStandaloneBeatmap` 的就地更新，天然保留）。

### 2.6 File 与 files/ 怎么处理

- **Realm 内**：`File` 表主键就是文件哈希（`File.Hash`）。`linkFiles`（DynamicBaselineWriter.cs:615-638）对每个 `(Hash, Filename)` 用法：`Find(File, hash) ?? Create(File, hash)`（**不重复建行，跨谱面共享 blob 行**），然后在宿主的 `Files` 列表（`RealmNamedFileUsage` 嵌入对象）里追加 `File=链接, Filename=原名`。
- **磁盘上**：`DynamicBaselineFileCopier.CopyMissing`（DynamicBaselineFileCopier.cs:9-42）按 bundle 里出现的全部哈希（谱面集 Files+Hash、难度 Hash、皮肤 Files、成绩 Files），从**源工作区 files/ → 目标工作区 files/** 逐个 `File.Copy(overwrite:false)`；源/目标是同一数据根（同一 files/）时直接 no-op；目标已存在该哈希也跳过。文件物理路径由 `RealmFilePathHelper.GetFullPath(filesDir, hash)` 计算（Realm 的两级哈希目录布局）。
- **files/ 目录解析**（RealmWorkspacePaths.cs:37-85）：osu 存储布局是共享 `{storage}/files/`，Realm 可在根目录（Ez2Lazer）或 `{storage}/data/`（官方 lazer）。解析顺序：realm 文件所在目录下的 `files/` → 若所在目录叫 `data` 则取其兄弟 `files/` → 父目录的 `files/`。`ResolveStorageRoot`：realm 路径的目录名是 `data` 时取上一级。
- 校验期还有一个软警告（RealmEzRealmSyncService.ValidatePathsAsync，:44-45）：两工作区 files/ 路径不一致时 warning「同步后目标端可能仍缺少实体文件」（旧 API 路径才检查；新 A/B 路径直接靠 FileCopier 补齐）。

### 2.7 成绩如何挂到目标难度：BeatmapHash

官方契约：本地成绩与谱面生命周期解耦，`Score` 靠 `BeatmapHash` 链接 `BeatmapInfo`。写入端（DynamicBaselineWriter.cs:466-538）按顺序判定，任一失败即**跳过并计数**（绝不写悬空成绩）：

1. 目标没有 `Score` 表 → 跳过「目标库没有 Score 表（版本过旧）」；
2. `dto.BeatmapHash` 为空 → 跳过「成绩没有谱写面 Hash」；
3. **目标库 BeatmapHash 索引查不到** → 跳过「目标库缺少该成绩对应的谱面（Hash xxxxxxxx）」。索引 `buildBeatmapHashIndex`（:543-561）扫目标全部非隐藏难度按 `Hash` 建 Dictionary，`TryAdd` 同哈希只留首行；**索引在写入循环内惰性构建（`??=`）**，且建在谱面集/难度写入之后，因此「本次刚同步进来的难度」也能被挂上（:80-86 注释原文）；
4. 规则集解析失败 → 跳过。`tryResolveScoreRuleset`（:567-579）：目标已有同名 `Ruleset` 行则复用；没有且 ShortName 属 Ez 专用（diva/bms）则返回 null（**不凭空插 Ez 规则集行**）；非 Ez 专用则 `resolveRuleset` 新建（Name=shortName, InstantiationInfo="", Available=true）；
5. 官方目标再叠加 `isOfficialScore`（:317-338）：规则集必须官方四模式；`ManiaHitMode > 0 || ManiaHealthMode > 0` 判为「Ez 判定语义」跳过（官方重算会算错）；mods JSON 里每个 acronym 必须全在官方名录（`OfficialModCatalog`），否则跳过。

通过后建行：`BeatmapInfo` 列直接链到查到的难度对象，`BeatmapHash` 列同时存字符串，其余列逐项 Set（见 2.4 DTO 字段表），`Pauses` 列表逐项 Append，`Files` 走 linkFiles（回放 `.osr` 文件随之复制）。

### 2.8 其他跳过/边界情况汇总

| 情况 | 行为 |
|---|---|
| 官方目标 + Ez 专用规则集难度 | 跳过 + 原因（:56-60） |
| 官方目标 + 外部托管谱面集（`ExternallyHosted`，即源库 `HostingKind==1`） | 跳过「内容位于 Ez 外部目录，官方客户端读不到」（shouldExportSet :296-314） |
| 官方目标 + 谱面集只含 Ez 规则集难度 | 跳过「官方客户端打不开」（同上） |
| 单独同步难度、目标缺父谱面集 | **抛异常**（不是跳过）：`目标库中不存在难度 X 所属的谱面集，请先同步谱面集`（:281-285） |
| 目标已有同 ID 行 | 删行重建 + Ez 列原值回写（2.5） |
| Ez → Ez 目标 | **不过滤**：Ez 规则集成绩、Ez 判定、外部托管谱面集照常写入（DATA-OPERATIONS.zh.md:38） |
| 收藏夹 | 整行重建：`Name`、`LastModified`、清空重填 `BeatmapMD5Hashes` 列表；无 Ez 列回写（表无 Ez 列） |
| 皮肤 | Ez 专有皮肤（IsEzOnlySkin）读写两端都过滤 |

---

## 3. 「不改 schema」原则的实现

### 3.1 如何读任意版本库

三个关键点：

1. **打开不传 schema**（见 1.2）→ realm-core 不调 `update_schema` → 不迁移；`IsDynamic` 会话按磁盘上实际存在的表/列暴露 `DynamicApi`。
2. **按列存在性工作，不按版本号选路**。所有读取/写入前都 `session.HasClass(className)` / `HasProperty(className, propertyName)` 检查（DynamicRealmSession.cs:162-170）；缺表缺列自然跳过。DATA-OPERATIONS.zh.md:19-21：「打开一律**钉死磁盘 schema**：不迁移、不改文件头版本号。不因 schema 高于/低于 bundled 版本拒绝用户库；版本号只用于**显示**与**选官方 schema 目标**。」
3. **版本号探测不依赖打开成功**：`RealmDiskSchemaReader.TryReadSchemaVersion` 先动态只读打开读 handle 版本；失败则退化为按文件名推断（正则 `^client_(?<n>\d+)\.realm$`，RealmDiskSchemaReader.cs:108）；占用/缺 DLL 等失败会被格式化成用户可读信息（如「Realm 文件正被占用，请先关闭 osu!/Ez2Lazer」）。

### 3.2 为什么不写 schema 版本号

- 版本号写在文件头，由 realm-core 在**带 schema 打开**时维护。动态打开（空 schema）下工具根本没有机会、也没有 API 去改它——「不写版本号」是「不传 schema」这一决定的自然结果，而非额外工作。
- 主动不写的原因：**版本号是客户端迁移的输入**。工具若擅自升降版本号，下一个打开它的客户端会按错误的迁移路径走（例如把 Ez 号写到官方库，官方客户端立刻 SchemaMismatch 拒开）。ROADMAP.md:44（P4.6）：「下线『升级 Realm 文件』」；README.md:61-67 明确：「本工具**不会**改动 Realm 文件的版本号，也不会代替游戏做 schema 迁移。要迁移请用 Ez2Lazer 客户端打开该数据目录一次，让游戏自己把库迁到当前版本。」
- 写入侧的兜底是 **RealmSchemaDriftGuard**：动态写入不提供 schema，Realm 结构上不可能加类加列，因此写前写后快照必须逐类逐列一字不差；不一致=本工具 bug，整笔回滚并报「出现此情况请报告」（RealmSchemaDriftGuard.cs:5-27，EnsureUnchanged 在 DynamicBaselineWriter.Apply / SoftDelete / RealmIllegalCharacterFixer.Apply 等所有写路径的事务末尾调用）。

### 3.3 跨版本可读的边界

- **能读**：任何能被动态打开的库（列缺失只导致该列数据不进 DTO）。测试 `RealmAccessGatewayTest.ReadDiffSnapshot_reads_legacy_sample` 用旧版样本库验证；`OpenDynamic_reads_without_touching_the_disk_header` 验证只读打开不改文件头版本号。
- **能写**：只写「目标库已有」的白名单列。目标版本比白名单老（某列尚未在官方引入）时，`DynamicRealmAccess.Set` 对不存在列的行为由 HasClass/列检查兜住——白名单是「上限」，实际写集 = 白名单 ∩ 目标 schema。EzColumnResolver 保证反向（Ez 列）不被触碰。
- **不能做**：就地收窄 schema（Realm 禁止版本号调低，DynamicRealmSession.cs:84-89 注释）；「版本过旧」的库（连官方基线核心列都缺，如无 Score 表）→ 对应操作跳过并在结果计数，让用户先用 Ez2Lazer 客户端打开一次完成官方迁移（DATA-OPERATIONS.zh.md:44、49）。
- **`SchemaModelMismatch` 的收窄含义**（P4.6）：现在只剩一种——「转回官方版」时工具 `snapshots/` 里没有源库 upstream 对应的官方 schema 快照、且用户没指定官方库。解法是给一份同版本官方 `client.realm`（DATA-OPERATIONS.zh.md:48）。

版本号编码（RealmSchemaVersions.cs:5-29）：Ez 修订号 = `official * 1000 + ez`（例 51006 = 官方 51 + Ez 6）；官方库磁盘版本是裸 upstream（51/52）。`Decode`：`<1000 → (value, 0)`，否则 `(value/1000, value%1000)`。工具只把它当**显示信息**与「选官方 schema 目标」的键。

---

## 4. 官方收窄（「转回官方版」）

### 4.1 schema 快照机制

`RealmSchemaSnapshotStore`（RealmSchemaSnapshotStore.cs:14-266）：

- 目录：exe 同目录 `snapshots/`，文件名 = `{diskSchemaVersion}.schema.json`（`FileSuffix = ".schema.json"`，`FilePathFor(version)`）。
- **采集时机**：访问任何库时「顺手落盘」——`TryCapture(session)` 在 `DynamicBaselineReader.ReadDiffSnapshot`、`DynamicBaselineWriter.Apply/SoftDelete`、`RealmAccessGateway.OpenDynamicForRead/Write` 等每个打开点调用；**同一版本只留第一份**（已存在即不动，除非 `overwrite`）；采集失败只记 WARN，绝不影响正在做的读写（:45-62）。
- **内容**（SnapshotDto，:210-264）：`diskSchemaVersion`、`sourcePath`、`collectedAtUtc`、`classes[]`；每个类 `name/embedded/primaryKey/properties[]`；每个属性 `name`、`type`（PropertyType 原始 flags int——**权威字段**，枚举名有位重合歧义）、`typeText`（人读形式，加载时忽略）、`objectType`（链接目标类名）、`linkOrigin`、`primaryKey`、`index`。写入走「临时文件 + Move」防半截 JSON；坏 JSON 当「没有这份快照」处理（:91-96）。
- **结构等价物**：`RealmSchemaSnapshot`（RealmSchemaSnapshot.cs:11-89）内存表示，带 `Signature`（全库稳定指纹，各类 Signature 拼接）与 `FindDifferences(other)`（双向逐类逐列差异清单）。

**谁能当「官方 N 的事实来源」**：`RealmSchemaSnapshotClassifier.TryGetOfficialUpstream`（RealmSchemaSnapshotClassifier.cs:17-32）两道判据：① 版本解码 `E == 0`（纯官方号）；② 快照无 Ez 特征（`HasEzFingerprint`：类名以 `Ez` 开头，或任一列命中 `IsEzOnlyProperty`）。注释明确判据刻意偏向「不是官方」：误把 Ez 库当官方基线会污染写集，代价远大于反向误判（后者只退化为保守白名单）——**官方版本比工具已知更新时靠这条偏向保安全**。

**为什么不手写官方列清单**（OfficialSchemaSource.cs:5-11 注释原文）：「官方每个 upstream 增删的表与列必须逐列命中——少一列，官方客户端打开时要做一次迁移；多一列，官方客户端根本不认。快照来自真实库，因此天然逐列对齐，且版本比本工具已知的更新时同样成立（碰过哪个版本就有哪个版本的快照）。」

快照来源优先级（`OfficialSchemaSourceResolver`，同文件 :15-61）：① 工具已采集的快照（`TryFromSnapshots`）；② 用户指定一份官方库当场显式 `Capture`（失败要如实报错，这条路径不允许静默），并强制校验「它必须是官方库（过 Classifier）」且「upstream 必须等于源 Ez 库解出的 upstream」——官方 51 的 schema 拿去收窄 52 的库会丢 52 才有的表列，不算转换成功。

### 4.2 收窄迁移算法

入口 `RealmRealmDataService.ConvertToOfficialRealmAsync`（RealmRealmDataService.ConvertOfficial.cs:16-90）流程：

```
校验（registry 找到文件 → 读磁盘版本 → 必须 EzExtended → Decode 出 upstream → 解析 OfficialSchemaSource）
→ RealmProcessGuard.ComprehensiveCheckAsync（进程 + 排他锁）
→ 自动备份（备份目录或「同目录 + _ezbackup_时间戳 后缀」，无配置也必须留原件——覆盖不可逆）
→ temp/official-convert/{guid}/ 下建临时目标
→ DynamicOfficialConverter.Convert(源, official, 临时目标)
→ File.Move(临时, 源, overwrite:true)   // 覆盖是最后一步：前面任何失败原文件一字未动
→ RealmFileWriteStamp.MarkWritten + 失效缓存
```

`DynamicOfficialConverter.Convert`（DynamicOfficialConverter.cs:28-85）：

1. `collectDropped`：比对源 schema 与官方快照，**丢弃清单**——官方没有的类记入 `DroppedClasses`，公共类上官方没有的列记入 `DroppedColumns`（`Class.Property` 格式，LinkingObjects 列除外，:109-129）。
2. `OfficialRealmCopyFilter.BuildIndexes()`：只读源库一遍，预计算判定所需事实（见 4.3）。
3. 按过滤器数出每个公共类「应搬行数」`sourceCounts`（供自检）。
4. `OpenDynamicWithSchema(临时目标, RealmSchemaDefinitionFactory.Create(官方快照), upstream, readOnly:false)`——**按官方 schema 新建库**（这就是 1.2 里唯一的显式 schema 打开）。`RealmSchemaDefinitionFactory` 把快照转回 RealmSchema 对象。
5. `DynamicRealmCopier.Copy(source, target, filter)`（见下）。
6. `verify`（:135-162）**产物自检**，三查：产物磁盘版本必须 == upstream；产物 schema 与官方快照 `FindDifferences` 必须为空；每个公共类行数必须 == sourceCounts。任一不符抛 `SchemaModelMismatch`，调用方保留原文件。注释（:131-134）说明动机：「搬运是逐列反射调用，静默漏一列也能『跑完』，所以必须回读磁盘上的产物」。

`DynamicRealmCopier.Copy`（DynamicRealmCopier.cs:29-58，两遍法）：

- **第一遍 buildSkeleton**：按**目标**（官方）schema 的类顺序，对每个非嵌入类，枚举源库同名类全部行 → 过滤器 `ShouldCopy` 拒掉的记 `SkippedPerClass` → 按主键值建目标行（主键值用 `DynamicValueCodec.Read` 读源，支持 Guid/string/long 三种主键；无主键类裸 Create）。维护 `targetBySource: Dictionary<IRealmObjectBase, IRealmObjectBase>`——**按行身份记**（Realm 包装对象 Equals/GetHashCode 即行身份），同一条共享元数据行不会被复制成多行（:69-73 注释）。
- **第二遍 fill**：逐行逐列搬格子。规则（类注释 :17-23）：嵌入类不单独建行，在宿主列上 `CreateEmbeddedObjectForProperty`（一对一）或 `AddEmbeddedObjectToList`（一对多）并递归 fill；`LinkingObjects` 反向链接列不搬（目标库自己算）；**目标有、源没有的列留默认值**（记 note）；**源有、目标没有的列（Ez 列、目标版本未引入的列）不搬**（记 note，去重）；单对象链接/集合链接先经 `targetBySource` 映射成目标行再写（RealmValue 里指向源库的链接直接写会在提交时炸）；映射不到的链接**留空并记 note，绝不新建「没人引用的行」**（:283-287）。每列搬运异常会被补上「哪一库哪一列、源类型→目标类型」的上下文再抛（反射链会把真实异常包进 TargetInvocationException，:210-217、338-342）。

结果 `DynamicCopyResult(int Rows, IReadOnlyDictionary<string,int> RowsPerClass, IReadOnlyDictionary<string,int> SkippedPerClass, IReadOnlyList<string> Notes)`。

### 4.3 行过滤：OfficialRealmCopyFilter

`OfficialRealmCopyFilter : IDynamicCopyFilter`（OfficialRealmCopyFilter.cs:15-178）。判定规则集中在纯数据类 `OfficialExportPolicy`，Filter 负责把跨界事实在源库上**先算清楚**（注释 :9-11：「搬运按目标 schema 的类顺序走，而谱面集要等它的难度、难度要等它所属的集、成绩要等难度是否留下——就地现查会读到一半的事实。索引只读源库一遍，之后判定是纯内存」）。

`BuildIndexes` 四步：`readRulesets`（ShortName+InstantiationInfo → 是否官方）→ `readBeatmaps`（每难度记 `BeatmapFacts(Hidden, RulesetIsOfficial, SetId, Hash, Metadata)`）→ `readBeatmapSets`（每集：`!DeletePending && IsOfficialBeatmapSet(HostingKind, 难度构成)`）→ `finalizeBeatmaps`（难度留下要看父集：`isKept` 通过的收集 `keptBeatmapHashes` 与 `keptMetadata`）。

`ShouldCopy` 分派：

| 类 | 判定 |
|---|---|
| Ruleset | `rulesetIsOfficial[ShortName]` |
| Beatmap | `isKept(facts)`（父集被拒则拒） |
| BeatmapSet | `beatmapSetIsOfficial[ID]` |
| Score | `!DeletePending && IsOfficialScore(...) && keptBeatmapHashes.Contains(BeatmapHash)`（难度没搬过去的成绩搬过去就是悬空行） |
| BeatmapMetadata | `keptMetadata.Contains(row)`（元数据跟着难度走，免得起草一堆没人引用的元数据） |
| 其他 | `true` |

`OfficialExportPolicy`（OfficialExportPolicy.cs:16-158）判定口径（类注释：「**能少不能错**：官方还原不了的成绩/谱面宁可留在这边，也不写过去变成官方端看不见或语义错误的行」）：

- **规则集**：`IsOfficialRuleset(shortName, instantiationInfo)`——Ez 专用（`ez_only_rulesets = {"diva","bms"}`）拒绝；InstantiationInfo 提到 `Rulesets.Diva` / `Rulesets.BMS` 拒绝；`official_rulesets = {"osu","taiko","catch","mania"}` 放行；短名不在四模式里但 InstantiationInfo 含 `osu.Game.Rulesets.` 的也放行（目标库已有的官方可实例化行不误杀）。**默认拒绝**：将来 Ez 再加私有规则集不用改这里。
- **谱面集**：`IsOfficialBeatmapSet(hostingKind, beatmaps)`——`hostingKind == 1`（`BeatmapSetHostingKind.External`，内容在 Ez 外部目录，官方只认 files/）拒绝；否则须至少一条「非隐藏且规则集官方」的难度。
- **难度**：`IsOfficialBeatmap(hidden, rulesetIsOfficial, parentSetIsOfficial)` = 三者全真。
- **成绩**：`IsOfficialScore(rulesetShortName, maniaHitMode, maniaHealthMode, modsJson)`——官方规则集 + `isNonLazerMode(maniaHitMode) || isNonLazerMode(maniaHealthMode)` 为假（`mode > 0` 即非 Lazer；负数只在 Ez v1 迁移前的老成绩上出现，客户端按 Lazer 读，不算非官方）+ mods 全官方。
- **mods 名录**（areModsOfficial :87-99）：把 Score 的 `Mods` 列（APIMod JSON 数组）解析出全部 `acronym`（属性名忽略大小写匹配——客户端存的是小写 `acronym`，:140-148 注释）；**解析不出来直接判不官方**（「宁可不过滤错也要拦下」）；每个 acronym 查 `OfficialModCatalog.IsOfficial(ruleset, acronym)`——共享集（`osu.Game/Rulesets/Mods` 下的跨规则集 mod）或该规则集专属集。比对 `StringComparer.Ordinal` **区分大小写**（官方解析 APIMod 时也区分）。
- `OfficialModList.json`（嵌入资源，86 行）：`{"$comment": "...由 scripts/Generate-OfficialModList.ps1 从 osu 仓库生成，请勿手改", "generatedFrom": "osu @ eb271d3776", "shared": [AC,AS,AT,BR,CL,CN,DA,DC,DT,EZ,FL,HD,HR,HT,MR,MU,NC,NF,NM,NS,PF,RD,RX,SD,SV2,SY,TD,WD,WU], "osu": [...20 个], "mania": [10K,1K..9K,CO,CS,DS,FI,HO,IN,NR], "taiko": [CS,SG,SR,SW], "catch": [FF,MF]}`。生成脚本从官方 mod 源码抽取；Ez 侧黑名单方案被否决的理由（OfficialModCatalog.cs:9-11）：「靠 Ez 侧硬编码黑名单会误杀同名官方 mod（Ez mania 的 DS/DP/GR/RP 在官方同名 mod 上完全合法），所以只放行『官方确有此缩写』的组合」——**白名单而非黑名单**。

### 4.4 丢弃项如何计数与告知用户

三层输出：

1. **结构层**：`OfficialConvertStats(UpstreamVersion, RowsWritten, DroppedClasses, DroppedColumns, Notes)`——被丢的 Ez 表名列表、Ez 列名列表（`Class.Property`）。
2. **行层**：`SkippedPerClass` 按类计数，`summariseSkipped`（DynamicOfficialConverter.cs:93-107）翻译成人话：「官方端不认识的规则集 N 条（Ez 专用规则集）未搬运」「外部托管 / 只含 Ez 规则集难度的谱面集 N 个未搬运」「Ez 规则集或已隐藏的难度 N 条未搬运」「官方无法还原的成绩 N 条未搬运（Ez 判定语义 / 非官方 mod / 难度未搬运）」「未被搬运难度引用的元数据 N 条未搬运」。
3. **UI 层**：`RealmOfficialConversionResult { TargetRealmFilePath, SourceSchemaVersion, TargetSchemaVersion, AppliedCount, BackupPath, SchemaSourceDescription, DroppedClasses, DroppedColumns }` 完整带回；README.md:104 概括为「都会被丢掉，并在结果里计数说明」。核心承诺：**被丢掉的每一条都计数并给出原因，不静默吞掉**（DATA-OPERATIONS.zh.md:39）；丢掉的行**只留在备份里**（原文件先备份），README.md:75「需要留着就先导出一份或先做 A/B 同步」。

同一套 `OfficialExportPolicy` 判定被**两条链路复用**（DATA-OPERATIONS.zh.md:37）：「转回官方版」（Filter 决定 Copier 搬哪些行）与「成绩/谱面集互导到官方库」（`DynamicBaselineWriter` 写前过滤，见 2.7）。

---

## 5. 工程细节

### 5.1 软删除（DeletePending）

- **数据页删除**（`RealmBrowseEntityMutator.Delete` + `DynamicBaselineWriter.SoftDelete`）：谱面集 / 皮肤 / 成绩置 `DeletePending = true`；**收藏夹没有软删语义，整行 `realm.Remove(row)`**——与官方一致（RealmBrowseEntityMutator.cs:3-7 注释）。库上没有 DeletePending 列（更老的官方 schema）时**按跳过处理并返回实际改动行数，不假装删掉了**（:17-19 注释）。
- **生效时机**：本工具只留标记，实际清理由 lazer 客户端下一次打开库时的 `cleanupPendingDeletions` 完成（DATA-OPERATIONS.zh.md:46）。
- 软删行在所有读取路径（diff、导出 bundle、收窄过滤）都被当作不存在。

### 5.2 备份策略

- `RealmFileBackup.CreateTimestampedCopy`（RealmFileBackup.cs:10-34）：`{原名}_{yyyyMMdd_HHmmss}.realm` 拷贝进备份目录；**已存在同名备份即抛错**（不覆盖备份），不修改源文件。
- `RestoreOverTarget`（:39-66）：还原前校验两个文件都可访问、路径不得相同、可选先给当前目标做一份「安全备份」再覆盖。
- 默认目录 exe 旁 `backups/`（EzRealmSyncDataPaths.BackupsDirectory）；`RealmBackupCatalog` 扫描目录内 `*.realm`（仅顶层），备份 ID = 全路径的 SHA256 hex（`CreateEntryId`），从文件名时间戳反推原始文件名（正则 `^(.+)_\d{8}_\d{6}\.realm$`）。
- 同步默认 `CreateBackup = true`（ApplyRequest.cs:14）；「转回官方版」**强制**先备份，未配置备份目录时退化为同目录 `_ezbackup_时间戳` 副本（ConvertOfficial.cs:92-109，注释：「用户明确要求『没设置备份文件夹时也要留一份原件』，而覆盖是不可逆的，所以这条退路不能省」）。

### 5.3 日志

`EzRealmSyncLog`（EzRealmSyncLog.cs:7-72）：极简同步文件日志，`log/EzRealmSync_{yyyyMMdd}.log`，行格式 `{yyyy-MM-dd HH:mm:ss.fff} [{LEVEL}] {message}`；初始化时记录 ApplicationRoot、exe/base 路径、OS/RID/架构/运行时、culture/ANSI/OEM 代码页（排查乱码问题用）；**日志写入失败静默吞掉**（不得影响主流程）。`RealmReadLog`（RealmReadLog.cs:10-40）在每次打开/浏览时写「现场」：文件大小、头字节、schema 版本、类清单（前 40 个）、行数、疑似乱码文本样本（≤8 个样本、每个 48 字符、最多检查 2500 个单元格）——为的是用户报「读不出」时可排查。

### 5.4 settings.json 与运行时布局

`EzRealmSyncDataPaths`（EzRealmSyncDataPaths.cs:7-92）——全部数据在 **exe 同目录**（`ResolveHostApplicationRoot` 对 `official-write/` Worker 子目录特判取上级）：

| 路径 | 常量 |
|---|---|
| 设置 | `settings.json`（优先 exe 旁，其次 `%LocalAppData%\EzRealmSync\settings.json`，均无则写 exe 旁） |
| 备份 | `backups/` |
| 导出 | `exports/` |
| schema 快照 | `snapshots/`（文件名=磁盘版本号） |
| 日志 | `log/EzRealmSync_{yyyyMMdd}.log` |
| 临时 | `temp/`，Realm 管道目录 `temp/lazer/`，收窄临时 `temp/official-convert/{guid}/`（用后整目录删除） |

`EzRealmSyncAppSettings`（EzRealmSyncAppSettings.cs:7-42）字段：`SearchDirectory`（导入页扫描根，扫根目录与 `data/` 子目录的 `*.realm`）、五个页签各自选中的 RealmId（`ImportSelectedRealmId/DataRealmId/SyncRealmIdA/SyncRealmIdB/FixRealmId/ExportRealmId`）、`BackupDirectory`、`ExportDirectory`、`ExportFolderName`、`ExportGroupScoresByPlayer`（默认 true，批量导出成绩按玩家名建子目录）、`IllegalCharacterReplacement`（默认 `"_"`）、`ConfirmBeforeDelete`（默认 true）、`UiTestMode`、`DarkTheme`（默认 true）。读写失败一律回默认值（Load 的 catch）。

### 5.5 并发 / 占用检测（要求先退出游戏）

`RealmProcessGuard`（RealmProcessGuard.cs:9-104），双保险：

1. **进程名检测**：`process_names = { "osu!", "osu", "Ez2Lazer" }` 的 `Process.GetProcessesByName`；`RetryCheckAsync` 重试 3 次、间隔 500ms（降低「游戏正在退出中」竞态窗口）。
2. **排他文件锁探测**：`TryAcquireExclusiveFileLock` 以 `FileShare.None` 打开 realm 文件再立刻释放，`IOException` → 被占用、`UnauthorizedAccessException` → 无权限。

`ComprehensiveCheckAsync` = 进程重试 + （给了路径时）排他锁。写入前必查：同步 applyCore、转官方 convertToOfficialCore 都在动手前调用，失败抛 `RealmUserOperationException(RealmUserErrorKind.FileInUse, …)`。用户错误枚举 `RealmUserErrorKind { FileInUse, PathConflict, SchemaModelMismatch }`（RealmUserOperationException.cs:3-8）。README.md:13 首行警示：「使用前请先完全退出 osu! / Ez2Lazer。打开中的 Realm 无法安全写入。」

### 5.6 其他值得记的细节

- **`RealmFileWriteStamp.MarkWritten`**（RealmFileWriteStamp.cs:5-35）：每次成功提交后把目标文件 `LastWriteTimeUtc` 置为当前时间。原因（注释原文）：「Realm 通过 mmap 写页，Windows 不会因为映射写入而更新 LastWriteTime，提交又常常不扩展文件长度……用户会以为根本没写进去」。失败只记 WARN。
- **修复页**：`RealmIllegalCharacterFixer` 只碰官方基线列（元数据 6 个文本列 `Title/TitleUnicode/Artist/ArtistUnicode/Source/Tags` + `Score.BeatmapHash`），替换字符可配（默认 `_`），写前重读当前值防「诊断时的值≠落库时的值」，事务末尾同样过 DriftGuard；`RealmOrphanFileScanner` 只认 `File.Hash` 一列做 files/ 缺失/僵尸扫描——官方库与 Ez 库同一条路径。非法字符修复仅对 Ez 库开放（DATA-OPERATIONS.zh.md:47）。
- **测试体系**（Tests 目录 47 个文件）：typed-vs-dynamic 逐单元格 parity（`DynamicBrowseParityTest` / `DynamicDumpParityTest` / `DynamicExportFixParityTest` / `OfficialMirrorSchemaParityTest`）、官方 DLL 打开验收（`DllOpenCompatibilityTest`，经 DllVerifier 进程外用真官方包打开产物）、Ez 列保留（`DynamicBaselineEzColumnTest`）、官方兼容过滤（`OfficialExportPolicyTest` / `OfficialRealmCopyFilterTest` / `OfficialModCatalogTest`）、快照与漂移守卫、备份/目录、Mock 同步、设置持久化。
- **文档与代码的一处不一致**：`docs/LEGACY-DB-EXPORT.zh.md:33` 链接了 `../osu.Game.EzRealmSync/Realm/RealmScoresDbSync.cs`，该文件实际不存在（已核实 `ls` 报错）；scores.db 导出的真实实现是 `Realm/Dynamic/DynamicScoresDbExporter.cs`（364 行）。属文档滞后，不影响结论。

---

## 6. 对 LZSync 的启示

EZRealmSync 是「单目录、双库、官方↔Ez 一对 fork」的专用工具；LZSync 的目标是「N 个 fork 的通用同步体系」。逐项评估：

### 6.1 可直接借鉴（设计已验证、与 fork 数量无关）

1. **DynamicRealm 全动态读写 + 「不加载游戏 DLL」**。这是整个项目最重要的决策：工具寿命不再被任何单一 fork 的版本节奏绑架。对 LZSync 尤其关键——N 个 fork 意味着 N 套模型 DLL，typed 路线根本不可行。三层守卫（csproj 结构 + 测试扫源码/工程文件 + 发布脚本反向断言）值得整套照搬，把「不加载任何游戏 DLL」变成机器保证而非约定。
2. **「不改 schema」纪律**：打开不传 schema → 版本号惰性、无迁移路径；只写「目标已有」的列；写前写后 schema 快照 DriftGuard 硬校验，任何漂移整笔回滚。LZSync 在 fork 库之间复制数据时应沿用同一纪律——同步工具**永远不该**是迁移工具，版本迁移让各 fork 自己的客户端完成。
3. **schema 快照仓库**（`snapshots/{version}.schema.json`，顺手采集、同版本只留首份、坏文件按不存在处理）+ 「碰过哪个版本就有哪个版本的快照」策略。对 LZSync 可泛化为「按 (fork, schemaVersion) 采集的结构档案」，是解决「目标 fork 的基线列集合从哪来」的同一答案：**从真实库采，不手写清单**（手写清单在每个 upstream 增删列时都会烂）。
4. **「Ez 列」概念的泛化——差异列保护**：覆盖写入（删行重建）前捕获「目标有、基线没有」的列原值、重建后原值写回。LZSync 里这就是「fork 专有列在同步后幸存」的机制；判定优先级（同版本基线快照 diff > 静态名单 > 已知列集合补集）直接可用。
5. **成绩按 `BeatmapHash` 挂目标难度 + 惰性建索引**（索引在谱面写入之后 `??=` 构建，覆盖本次刚写入的难度）+ 「缺目标难度/规则集就跳过并计数，绝不写悬空行」。任何 fork 组合间同步成绩都用得上。
6. **「能少不能错」的目标端兼容过滤 + 白名单 mod 名录**：向低兼容目标（官方/旧 fork）同步时，宁可跳过也不写「目标端看不见或语义错误」的行；判定规则做成纯数据类（不碰 DB），供多条链路复用；mod/规则集兼容性用「目标确有」白名单（数据文件从目标 fork 源码生成）而非黑名单，避免误杀同名合法项。
7. **File 表按哈希去重共享 + files/ 目录解析规则**（根目录 `files/`、`data/` 兄弟 `files/`、父目录 `files/` 三级回退）与「同数据根 no-op、目标已有哈希跳过」的 CopyMissing。跨 fork 的 blob 目录布局差异正需要这个。
8. **工程化护栏**：写前 `ComprehensiveCheckAsync`（进程名 + 排他锁双重探测、重试缩竞态）、写入前时间戳备份（未配置目录也要留副本——覆盖不可逆）、覆盖永远是最后一步（先建临时产物、自检通过再 Move）、RealmFileWriteStamp（mmap 写入不更新 mtime 的坑）、SkipCollector（计数 + 去重原因上限 5 条）、把用户可解的错误收敛成三值枚举 `RealmUserErrorKind`。
9. **产物自检**（收窄后的三查：版本、逐列 schema diff、逐类行数）与「测试用真实 DLL 做最终验收」（进程外 DllVerifier 用目标客户端的包打开产物）——LZSync 对每个 fork 的产物也应做「目标 fork 客户端打得开」级别的验收，而不是只信自己的写入逻辑。

### 6.2 必须泛化（1→1 假设不成立）

1. **`OfficialBaselineSchema.KnownProperties` 是「官方 V51」的硬编码白名单**。LZSync 的「基线」必须是**每对 fork 组合的交集**或「每 fork 一份结构档案 + 运行时求交」，不能是一张静态表；同理 `SyncTables` / `EzOnlyPropertyNames` / `ez_only_rulesets` / `IsEzOnlySkin`（连具体皮肤 GUID 都写死了：`fc372386-381d-4f8e-897a-c1d89ef39f9c` 等三个）这些 Ez 特定知识要换成「fork 元数据」（每 fork 声明自己的扩展类/列/规则集/内置皮肤标识）。
2. **版本编码 `official * 1000 + ez`** 是 Ez2Lazer 一家的约定。N 个 fork 各有自己的编码方案，LZSync 需要一个「fork 身份 + schema 版本」的识别层：探测文件头版本 → 匹配 fork 指纹（结构特征：专有表/列名，即 `HasEzFingerprint` 思路的泛化），而不是 `<1000 即官方` 这种单叉判断。
3. **`SyncDirection` 四值枚举与 `RealmWritePlan` 的 (Ez, Ppy) 二元 kind**。N fork 是任意图拓扑：同步方向从「四种方向」变成「任意源→任意目标」，`StripEzFieldsForTarget`/`NormalizeEzFieldsForTarget` 这类成对特判要泛化为「目标结构档案驱动的列映射」。
4. **diff 键与冲突规则**：按 `(EntityKind, Guid)` 是 lazer 系的共同底座（各 fork 同源，GUID 稳定），可保留；但「冲突字段清单」（Hash/Ruleset/Date/难度名…）应随 fork 的列演化而数据化，且 N 路同步迟早需要「多源对同一目标」的合并语义（EZRealmSync 只有 1→1，没有三方合并/冲突解决，`Conflicted` 只是展示标签，覆盖策略永远是「源覆盖目标」）。
5. **官方收窄 → 「目标 fork 收窄」**：`DynamicRealmCopier` + 快照建库 + 过滤器 + 三查自检的流水线本身与 fork 无关（它已经按目标 schema 全库搬运），要换的只是：SchemaSource 来自目标 fork 的结构档案、ExportPolicy 的判定表来自目标 fork 元数据（哪些规则集/mod/判定语义它认识）。这是全项目里**泛化成本最低、收益最大**的一块。
6. **多 fork 数据布局**：EZRealmSync 假设 A/B 同属一个数据根或两个独立根、共享或各自的 files/；LZSync 要管理 N 个安装的路径发现、files/ 去重（同一 blob 在多个 fork 目录间复制），CopyMissing 的「按 bundle 哈希集合拷贝」模式可扩展成跨 N 目录的 blob 池同步。

### 6.3 不适用 / 不必照搬

- **WPF + Windows-only**：LZSync 技术栈（仓库现状为 Go + TS/Vite 前端）不同，UI 层（AppModel/Desktop/Pages）与本地化方案无参考价值；但其「UI 完全经四个服务接口驱动、Mock 后端可运行时切换」的**可测试性结构**值得保留精神。
- **「单目录内多份 client.realm」** 的管理模型：EZRealmSync 同步页本质是手工挑两个文件；LZSync 若做「N fork 自动发现 + 配对」需要自己的注册表/配置层（它的 `RealmFileRegistry` 只是内存字典，无持久化）。
- **`collection.db` / `scores.db` legacy 导出**、`.osr` 导出、修复页（非法字符/僵尸文件）：属周边功能，与同步内核解耦，LZSync 按需取舍。
- **「转回官方版」里 Ez 特有的丢弃规则**（diva/bms、外部托管 HostingKind、ManiaHitMode 语义、Ez 皮肤 GUID）：这是 Ez2Lazer 的领域知识，只应进 LZSync 的「Ez fork 元数据包」，不该进内核。
- 它**没有**的东西，LZSync 设计时要自己补：调度/自动化（无 CLI，纯 GUI 手动操作）、增量同步（每次都是全表重读全量 diff）、网络/远程同步、冲突解决策略（只有覆盖）、多用户/权限。

---

## 附：关键文件索引（调研时快照）

| 主题 | 文件（相对 `reference/EZRealmSync/`） |
|---|---|
| 统一访问入口 | `osu.Game.EzRealmSync/Realm/RealmAccessGateway.cs` |
| 动态会话 | `osu.Game.EzRealmSync/Realm/DynamicRealmSession.cs` |
| 头版本探测 | `osu.Game.EzRealmSync/Realm/RealmDiskSchemaReader.cs` |
| 基线白名单 | `osu.Game.EzRealmSync/Realm/OfficialBaselineSchema.cs` |
| Diff | `osu.Game.EzRealmSync/Realm/RealmDiffEngine.cs`、`RealmDiffConflictRules.cs`、`RealmSetCompareHelper.cs` |
| 同步读/写 | `Realm/DynamicBaselineReader.cs`、`Realm/DynamicBaselineWriter.cs`、`Realm/DynamicBaselineFileCopier.cs` |
| Ez 列 | `Realm/Dynamic/EzColumnResolver.cs`、`Realm/Dynamic/RealmSchemaSnapshotClassifier.cs` |
| 快照 | `Realm/Dynamic/RealmSchemaSnapshotStore.cs`、`RealmSchemaSnapshot.cs` |
| 收窄 | `Realm/Dynamic/DynamicOfficialConverter.cs`、`DynamicRealmCopier.cs`、`OfficialRealmCopyFilter.cs`、`OfficialSchemaSource.cs`、`Realm/OfficialExportPolicy.cs`、`Realm/OfficialModCatalog.cs` + `OfficialModList.json` |
| 服务入口 | `Realm/RealmEzRealmSyncService.cs`、`Realm/RealmRealmDataService.ConvertOfficial.cs` |
| 工程护栏 | `osu.Game.EzRealmSync.Tests/RealmAccessGatewayTest.cs`、`scripts/prune-publish.ps1`、`scripts/smoke-publish.ps1` |
| 路径/备份/日志/守卫 | `EzRealmSyncDataPaths.cs`、`IO/RealmFileBackup.cs`、`IO/RealmBackupCatalog.cs`、`IO/RealmFileWriteStamp.cs`、`EzRealmSyncLog.cs`、`Realm/RealmProcessGuard.cs`、`Models/RealmWorkspacePaths.cs` |
| 文档 | `README.md`、`docs/DATA-OPERATIONS.zh.md`、`docs/LEGACY-DB-EXPORT.zh.md`、`docs/ROADMAP.md`、`docs/FUTURE-RULESET-HOST.md`、`lib/README.md` |
