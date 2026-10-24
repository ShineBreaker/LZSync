# Ez2Lazer（osu!lazer fork）差异调研

> 调研对象：`reference/Ez2Lazer`（SK-la 的 ppy/osu fork，git HEAD `9190e03`，tag `2026.1023.0-ez2lazer`）与 `reference/lazer`（官方参照快照）。
> 参照快照不带 git 元数据，具体 tag **未确认**；两边 Realm 上游 schema 均为 52，可视为同一 schema 代的代码。
> 方法：目录级 `diff -rq`（osu.Game 侧 327 行差异输出：改动 276、Ez 独有 50、官方独有 1），再逐文件读关键 diff 与 fork 自有文档（`AGENTS.md`、`.cursor/skills/ez-realm-schema/SKILL.md`、`docs/EZ-UPSTREAM-MERGE.md`、`docs/EZ_ANALYSIS_STORAGE_REDESIGN.md`、`Ez2Lazer.Dependencies.props`）。
> 本文自包含：所有结构定义均抄录到字段级，不依赖 reference/ 目录存活。

## 0. 一览

| 维度 | 官方 lazer | Ez2Lazer | 共存/同步影响 |
|---|---|---|---|
| 游戏名 `GAME_NAME` | `osu!` | `Ez2Lazer` | — |
| 数据目录基名 `base_game_name` | `osu`（release）/ `osu-development`（debug） | `osu-Ez2Lazer` / `osu-Ez2Lazer-development` | **完全独立目录，可同机共存** |
| Realm 磁盘 schema 版本 | 52 | 52×1000+11 = **52011** | 同一文件两边互相打不开 |
| Realm 主库文件名（release） | `client.realm` | **`client_52011.realm`** | 文件名不同 |
| files/ 布局 | `files/<h0>/<h01>/<hash>` | 相同（`GetStoragePath` 逐字节一致） | 普通导入的文件可直接复制 |
| 上游 schema 兼容 | — | 追加式扩展，`schema_version` 恒跟上游 | 转官方需剥离 Ez 列/表 |

---

## 1. 身份与数据目录（问题 1）

### 1.1 游戏名与目录基名（原文摘录）

`osu.Desktop/Program.cs`（Ez2Lazer，第 22–27 行）：

```csharp
#if DEBUG
        private const string base_game_name = @"osu-Ez2Lazer-development";
#else
        private const string base_game_name = @"osu-Ez2Lazer";
#endif
```

官方 lazer 同位置为 `@"osu-development"` / `@"osu"`。`base_game_name` 被赋给 `string gameName`，随后 `Host.GetSuitableDesktopHost(gameName, hostOptions)`——osu!framework 用该名决定数据目录（Linux `~/.local/share/<name>`，Windows `%APPDATA%\<name>`）与单实例锁名。因此：

- **Ez2Lazer 数据目录**：`~/.local/share/osu-Ez2Lazer`（Linux）或 `%APPDATA%\osu-Ez2Lazer`（Windows）。
- **官方 lazer 数据目录**：`~/.local/share/osu`。两者无交集，**同一台机器可同时安装运行，互不读写对方数据**。
- 单实例检测只查同名进程，`Ez2Lazer` 与 `osu!` 互不冲突（Program.cs 中多开提示文案改为 "Ez2Lazer does not support multiple running instances."）。

### 1.2 其他身份常量

- `osu.Game/OsuGameBase.cs`：`public const string GAME_NAME = "Ez2Lazer";`（官方为 `"osu!"`；DEBUG 构建为 `"Ez2Lazer (development)"`）。同文件 `public const string CLIENT_DATABASE_FILENAME = @"client.realm";` **两边一致**（实际磁盘名差异来自 RealmAccess 的版本后缀机制，见 §2.2）。
- `osu.Game/IO/OsuStorage.cs`：**与官方零差异**（`diff -u` 为空），未重命名 `client.realm`、`files/` 等内部路径。
- `osu.Desktop/osu.nuspec`：`<id>ez2lazer</id>`、`<title>Ez2Lazer</title>`、`<authors>SK-la</authors>`、`projectUrl https://github.com/SK-la/osu`。
- `osu.Desktop/osu.Desktop.csproj`：`<InformationalVersion>$(ReleaseVersion)-ez2lazer</InformationalVersion>`；额外引用 fork 版 framework（`Ez2Lazer.Dependencies.props`：`Ez2LazerFrameworkVersion 2026.1023.0-ez2lazer`、`Ez2LazerGameResourcesVersion 2026.1022.0-ez2lazer`，即 framework 与 resources 均为 SK-la fork 版）。
- `osu.Game/Configuration/ReleaseStream.cs` 枚举**新增成员**：

```csharp
public enum ReleaseStream
{
    Lazer,
    [Description("Tachyon (Unstable)")]
    Tachyon,
    [Description("Ez2Lazer (SK-la)")]   // ← Ez 新增
    Ez2Lazer,
}
```

首次运行时强制 `LocalConfig.SetValue(OsuSetting.ReleaseStream, ReleaseStream.Ez2Lazer)`（OsuGameDesktop.CreateUpdateManager）；非官方构建（无 `OfficialBuildAttribute`）退回空 UpdateManager，官方构建走 Velopack 更新 Ez 渠道。

---

## 2. Realm schema 差异（问题 2）

### 2.1 双版本号机制（fork 核心设计）

`osu.Game/Database/RealmAccess.cs`（Ez，第 108–134 行，原文）：

```csharp
private const int schema_version = 52;                       // 与 ppy 上游对齐，只在合并上游时递增
public static int UpstreamSchemaVersion => schema_version;   // 供 EzRealmSync 等工具引用
public const int EZ_REALM_SCHEMA_VERSION = 11;               // 仅 Ez 字段变更时递增
public static int EzFileSchemaVersion => schema_version * 1000 + EZ_REALM_SCHEMA_VERSION;
```

- **磁盘 Realm schema version = 52011**（52×1000+11）。源码注释写 "currently 52010" 已过时（那是 v10 时的值），以代码计算为准。
- 磁盘版本解码（`decodeDiskSchemaVersion`，`internal` 静态方法）：`>=1000` 时 `upstream = v/1000, ez = v%1000`；`<1000` 时视为纯上游版本（ez=0）。
- 迁移顺序（`onMigration`）：先跑上游 1..52（`applyMigrationsForVersion`），再跑 Ez 1..11（`applyEzMigrationsForVersion`）。
- `RealmSchemaMode` 枚举：`Ez`（磁盘 = EzFileSchemaVersion，跑上游+Ez 迁移）/ `Official`（磁盘 = 52，只跑上游迁移）。
- 规则集程序集可贡献 Ez 迁移：`EzRealmMigrationContributorRegistry` 反射扫描 `osu.Game.Rulesets*` 程序集中实现 `IEzRealmMigrationContributor`（接口含 `IReadOnlyList<int> TargetEzVersions` 与 `Apply(Migration, int)`）的类型。BMS 规则集注册了 v7 贡献者（见 §5.2）。

### 2.2 主库文件名：Ez 任何构建都带版本后缀（重大差异）

官方 `RealmAccess` 构造函数中版本化文件名逻辑包在 `#if DEBUG` 内——release 用 `client.realm`，只有 DEBUG 用 `client_52.realm`。

Ez 版把 `#if DEBUG` **注释掉**，且 Ez 模式默认 `useDevelopmentVersionedFilenames = true`（第 292 行：`useDevelopmentVersionedFilenames1 = useDevelopmentVersionedFilenames ?? schemaMode == RealmSchemaMode.Ez`）：

```csharp
// #if DEBUG
            if (useDevelopmentVersionedFilenames1 && !DebugUtils.IsNUnitRunning)
                applyFilenameSchemaSuffix(ref Filename);
// #endif
```

因此 **Ez2Lazer 客户端（含 release）实际打开的主库是 `client_52011.realm`**，sidecar 前驱搜索链为 `client_52010.realm` → … → `client_52.realm` → `client_51011.realm` → … → `client.realm`（`EnumerateSidecarPredecessorFilenames`）。官方 release 客户端只认 `client.realm`。同步工具两边取库时文件名不同，这是硬差异。

### 2.3 为外部同步工具预留的 API（注释点名 EzRealmSync）

- `RealmAccess.OpenWithoutMigration(Storage, string filename, int pinnedDiskSchemaVersion, GameThread?)`：按磁盘已有 schema 打开、**不迁移**、不做破坏性恢复、`useDevelopmentVersionedFilenames: false`（即直接用传入的 `client.realm`/`client_52011.realm` 字面名）。
- `OfficialRealmAccess : RealmAccess`（`osu.Game/Database/OfficialRealmAccess.cs`，sealed）：
  - `OpenWithoutMigration(...)`：「以磁盘已有 schema 打开官方 client.realm，不执行任何迁移（供 EzRealmSync 等外部工具）」；
  - `OpenWithMigrationForTool(...)`：「供 EzRealmSync 修复页显式升级：执行官方 schema migration，禁止降级删库重建。仅应在已备份的工作副本上调用」。
- 工具打开时**不跑文件 GC**：`RealmFileStore.Cleanup()` 仅在 `performSchemaMigration && allowDestructiveRecoveryOnSchemaMismatch` 时执行，源码注释明说「主要避免 EzRealmSync 工具在升级时的误报、误清理」。

### 2.4 对官方模型追加的列（字段级）

**`BeatmapInfo`**（`osu.Game/Beatmaps/BeatmapInfo.cs`，追加于 `StarRating` 之后）：

| 属性 | 类型 | 默认 | 引入版本 | 语义 |
|---|---|---|---|---|
| `XxyStarRating` | `double` | `-1` | Ez v3 | mania xxy 基线星（无 mod），由 `BeatmapUpdater` 回填 |
| `PerformancePoints` | `double` | `-1` | Ez v4 | 无 mod 理论 PP；L1 面板/排序占位 |
| `HasVideo` | `bool?` | `null` | Ez v2（v5 改 nullable） | 谱面组 storyboard 是否含 Video 事件；null=未算 |
| `HasStoryboard` | `bool?` | `null` | Ez v2（v5 改 nullable） | 是否含 sprite/animation 事件；null=未算 |

**`ScoreInfo`**（`osu.Game/Scoring/ScoreInfo.cs`，均持久化列）：

| 属性 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `JudgeMode` | `int` | `0` | 全规则集共用判定档；ez v11 起为规范列，旧列 `ManiaHitMode` 数据已迁入 |
| `HealthMode` | `int` | `0` | 全规则集共用血量档；旧列 `ManiaHealthMode` 数据已迁入 |

另有 `[Ignored]`（**不持久化**，无需同步）：`EzBonus`（`EzScoreBonusSet?`）、`SessionOffsetPlusMania`/`SessionOffsetPlusNonMania`（double）、`SessionAccuracyCutoffA`（默认 `EzOnlineScoreSubmissionPolicy.DEFAULT_ACCURACY_CUTOFF_A = 0.9`）、`SessionAccuracyCutoffS`（默认 0.95）、`SessionSettingsCaptured`（bool）。

**`BeatmapSetInfo`**（外部托管，字段级）：

```csharp
public string ExternalContentRoot { get; set; } = string.Empty;   // 外部托管 set 的磁盘绝对路径
[MapTo(nameof(HostingKind))]
public int HostingKindInt { get; set; } = (int)BeatmapSetHostingKind.Internal;
public BeatmapSetHostingKind HostingKind { get; set; }             // Internal = 0, External = 1
public bool IsExternallyHosted => HostingKind == BeatmapSetHostingKind.External;
public string? GetEffectiveExternalContentRoot();  // 优先字段；回退 Hash 解码（见 §5.1）
```

**`RulesetInfo`**：

```csharp
public int LastAppliedXxySrVersion { get; set; }    // Ez v6：上次 xxy SR 算法版本（yyyyMMdd），用于失效 XxyStarRating
public int LastAppliedManiaSkillVersion { get; set; } // Ez v8：mania 技能算法版本，失效技能行
```

**`BeatmapMetadata`、`osu.Game/Models/` 全目录（RealmFile/RealmUser/RealmNamedFileUsage/RealmOnlineAsset 等）：与官方零差异**（官方 schema 52 新增的 `BeatmapMetadata.UserTags`、`ScoreInfo.Pauses`、`RealmOnlineAsset` 两边同在）。

### 2.5 Ez 新增的表（7 张，全部在 `osu.Game/EzOsuGame/Skills/`，字段级）

**EzBeatmapSkillValue**（谱面单项技能值，如 `beatmap_msd.stream`）：
`ID` Guid [PrimaryKey]；`BeatmapHash` string [Indexed]；`BeatmapId` Guid [Indexed]（hash 变更时维护用，可 Empty）；`SystemId` string [Indexed]；`SkillId` string [Indexed]；`Value` double；`AlgorithmVersion` int；`ComputedAt` DateTimeOffset。

**EzPlayerSkillValue**（玩家单键位数技能值，如 `player_ssr.stream @ 7K`）：
`ID` Guid PK；`Username` string [Indexed]；`KeyCount` int [Indexed]；`SystemId` [Indexed]；`SkillId` [Indexed]；`Value` double；`AnalyzedPlays` int；`Provisional` bool（向群体中位数收缩）；`Stale` bool（重算中）；`AlgorithmVersion`；`ComputedAt`。

**EzDanEstimate**（段位估计行，非 MSD/SSR 技能组）：
`ID` Guid PK；`Username` [Indexed]；`KeyCount` [Indexed]；`Side` string [Indexed]（`DanSkillSystem.SIDE_RC = "rc"` 或 `SIDE_LN`）；`RawDan` double（-1 未设）；`Label` string；`Clears` int；`BeyondTable` bool（超出已发布段位表上限）；`CourseName` string；`CourseAccuracy` double（-1 未设）；`ClearWindowHave` int（-1 未设）；`ClearWindowNeed` int（-1 未设）；`AlgorithmVersion`；`ComputedAt`。

**EzPlayerSkillHistoryPoint**（技能历史曲线点）：
`ID` Guid PK；`Username` [Indexed]；`KeyCount` [Indexed]；`SkillId` [Indexed]；`Value` double；`RecordedAt` DateTimeOffset [Indexed]；`AlgorithmVersion` int。

**EzBeatmapChartSkillInfo**（hub 对齐的谱面 chart 技能归档，EZ≥9）：
`ID` Guid PK；`BeatmapHash` [Indexed]；`BeatmapId` Guid [Indexed]；`InfoVersion` int；`ComputedAt`；`PatternTagsJoined` string（图案标签以 U+001F 连接，封闭词表，非 JSON）；`JackDemand` bool；`JackShare` double(-1 未设)；`StreamShare` double(-1)；`TechCategory` bool?；`ClusterTrill` bool?；`HandstreamCluster` bool?；`HandstreamEndurance` bool；`TechScore` double；`ChordjackScore` double；`JackScore` double(-1 保留)；`MotionRhythmBreak`/`MotionCrossHandTrill`/`MotionMiniJack`/`MotionSameHand` double（各 -1=motion 缺失）；`LnRatio` double(-1)；`Vibro` bool；`DanEligible` bool(默认 true)；`LengthSeconds` double(-1)；`KeyCount` int(-1)。

**EzPlayerDanSkillsetValue**（玩家技能组段位贴片缓存，EZ≥9）：
`ID` Guid PK；`Username` [Indexed]；`KeyCount` [Indexed]；`Side` [Indexed]（rc/ln）；`SkillsetId` [Indexed]；`RawDan` double(-1)；`Label` string；`Clears` int；`AlgorithmVersion`；`ComputedAt`。

**EzBeatmapChartDan**（nomod chart 段位基线 + 技能组标签戳，EZ≥10，每 BeatmapHash 一行）：
`ID` Guid PK；`BeatmapHash` [Indexed]；`BeatmapId` [Indexed]；`AlgorithmVersion` int（= 组合段位算法版本+上游 CSI/MSD facet，相等性过滤失效旧行）；`KeyCount` int；`HoldRatio` double；`OverallMsd` double；`RcRawDan` double(-1=RC 半门关闭)；`RcLabel` string；`LnRawDan` double(-1=LN 半门关闭)；`LnLabel` string；`RcSkillsetLabelsJoined`/`LnSkillsetLabelsJoined` string（`id␟label` 条目以 ␞ 连接）；`ComputedAt`。

**没有删除任何官方表**（lazer 侧独有的 RealmObject 文件为 0；唯一的官方独有普通文件是 `Screens/RankingV2/Legacy/LegacyRankingPanel.cs`，非 Realm 模型）。

### 2.6 Ez v1–v11 迁移历史（RealmAccess.cs 注释原文转述）

- v1：ScoreInfo 判定/血量档（后更名 Judgement→JudgeMode）。
- v2：BeatmapInfo.HasVideo/HasStoryboard。v5：两者改 nullable。
- v3：BeatmapInfo.XxyStarRating。v4：PerformancePoints。
- v6：RulesetInfo.LastAppliedXxySrVersion。
- v7：BeatmapSetInfo 外部托管路径规范化（含 BMS 旧 hash 迁移，见 §5.1）。
- v8：mania 技能 RealmObject 4 张 + RulesetInfo.LastAppliedManiaSkillVersion。
- v9：typed EzBeatmapChartSkillInfo + EzPlayerDanSkillsetValue（去 JSON blob）。
- v10：EzBeatmapChartDan。
- v11：**枚举重排** `EzEnumJudgeMode`/`EzEnumHealthMode`（StableClassic=1；Mania 私有档移到 31+），新增规范列 ScoreInfo.JudgeMode/HealthMode 并把旧 `ManiaHitMode`/`ManiaHealthMode` 数据迁入；重排映射 `RemapEzJudgeMode`：旧 0→Lazer(0)、1→EZ2AC(31)、2→O2Jam(32)、3→IIDX_HD(33)、4→LR2_HD(34)、5→Raja_NM(35)、6→Malody_E(36)、7→Malody_B(37)、8→`LEGACY_CLASSIC_JUDGE_MODE=38`（已淘汰，仅作「需按 classic 语义重算」标记）、9→StableClassic(1)；负值保持。

### 2.7 判定/血量档枚举取值域（`EzOsuGame/Configuration/EzEnumGameplay.cs`）

```
EzEnumJudgeMode: Lazer=0, StableClassic=1, EZ2AC=31, O2Jam=32, IIDX_HD=33,
                 LR2_HD=34, Raja_NM=35, Malody_E=36, Malody_B=37
EzEnumHealthMode: Lazer=0, StableClassic=1, O2JamEasy=31, O2JamNormal=32,
                  O2JamHard=33, Ez2Ac=34, IIDX_HD=35, LR2_HD=36, Raja_HD=37
```

编码约定（注释原文）：共享低段 0=Lazer、1=StableClassic；规则集私有档 = 规则集 OnlineID × 10 起（Mania=3 → 31..39），其他规则集取自己的段（如 4x）。**官方库没有 JudgeMode/HealthMode 概念，任何非 0 值的语义官方都不存在。**

---

## 3. 设置差异（问题 3）

### 3.1 `OsuConfigManager`：只改默认值，无枚举成员增删

`OsuGameSetting`（即 `OsuConfigManager.cs` 内 `enum OsuSetting`）两边成员一致。Ez 改了 9 个默认值（`SetDefault` 原文）：

| 设置 | 官方默认 | Ez 默认 |
|---|---|---|
| `ShowConvertedBeatmaps` | `true` | `false` |
| `ModSelectTextSearchStartsActive` | `true` | `false` |
| `SongSelectBackgroundBlur` | `false` | `true` |
| `ShowFpsDisplay` | `false` | `true` |
| `PositionalHitsoundsLevel` | `0.2f`（0..1 步进 0.01） | `0.7f` |
| `StarFountains` | `true` | `false` |
| `KeyOverlay` | `false` | `true` |
| `AlwaysShowHoldForMenuButton` | `false` | `true` |
| `AlwaysRequireHoldingForPause` | `false` | `true` |

（这些是「新安装默认值」，存量用户的 realm `Settings` 行不回写——fork 约定见其 AGENTS.md「用户未明确要求时，不在加载或启动路径改写已保存的配置」。）

### 3.2 Ez 专有设置走独立 ini，不进 Realm

`EzOsuGame/Configuration/Ez2ConfigManager.cs`：

```csharp
public class Ez2ConfigManager : IniConfigManager<Ez2Setting>, IGameplaySettings
{
    protected override string Filename => "EzSkinSettings.ini";
```

`Ez2Setting` 枚举共 **167 个成员**（含 `ServerPreset`、`ServerGuToken`、`ServerManualUsername/Token`、Pixiv 系列、`TurboMode`、`FlowMode`、`ScratchAxis*`、`DesktopPet*`、`MaterialUi*`、`EzAnalysisSqliteEnabled` 等）。文件落在数据目录根部 `EzSkinSettings.ini`，**官方客户端自然忽略它，同步工具无需处理**。另有 `EzRulesetMapping.ini`（外部规则集 OnlineID 映射，见 §5.3）。

---

## 4. 谱面状态语义与成绩上传（问题 4）

### 4.1 BeatmapOnlineStatus：未改

`osu.Game/Beatmaps/BeatmapOnlineStatus.cs` **与官方零差异**（枚举值 None/-4…Ranked/Qualified/Loved/Graveyard/Pending/LocallyModified 不变）。全库检索 `Approval`/强制改状态的写点，仅发现：

- 外部托管 set 及其难度在入库时被标 `BeatmapOnlineStatus.LocallyModified`（`BMSOsuLibrarySynchronizer.cs:104/147`）；
- 谱面导出工具把导出副本状态设为 `None`（`EzOsuGame/Statistics/BeatmapExportUtils.cs:37`）。

**结论：没有「强制 approval/绕过在线状态检查」的改动**；任务给的「是否强制 approval」这一假设在现场代码中不成立。在线元数据查询链路（`BeatmapUpdaterMetadataLookup.cs`）与官方零差异。

### 4.2 成绩上传：收紧而非绕过

`EzOsuGame/Scoring/EzOnlineScoreSubmissionPolicy.cs`（静态类）：

- 常量：`DEFAULT_ACCURACY_CUTOFF_A = 0.9`、`DEFAULT_ACCURACY_CUTOFF_S = 0.95`。
- `ApplySessionSettingsSnapshot(ScoreInfo, Ez2ConfigManager)`：游玩开始时把 offset/精度门槛快照进 ScoreInfo 的 `[Ignored]` 字段（mania 在游玩结束时由 `EzScoreInfoExtensions.ApplyManiaGameplayModes` 写 JudgeMode/HealthMode；osu! 规则集当场快照 `StdJudgeMode`）。
- `AllowsOfficialSubmission(ScoreInfo, out string blockReason)`：**提交官方服务器的门槛**。mania（`Ruleset.OnlineID == 3`）成绩必须同时满足：① JudgeMode 与 HealthMode 均为 Lazer(0)；② `SessionOffsetPlusMania == 0`；③ 两个精度门槛为默认值；否则拒绝。osu! 规则集走 `TryGetStdGameplayMode` 同类检查。

调用点 `SubmittingPlayer.cs:384`（token 获取之后、提交之前）：

```csharp
if (!EzOnlineScoreSubmissionPolicy.AllowsOfficialSubmission(score.ScoreInfo, out string blockReason))
{
    Logger.Log($"[Ez] Score submission blocked by session snapshot ({blockReason}).", ...);
    return Task.CompletedTask;
}
```

即：**用了 Ez 判定档/offset/门槛的成绩不会上传官方**，官方排行榜不会看到非官方判定的成绩。对非官方服务器（`ServerPreset != Official`），`SubmittingPlayer.Ez.cs` 额外把 `judgeMode/healthMode/onlineScoreId/beatmapId` 通过 `SubmitEzManiaScoreMetadataRequest` 上传（仅 `beatmapId > 0` 时）。

### 4.3 ScoreInfo 反序列化容错（同步工具必须关注）

Ez 改了三处 JSON 反序列化（官方为直转）：

1. `StatisticsJson` / `MaximumStatisticsJson`：先 `DeserializeObject<Dictionary<string, int>>` 再 `Enum.TryParse<HitResult>` 逐键解析，**未知键跳过**，`JsonException` 回退空表。→ 说明 Ez 库中成绩的 statistics 可能含官方不认识的自定义 `HitResult` 键（BMS 等规则集），**官方客户端对 `Dictionary<HitResult,int>` 直转会因未知枚举字符串抛 `JsonSerializationException`**，同步时必须过滤未知键。
2. `ModsJson`：解析失败回退 `Array.Empty<APIMod>()` 并记日志。
3. `Mods` 属性：`APIMod.ToMod` 解析为 `UnknownMod`（官方已删除/改名的 mod）时**剥离**并日志提示，`ModsJson` 本身不回写。

### 4.4 服务器预设

`EzOsuGame/Online/ServerPreset.cs`：`Official = 0, Manual = 1, g0v0 = 2`（g0v0 即 `reference/g0v0` 配套私服）。`ServerConfig` 持有 `ApiUrl/WebsiteUrl/ClientId/ClientSecret/SpectatorUrl/MultiplayerUrl/MetadataUrl`。OsuGameBase 按 preset 构造 `ManualServerEndpointConfiguration` 等。Ez 客户端连接官方 API 时的行为（token、score 提交协议本身）沿用官方代码，只是多了 §4.2 的 gate。

---

## 5. fork 特有能力（问题 5）

### 5.1 外部托管谱面集（External hosting）

- `Beatmaps/BeatmapSetHostingKind.cs`：`Internal = 0`（内容在 `files/`）、`External = 1`（内容在 `ExternalContentRoot` 磁盘目录原地读取）。
- `Beatmaps/ExternalLibraries/ExternalBeatmapPathEncoding.cs`：外部 set 的 `BeatmapSetInfo.Hash` = `"ext:set:" + Base64(UTF-8(规范化绝对路径))`（`HASH_PREFIX = "ext:set:"`）。提供 `Encode/TryDecode/IsExternalSetHash/TryResolveContentRoot`。**这类 Hash 不是文件内容哈希**。
- `RealmFileStore.RegisterExternalHash(string sha256Hash, Realm realm, bool addToRealm = true)`：只为已在磁盘上的文件登记 `RealmFile` 行，**不复制字节进 files/**（官方只有 `Add(Stream, ...)` 会复制）。
- BMS 外部谱面的 `RealmFile.Hash` 是**路径哈希**：`BmsPathKeys.ComputeRealmFileHash(chartPath) = SHA256(lowercase(normalized fullpath))`（`BmsPathKeys.cs`，注释明言 "no content hashing"）。
- Ez v7 迁移 + `BmsLegacyExternalBeatmapEzMigration`（`osu.Game.Rulesets.BMS/Database/`，TargetEzVersions=[7]）：把旧 `bms-ext:set:` 前缀 hash 统一重写为共享 `ext:set:` 格式，并回填 `HostingKind=External` 与 `ExternalContentRoot`。
- `BMSOsuLibrarySynchronizer`：外部 BMS 库（磁盘目录树）→ Realm 的增量同步器；set 级写 `Hash=Encode(contentRoot)`、`ExternalContentRoot`、`HostingKind=External`、`Status=LocallyModified`；难度级 `BeatmapInfo.Hash = RealmFile 合成哈希`、`MD5Hash = targetBeatmap.MD5Hash`（来自索引），难度文件以相对路径注册为 `RealmNamedFileUsage`。另有 `ExternalBeatmapFileStore` / `ExternalBeatmapFileMappingsBuilder`（资源从原目录解析）；`WorkingBeatmapCache.cs:311` 注释确认「External sets keep bytes on disk (RegisterExternalHash); global texture stores only see files/」。

### 5.2 新规则集

- **BMS**：仓库内完整项目 `osu.Game.Rulesets.BMS`（+ `osu.Game.Rulesets.BMS.Tests`），`BMSRuleset.ShortName => "bms"`，基于 Mania 适配（`ManiaDifficultyCalculator` 等）。
- **DIVA（ProjectDIVA）**：仓库内**无**项目。仅两处代码注释佐证其以外部规则集形式存在：`LegacyChartTextEncoding.cs`（"legacy chart formats (BMS, ProjectDIVA, …)"）与 `EzQuickRotationRankedPlayCardContent.cs:32`（"GetRuleset(-1) is ambiguous when BMS/DIVA coexist"）。**未确认其 DLL 及 RulesetInfo 具体取值**（不在本仓库）。
- 其他 Ez 专有项目：`osu.Game.EzCamera.Windows`（摄像头 HUD 后端）。

### 5.3 外部规则集加载框架

- `EzExternalRulesetScanner.Scan(Storage)`：从数据目录 `rulesets/` 加载 `osu.Game.Rulesets.*.dll`（排除 Tests），反射实例化 `Ruleset` 子类。
- `EzRulesetMappingConfig`：ini 文件 `EzRulesetMapping.ini`，节=ShortName，键 `Enabled/OnlineID/Order`；头注释 "OnlineID must avoid 0~3 (official modes). Use -1 or >= 4. Restart required."
- `EzExternalRulesetMapping`：`EXPLICIT_ONLINE_ID_MINIMUM = 4`；`IsOfficialOnlineId = 0..ILegacyRuleset.MAX_LEGACY_RULESET_ID`。
- `EzRealmRulesetStore : RealmRulesetStore`：`OnBeforeRulesetValidation` 中应用映射（保留 ID 集合 `{0,1,2,3}`），可按配置禁用规则集（`Available=false`）。上游 `RealmRulesetStore` 被改造：`PrepareDetachedRulesets` 改 protected virtual、插入 `OnBeforeRulesetValidation`/`ShouldDisableRuleset`/`ShouldAllowOnlineIdMismatch`/`ShouldSkipDuplicateOnlineIdCheck` 钩子——**放宽了官方的 OnlineID mismatch 抛错与重复 ID 抛错**。

### 5.4 Ez 分析与技能数据（Realm 之外的持久化）

- EzAnalysis 主库：独立 SQLite，文件名 `ez-analysis_v{ANALYSIS_VERSION}.sqlite`，当前 `ANALYSIS_VERSION = 8`（`EzAnalysisPersistentStore`）；旧稳定名 `ez-analysis.sqlite` 首启迁移一次。分支快照库在数据目录 `EzData/` 下（`SONGS_BRANCH_DATABASE_DIRECTORY = "EzData"`），文件名形如 `songs_{ruleset}_{timestamp}_{suffix}.sqlite`。表结构（v6 设计文档裁决）：主表 `ez_analysis_entry`（`beatmap_id, beatmap_hash, beatmap_md5, ruleset_online_id, common_updated_at, kps_avg, kps_max, kps_list_json, tag_updated_at, tag_payload_json`）+ mania 扩展表 `ez_analysis_mania`（`beatmap_id, updated_at, xxy_sr, column_counts_json, hold_note_counts_json`）；`*_updated_at == 0` 表示切片未补齐。
- mania 技能系统：7 张 Realm 表（§2.5）+ SQLite evidence 明细。
- 其他：桌宠（Live2D）、Pixiv 背景源、ScriptedSkin（csx 脚本皮肤）、本地头像、Ez 字体系统、结算统计扩展等，均在 `osu.Game/EzOsuGame/`（650 个 .cs 文件，57 个子目录），对 Realm/存储布局无额外影响（除上述 SQLite）。

### 5.5 上游合并基线

`docs/EZ-UPSTREAM-MERGE.md`：最近一次上游合并 tag 为 `2026.819.0-tachyon`；合并纪律「schema_version 跟上游，EZ_REALM_SCHEMA_VERSION 仅 Ez 字段」「新功能优先放 `osu.Game/EzOsuGame/**` 或 `*.Ez.cs` partial」。Ez fork 的 framework/resources 版本 `2026.1023.0-ez2lazer` / `2026.1022.0-ez2lazer`。

---

## 6. files/ 目录布局（问题 6）

- 路径函数两边**逐字符一致**（`Extensions/ModelExtensions.cs:23`）：

```csharp
public static string GetStoragePath(this IFileInfo fileInfo)
    => Path.Combine(fileInfo.Hash.Remove(1), fileInfo.Hash.Remove(2), fileInfo.Hash);
```

即 `files/<hash第1字符>/<hash前2字符>/<完整hash>`（SHA-256 十六进制小写）。
- `RealmFile` 模型（`osu.Game/Models/RealmFile.cs`）零差异；`OsuStorage.cs` 零差异；`RealmFileStore` 仅追加 `RegisterExternalHash`（§5.1），正常导入路径 `Add(Stream...)` 行为一致（含 hardlink 优化逻辑）。

**结论：可以直接共享或复制 files/**——普通（Internal）导入的字节块布局与官方完全相同，哈希键也相同（同一文件内容同一 SHA-256）。**唯一例外**：外部托管 set 的 `RealmFile` 行是路径哈希且 files/ 中**没有**对应文件；把这些行带入官方库是坏引用（官方读该 set 的文件流会失败）。

---

## 7. 对同步工具的启示（问题 7）

把 Ez 库（磁盘 schema 52011，文件 `client_52011.realm`）转回官方（schema 52，文件 `client.realm`）时的硬约束清单：

**A. 必须丢弃的列**（官方 schema 52 无；Realm 打开旧版本文件时多余列会因 schema 不匹配而无法打开，必须以官方 schema 重建后选择性拷行）：
- `BeatmapInfo`：`XxyStarRating`、`PerformancePoints`、`HasVideo`、`HasStoryboard`
- `ScoreInfo`：`JudgeMode`、`HealthMode`
- `BeatmapSetInfo`：`ExternalContentRoot`、`HostingKindInt`（MapTo `HostingKind`）
- `RulesetInfo`：`LastAppliedXxySrVersion`、`LastAppliedManiaSkillVersion`

**B. 必须丢弃的表**：`EzBeatmapSkillValue`、`EzPlayerSkillValue`、`EzDanEstimate`、`EzPlayerSkillHistoryPoint`、`EzBeatmapChartSkillInfo`、`EzPlayerDanSkillsetValue`、`EzBeatmapChartDan`（7 张）。

**C. 必须丢弃的 BeatmapSetInfo 行**：`HostingKind == External`（或 `Hash` 以 `ext:set:` 开头）的 set——其 RealmFile 行是路径哈希、files/ 无字节、`ExternalContentRoot` 指向外部目录。随之丢弃该 set 全部 BeatmapInfo、其 RealmNamedFileUsage，以及 `BeatmapInfo.BeatmapSet` 指向它的 ScoreInfo（BMS 成绩）。

**D. 必须丢弃/过滤的 RulesetInfo 行与成绩**：`ShortName = "bms"`（及 diva 等外部规则集）的 RulesetInfo 行；`ScoreInfo.Ruleset.ShortName` 属于这些规则集的成绩全部丢弃（官方无 InstantiationInfo 可解析）。官方四规则集（osu/taiko/fruits/mania）的成绩行可保留，但其 JudgeMode/HealthMode != 0 的「非官方判定」语义在官方库无处安放（列被丢弃即信息丢失；这些成绩本来也被 §4.2 的 gate 挡在官方提交之外，是否保留属工具策略）。

**E. 必须转换的成绩 JSON**：
- `StatisticsJson`/`MaximumStatisticsJson`：过滤官方 `HitResult` 无法解析的键（BMS 自定义判定名），否则官方反序列化抛异常导致成绩不可用（Ez 侧为此专门加了容错，官方没有）。
- `ModsJson`：Ez 独有 mod（LAsMods、CommunityMod 等）acronym 在官方侧解析为 UnknownMod/抛错——过滤或整条丢弃。
- `APIMods` 同上。

**F. RealmFile 行**：Internal set 引用的行可直接保留（哈希一致）；External set 的行（路径哈希）随 set 丢弃。files/ 可整体复制/硬链，但复制后官方首次 `RealmFileStore.Cleanup()` 只会清理无引用文件，不会破坏保留行。

**G. 文件名与打开方式**：
- Ez 主库是 `client_52011.realm`（release 也是）；官方主库 release 是 `client.realm`。
- 建议直接使用 fork 预留 API：`OfficialRealmAccess.OpenWithoutMigration`（读官方库）/ `RealmAccess.OpenWithoutMigration`（读 Ez 库，pinned 52011）/ `OfficialRealmAccess.OpenWithMigrationForTool`（修复页，只许升不许降）。这些 API 已保证不迁移、不 GC、不破坏性恢复。

**H. 无需处理**：Ez2Setting 167 项设置（`EzSkinSettings.ini`）、EzAnalysis SQLite（`ez-analysis_v8.sqlite`、`EzData/`）、`EzRulesetMapping.ini`——均在数据目录，官方客户端不读取，留在 Ez 目录即可；ScoreInfo 的 `[Ignored]` 会话快照字段不持久化。

**I. 两库并存安全**：数据目录不同（§1），Ez 与官方可在同一台机器共存；同步工具应以 Ez 的 `~/.local/share/osu-Ez2Lazer` 与官方的 `~/.local/share/osu` 为两个独立根。

---

## 8. 任务给定线索的核验结果

| 线索（来自 EZRealmSync 文档） | 核验结果 |
|---|---|
| 「扩展了 Realm 列」 | ✅ 属实，见 §2.4（4+2+2+2 列，字段级已列） |
| 「新增 diva/bms 规则集」 | ⚠️ 部分属实：BMS 在仓库内（`osu.Game.Rulesets.BMS`，ShortName="bms"）；**diva 未在本仓库**，仅注释佐证其为 rulesets/ 目录外部 DLL（未确认） |
| 「支持外部托管谱面集」 | ✅ 属实：`HostingKind/ExternalContentRoot/ext:set: hash/RegisterExternalHash`，见 §5.1 |
| 「这些在转回官方时会被丢弃」 | ✅ 与代码一致，且必须连带丢弃对应 set/成绩/RealmFile 行，见 §7 |
| 「是否强制 approval / 绕过在线状态检查」（任务待验假设） | ❌ 未发现：`BeatmapOnlineStatus` 枚举与元数据查询链路零差异；成绩上传反而被 §4.2 gate 收紧 |

## 9. 未确认事项

1. lazer 参照快照的确切 tag/commit（无 git 元数据）；仅能以 schema 52 锚定代际。
2. DIVA（ProjectDIVA）规则集 DLL 的存在、ShortName 与 OnlineID 取值（不在 Ez2Lazer 仓库；需到目标机器数据目录 `rulesets/` 验证）。
3. `Ez2Lazer` 客户端更新渠道（Velopack Ez feed）的实际 URL——代码中未见硬编码，未确认（与本调研目标关系弱）。
4. Ez 库中实际存在的 `StatisticsJson` 自定义键集合、Ez 独有 mod acronym 集合——需对真实用户库取样统计（本调研只有源码证据：官方侧解析会失败这一事实成立）。
