# DeltaLazer fork 差异调研（相对官方 osu!lazer）

> 调研对象：`reference/delta`（DeltaLazer，<https://github.com/deltalazer/delta>）与 `reference/lazer`（官方 ppy/osu，对照基准）。
> 调研方式：目录级 `diff -rq` + 关键文件逐一比对。**本文档自包含**：所有事实均摘录自比对时的源码原文，`reference/` 目录删除后仍可完整阅读。
> 调研日期：2026-10-24。

## 0. 基线与总体结论

- DeltaLazer 是 osu!lazer 的社区 fork，定位是「lazer 客户端自定义玩法的试验场」（README 原文：*"a community-driven fork of osu!lazer, made to be a playground for custom implementations with the lazer client!"*）。
- **基线落后**：delta 基于 .NET 8.0 / ppy.osu.Framework `2026.731.0` / ppy.osu.Game.Resources `2026.710.0`；对照的官方仓库是 net10.0 / Framework `2026.1019.0` / Resources `2026.1021.0`。delta 基线约为 2026 年 7-8 月，落后官方参考约 3 个月。因此两仓库约 349 个文件「内容不同」，**其中绝大多数是官方这 3 个月的演进而 delta 未同步**（如 `Lock` 类型、`IConfiguredMod` 接口、编辑器新键位、`RankingV2` 界面等），并非 fork 主动改动。本文只记录经甄别后确认的 fork 语义差异与同步工具必须知道的结构差异。
- **对同步工具最重要的结论**：delta 的 Realm 数据层与官方**完全同构**（schema 51，仅缺官方 v52 新增的一张缓存表），fork 的玩法数据全部写在谱面 `.osu` 文本文件的两个扩展 Section 里，不进数据库。两客户端数据目录不同名、内部布局相同，可共存。

## 1. 身份与数据目录（问题 1）

### 1.1 游戏名

`delta/osu.Desktop/Program.cs`（对照 `lazer/osu.Desktop/Program.cs`）：

```csharp
#if DEBUG
        private const string base_game_name = @"deltalazer-development";   // 官方: osu-development
#else
        private const string base_game_name = @"deltalazer";               // 官方: osu
#endif
```

`delta/osu.Game/OsuGameBase.cs:81,83`（对照 `lazer/osu.Game/OsuGameBase.cs:81,83`）：

```csharp
#if DEBUG
        public const string GAME_NAME = "deltalazer (development)";        // 官方: "osu! (development)"
#else
        public const string GAME_NAME = "deltalazer";                      // 官方: "osu!"
#endif
```

程序集图标：`delta/osu.Desktop/OsuGameDesktop.cs` 使用内嵌资源 `delta.ico`（官方为 `lazer.ico`）；Windows 安装图标路径常量 `osu.Desktop/Windows/Icons.cs:15` 为 `Path.Join(icon_directory, "delta.ico")`。Discord Rich Presence 资产键 `"deltalogo"`，状态文本追加 `"• delta"`。

### 1.2 数据目录与共存性

- 数据目录名由 `base_game_name` 决定（`Program.cs:77` `string gameName = base_game_name;`，多开时 `--tc:id` 追加 `-{clientID}` 后缀，逻辑与官方一致）。Linux 下即 `~/.local/share/deltalazer`（Release）vs 官方 `~/.local/share/osu`。**目录名不同，同机完全可共存**。
- `osu.Game/IO/OsuStorage.cs`、`osu.Game/IO/StableStorage.cs` 两边逐字节一致（diff 无输出）——对 stable 安装的探测/迁移逻辑未改。
- Realm 数据库文件名一致：两边 `OsuGameBase.cs:91` 均为 `public const string CLIENT_DATABASE_FILENAME = @"client.realm";`。
- 顶层 `reference/delta/files/` 只是仓库里的 3 张 logo 图片（`deltafinal.png` 等），与运行时数据无关。

### 1.3 网络端点（重要）

delta **未改任何服务器端点**。逐字节一致的文件（diff 无输出）：

- `osu.Game/Online/ProductionEndpointConfiguration.cs`：`WebsiteUrl = APIUrl = @"https://osu.ppy.sh"`，`SpectatorUrl = "https://spectator.osu.ppy.sh/spectator"`，`MultiplayerUrl = "https://spectator.osu.ppy.sh/multiplayer"`，`MetadataUrl = "https://spectator.osu.ppy.sh/metadata"`，`BeatmapSubmissionServiceUrl = "https://bss.ppy.sh"`。
- `osu.Game/Online/DevelopmentEndpointConfiguration.cs`：`WebsiteUrl = APIUrl = @"https://dev.ppy.sh"`。
- `osu.Game/Online/API/APIAccess.cs`、`osu.Game/Online/API/OAuth.cs`（client_id 传递逻辑未改）。

即 delta 仍是连接官方 osu! 服务器的客户端（见 §4 登录禁用）。

## 2. Realm schema 差异（问题 2）

### 2.1 schema 版本号

- 官方：`lazer/osu.Game/Database/RealmAccess.cs:106` — `private const int schema_version = 52;`
- delta：`delta/osu.Game/Database/RealmAccess.cs:105` — `private const int schema_version = 51;`

官方版本历史注释（RealmAccess.cs 顶部，两边共有部分）：

```
/// 49   2025-06-10    Reset the LegacyOnlineID to -1 for all scores ...
/// 50   2025-07-11    Add UserTags to BeatmapMetadata.
/// 51   2025-07-22    Add ScoreInfo.Pauses.
/// 52   2026-07-28    Add RealmOnlineAsset.        ← 仅官方有此行
```

### 2.2 表级差异：仅一张

RealmObject 派生类清单（按 `grep -rl ": RealmObject"` 全目录比对）：官方 21 个，delta 20 个，**唯一差集为 `osu.Game/Models/RealmOnlineAsset.cs`**（官方 v52 新增，delta 无）。官方该表定义：

```csharp
public class RealmOnlineAsset : RealmObject
{
    public RealmFileUsage File { get; set; }          // 类型为 RealmNamedFileUsage（含原始 URL 与磁盘位置）
    [Indexed]
    public DateTimeOffset LastAccessed { get; set; } = DateTimeOffset.Now;
    public RealmOnlineAsset(RealmFile localFile, string remoteUrl) { ... }
}
```

该表仅用于缓存在线资源（头像/封面等），官方会在 `cleanupPendingDeletions` 中清理「超过一个月未访问」的记录（`RealmAccess.cs`，delta 无此清理段）。**无业务数据**。

### 2.3 字段级差异：零

以下持久化类两边逐行 diff **完全一致**（0 行差异）：

`Beatmaps/BeatmapInfo.cs`、`Beatmaps/BeatmapSetInfo.cs`、`Beatmaps/BeatmapMetadata.cs`、`Collections/BeatmapCollection.cs`、`Scoring/ScoreInfo.cs`（Realm 部分）、`Skinning/SkinInfo.cs`、`Rulesets/RulesetInfo.cs`、`Input/Bindings/RealmKeyBinding.cs`、`Configuration/RealmRulesetSetting.cs`、`Models/RealmFile.cs`。`Rulesets/Mods/ModPreset.cs` 仅差一行 `using`（官方因新接口 `IConfiguredMod` 增加 using，无字段变化）。

`ScoreInfo.cs` 的 diff 仅含：`LegacyTotalScore` 注释差异、`IScoreInfo` 显式接口实现区域（官方新增 `IConfiguredMod` 相关成员）、`GetStatisticsForDisplay()` 的位置差异——**均为官方侧演进，无 fork 字段增删**。

**结论：delta 没有任何 fork 专属的 Realm 表、列或迁移；delta 的 client.realm（v51）可直接被官方客户端打开并自动迁移到 v52（Realm 迁移代码内嵌于官方客户端，与数据库文件同目录无关）。**

## 3. 设置差异（问题 3）

设置存储机制一致：两边 `OsuConfigManager : IniConfigManager<OsuSetting>`，构造均为 `: base(storage)`（即各写各的数据目录，互不干扰）。

`diff lazer/osu.Game/Configuration/OsuConfigManager.cs delta/osu.Game/Configuration/OsuConfigManager.cs` 的全部实质差异：

### 3.1 delta 新增的枚举值与默认值（官方没有）

```csharp
// 枚举成员（插在 MenuCursorSize 之后）
MenuCursorFromSkin,
MenuCursorFromSkinConfirmed,
MenuCursorFromSkinSize,
MenuCursorTrail,
MenuCursorScaleX,
MenuCursorScaleY,

// 默认值
SetDefault(OsuSetting.MenuCursorSize, 1.0f, 0.5f, 2f, 0.01f);          // ←官方有，锚点
SetDefault(OsuSetting.MenuCursorFromSkin, false);
SetDefault(OsuSetting.MenuCursorFromSkinConfirmed, false);
SetDefault(OsuSetting.MenuCursorFromSkinSize, 1.0f, 0.2f, 2f, 0.01f);
SetDefault(OsuSetting.MenuCursorTrail, false);
SetDefault(OsuSetting.MenuCursorScaleX, 1.0f, 0.5f, 2f, 0.01f);
SetDefault(OsuSetting.MenuCursorScaleY, 1.0f, 0.5f, 2f, 0.01f);
```

全部服务于 fork 的「菜单光标自定义」能力（皮肤光标 + X/Y 独立缩放 + 轨迹，配套 fork 新文件 `osu.Game/Graphics/Cursor/CursorTrail.cs` 与重写的 `MenuCursorContainer.cs`）。

### 3.2 delta 缺失的官方枚举值与默认值（官方新增、delta 基线没有）

```csharp
SetDefault(OsuSetting.SongSelectGroupMode, GroupMode.None);            // ←两边共有，锚点
// 官方有、delta 无：
SetDefault(OsuSetting.SongSelectCollectionFilter, string.Empty);
SetDefault(OsuSetting.HitsoundVolume, 0.6, 0, 1, 0.01);
SetDefault(OsuSetting.PMFriendsOnly, false);
```

### 3.3 行为差异（无设置项）

音量滚动：官方新版把「滚轮调音量」改为 `ScrollAdjustsVolume(bool requireAltPressed = false)` + 需按 Alt + 新接口 `osu.Game/Input/IBlockGlobalAltScrollVolume.cs`；delta 保留旧行为类 `osu.Game/Overlays/Volume/GlobalScrollAdjustsVolume.cs`（滚动即调音量，无需 Alt）。属基线差异，不是 delta 的功能。

## 4. 谱面状态语义与成绩上传（问题 4）

### 4.1 无 approval/在线状态绕过

逐字节一致（diff 无输出）：`Beatmaps/BeatmapOnlineStatus.cs`、`Beatmaps/BeatmapSetOnlineStatus.cs`、`Beatmaps/BeatmapManager.cs`、`Beatmaps/BeatmapUpdaterMetadataLookup.cs`、`Beatmaps/BeatmapInfo.cs`、`Beatmaps/BeatmapSetInfo.cs`。delta **没有**修改 `BeatmapOnlineStatus` 枚举、没有强制 approval、没有绕过任何在线状态检查逻辑——它不需要绕过，因为它走的是更彻底的路线：

### 4.2 直接禁用登录（fork 主动改动）

`delta/osu.Game/Overlays/Login/LoginForm.cs`（对照官方同文件）：

- 登录按钮 `Action = null`（官方为 `Action = performLogin`），后随注释 `// Standard Action: performLogin`；
- 注册按钮同样 `Action = null`；
- 界面插入红底提示框，原文：

> *"Logins are currently disabled, as this client still connects to servers upstream. DeltaLazer contains features that make it incompatible with the game's current infrastructure, with Bancho blocking score uploads through modified clients. Sorry! :P"*

- 标题栏追加 "Delta Server" 链接 → `https://deltalazer.vercel.app/`。
- 细节：`password.OnCommit`（密码框回车）仍绑定 `performLogin`，即回车路径代码上可达，但按钮不可点（未验证实际效果，标注：未确认）。

README「important server note」佐证（原文）：

> *"We have temporarily disabled logins as this client still connects to osu! servers. When bypassed, osu!bancho prohibits score submission as this client is modified."*

### 4.3 成绩上传机制

`osu.Game/Screens/Play/SubmittingPlayer.cs` 的 diff 仅为基线演进（`Lock` vs `object`、`?.` 写法），**无 fork 语义改动**。由于无法登录（`DummyAPIAccess` 状态），delta 内所有成绩事实上只落本地 client.realm 的 ScoreInfo 表；「上传」路径自然不触发。gimmick 玩法的判定合法性由 fork 新接口保障（见 §5.3），成绩结构（Statistics/MaximumStatistics 字典）仍用官方枚举，不引入新 HitResult 枚举值。

## 5. fork 特有能力（问题 5）

### 5.1 Gimmick 体系（核心特性，数据全在 .osu 文件）

fork 扩展了 beatmap 文件格式。`delta/osu.Game/Beatmaps/Formats/LegacyDecoder.cs:168-184` 的 `Section` 枚举（官方到 `Mania` 为止）：

```csharp
public enum Section
{
    General, Editor, Metadata, Difficulty, Events, TimingPoints,
    Colours, HitObjects, Variables, Fonts, CatchTheBeat, Mania,
    BeatmapSectionGimmicks,      // ← fork 新增
    BeatmapHitObjectGimmicks,    // ← fork 新增
}
```

**格式（来自 `LegacyBeatmapDecoder.handleSectionGimmick` 与 `LegacyBeatmapEncoder.handleSectionGimmicks/handleHitObjectGimmicks` 的读写代码）：**

- `[BeatmapSectionGimmicks]` 每行：`{Id:int},{StartTime:double},{EndTime:double},{Key=Value|Key=Value...}`（EndTime < 0 表示无上限；`SectionGimmickSection.Contains`：`time >= StartTime && (EndTime < 0 || time <= EndTime)`）。
- `[BeatmapHitObjectGimmicks]` 每行：`{StartTime:double},{ComboIndexWithOffsets:int},[ObjectId={long}|]{Key=Value|...}`（ObjectId 可选，用于绑定具体物件）。

测试用例 `delta/osu.Game.Tests/Beatmaps/Formats/SectionGimmickDecoderTest.cs:48` 的原文样例：

```
[BeatmapSectionGimmicks]
0,0,1500,EnableHPGimmick=True|NoDrain=True|HP300=-0.02|HP100=-0.05|HP50=0.08|HPMiss=-0.12|EnableNoMiss=True
```

**内存结构（纯 POCO，非 RealmObject，不进数据库）：**

`Beatmaps/SectionGimmicks/SectionGimmickSection.cs`：

```csharp
public class SectionGimmickSection
{
    public int Id { get; set; }
    public double StartTime { get; set; }
    public double EndTime { get; set; }
    public SectionGimmickSettings Settings { get; set; } = new();
    public bool Contains(double time) => time >= StartTime && (EndTime < 0 || time <= EndTime);
}
```

`Beatmaps/SectionGimmicks/SectionGimmickSettings.cs` 的字段（节选关键项，类型与默认值照抄）：

```csharp
public class SectionGimmickSettings
{
    // 生命值 gimmick
    public bool EnableHPGimmick { get; set; }
    public bool EnableNoMiss { get; set; }
    public bool EnableAccuracyRequirement { get; set; }
    public float RequiredAccuracy { get; set; } = float.NaN;
    public bool EnableCountLimits { get; set; }
    public bool EnableNoMissedSliderEnd { get; set; }
    public bool EnableGreatOffsetPenalty { get; set; }
    // 计数上限（-1 = 不限制）
    public int Max300s / Max100s / Max50s / MaxMisses { get; set; } = -1;   // int，默认 -1
    public bool Max300sAffectsSliderEndsAndTicks / ... { get; set; }        // 4 个联动开关
    // HP 数值（NaN = 不覆盖）
    public float HP300 / HP100 / HP50 / HPMiss / HPStart / HPCap { get; set; } = float.NaN;
    public bool HP300AffectsSliderEndsAndTicks / ... { get; set; }
    public bool NoDrain { get; set; }
    public bool ReverseHP { get; set; }
    public float GreatOffsetThresholdMs { get; set; } = -1;
    public float GreatOffsetPenaltyHP { get; set; } = float.NaN;
    // 分段难度覆盖
    public bool EnableDifficultyOverrides { get; set; }
    public bool AllowUnsafeDifficultyOverrideValues { get; set; }
    public bool DifficultyOverrideStartWithBeatmapValues { get; set; }
    public bool EnableGradualDifficultyChange { get; set; }
    public float GradualDifficultyChangeEndTimeMs { get; set; } = float.NaN;
    public bool KeepDifficultyOverridesAfterSection { get; set; }
    public float SectionCircleSize / SectionApproachRate / SectionOverallDifficulty / SectionStackLeniency { get; set; } = float.NaN;
    public double SectionTickRate { get; set; } = double.NaN;
    // 每项难度覆盖均带 *Window / *StartTimeMs / *EndTimeMs / EnableGradual*Change 窗口字段（略，模式同上）
    // 强制 mod
    public bool ForceHidden / ForceNoApproachCircle / ForceHardRock / ForceFlashlight { get; set; }
    public float FlashlightRadius { get; set; } = float.NaN;
    public bool EnableGradualFlashlightRadiusChange / EnableGradualFlashlightFadeIn { get; set; }
    public float GradualFlashlightRadiusEndTimeMs { get; set; } = float.NaN;
    public bool ForceDoubleTime / ForceSingleTap / ForceAlternate { get; set; }
    public bool ForceTransform / ForceWiggle / ForceSpinIn / ForceGrow / ForceDeflate { get; set; }
    public bool ForceBarrelRoll / ForceApproachDifferent / ForceMuted / ForceNoScope { get; set; }
    public bool ForceMagnetised / ForceRepel / ForceFreezeFrame / ForceBubbles / ForceSynesthesia { get; set; }
}
```

`Beatmaps/HitObjectGimmicks/HitObjectGimmickEntry.cs` 与 `HitObjectGimmickSettings.cs`（物件级，含假音符）：

```csharp
public class HitObjectGimmickEntry
{
    public long? ObjectId { get; set; }
    public double StartTime { get; set; }
    public int ComboIndexWithOffsets { get; set; }
    public HitObjectGimmickSettings Settings { get; set; } = new();
}

public enum FakePunishMode { None, Miss }

public class HitObjectGimmickSettings
{
    public const float DEFAULT_FAKE_REVEAL_STRENGTH = 0.55f;
    public const float DEFAULT_FAKE_REVEAL_LEAD_IN_START_MS = 900;
    public const float DEFAULT_FAKE_REVEAL_LEAD_IN_LENGTH_MS = 200;
    public const float DEFAULT_FAKE_REVEAL_FADE_OUT_START_MS = 300;
    public const float DEFAULT_FAKE_REVEAL_FADE_OUT_LENGTH_MS = 250;

    public bool IsFakeNote { get; set; }
    public FakePunishMode FakePunishMode { get; set; } = FakePunishMode.None;
    public bool FakePlayHitsound { get; set; }
    public bool FakeAutoHitOnApproachClose { get; set; }
    public bool FakeAutoHitPlayHitsound { get; set; }
    public bool FakeRevealEnabled { get; set; } = true;
    public float FakeRevealRed { get; set; } = 1f;
    public float FakeRevealGreen { get; set; } = 0.3019608f;
    public float FakeRevealBlue { get; set; } = 0.3019608f;
    public float FakeRevealStrength { get; set; } = DEFAULT_FAKE_REVEAL_STRENGTH;
    public float FakeRevealLeadInStartMs { get; set; } = DEFAULT_FAKE_REVEAL_LEAD_IN_START_MS;
    // ... 以及与 SectionGimmickSettings 同名的 HP/计数/难度覆盖/强制 mod 字段（子集）
    public bool EnableHPGimmick / EnableNoMiss / EnableCountLimits / EnableGreatOffsetPenalty { get; set; }
    public int Max300s / Max100s / Max50s / MaxMisses { get; set; } = -1;
    public float HP300 / HP100 / HP50 / HPMiss / GreatOffsetPenaltyHP / FlashlightRadius { get; set; } = float.NaN;
    public bool EnableDifficultyOverrides / AllowUnsafeDifficultyOverrideValues { get; set; }
    public float SectionCircleSize / SectionApproachRate / SectionOverallDifficulty / SectionStackLeniency { get; set; } = float.NaN;
    public double SectionTickRate { get; set; } = double.NaN;
    public bool ForceHidden / ForceNoApproachCircle / ForceHardRock / ForceFlashlight / ForceTraceable { get; set; }
}
```

挂载点：`Beatmap.cs`/`IBeatmap.cs` 新增 `BeatmapSectionGimmicks SectionGimmicks` 与 `BeatmapHitObjectGimmicks HitObjectGimmicks` 属性；`Rulesets/Difficulty/DifficultyCalculator.cs` 的 `IBeatmap` 适配器透传这两个属性；osu! 规则集 `OsuRuleset.cs` 将 `CreateHealthProcessor` 替换为 `new SectionGimmickHealthProcessor(drainStartTime)`；`OsuBeatmapProcessor.PostProcess()` 中调用 `SynchroniseEntriesWithHitObjects`、`convertFakeNotes`、`applySectionDifficultyOverrides`、`applySectionForcedMods`。编辑器侧有完整支持（`Screens/Edit/Compose/SectionGimmickEditorModel.cs`、Timeline 展示件、`Rulesets/Edit/Checks/CheckSectionGimmicks.cs` 校验）。

**文件头版本号不变**：两边 encoder 均写 `osu file format v{FIRST_LAZER_VERSION}`，`FIRST_LAZER_VERSION = 128`（delta 与官方常量值相同）。

### 5.2 官方客户端对 delta 谱面的容忍度

官方 `lazer/osu.Game/Beatmaps/Formats/LegacyDecoder.cs:62-63`：

```csharp
if (!Enum.TryParse(line[1..^1], out section))
    Logger.Log($"Unknown section \"{line}\" in \"{output}\"");
```

即官方遇到 `[BeatmapSectionGimmicks]` 只记日志跳过，**不报错**；但官方编辑器重新保存该谱面时，encoder 不认识这两个 section，**gimmick 数据会被静默丢弃**。

### 5.3 判定扩展接口

`delta/osu.Game/Rulesets/Judgements/ICustomJudgementResultValidator.cs`（fork 新文件）：

```csharp
/// <summary>
/// Allows a judgement to accept additional result types beyond its min/max pair.
/// </summary>
public interface ICustomJudgementResultValidator
{
    bool IsResultAllowed(HitResult result);
}
```

唯一使用点：`DrawableHitObject.cs:739`，允许假音符等 judgement 接受额外的 HitResult 类型。**不新增 HitResult 枚举值**（枚举两边一致）。

### 5.4 其余 fork 新文件/改动

- 菜单光标自定义：`Graphics/Cursor/CursorTrail.cs`（新）、`MenuCursorContainer.cs` 重写、§3.1 的 6 个设置。
- IPC WebSocket：delta 把官方位于 `osu.Game/IPC/Messages` 的消息类挪到 `osu.Desktop/IPC/Messages`，并新增 `HitCountMessage.cs`：

```csharp
public class HitCountMessage : OsuWebSocketMessage
{
    [JsonProperty("new_hits")]
    public long NewHits { get; init; }
}
```

- 资源包：`osu.Game.csproj` 引用 `delta.Game.Resources.Overrides`（版本 `2026.4.10-A`），`OsuGameBase` 构造时 `Resources.AddStore(new DllResourceStore(delta.Game.Resources.Overrides.DeltaResources 的程序集))`，官方资源作为缺省回退。对应仓库 `deltalazer/delta-resources`。
- osu! 规则集新增假音符对象：`osu.Game.Rulesets.Osu/Objects/FakeHitCircle.cs`、`Objects/Drawables/DrawableFakeHitCircle.cs`、`Scoring/SectionGimmickCountTracker.cs`。
- README 另宣称「(osu!standard only) Object Aware Star Rating and PP for Forced Mods」「Extended Slider Velocity Cap of 1000x」——PP/难度计算器文件的 diff 均为基线演进痕迹，未在本次 diff 中定位到明确的独立实现文件（可能在 OsuBeatmapProcessor 的 600+ 行 fork 改动内，如 `applySectionForcedMods`；**具体算法未逐行核实，标注：未确认**）。

### 5.5 delta 缺失的官方能力（基线落后所致）

delta 中不存在（官方有）的代表性文件：`Models/RealmOnlineAsset.cs`、`Graphics/OnlineAssetCachingStore.cs`、`Screens/RankingV2/`、`Online/API/Requests/UploadScreenshot.cs`、`Scoring/IConfiguredMod.cs`、`Overlays/Chat/WatchCommand.cs`、编辑器快捷键绑定（`GlobalActionContainer` 中 EditorSelectTool 等 13 个键位）、`Screenshot 上传（Shift+F12）`、设置项见 §3.2。

## 6. files/ 目录布局（问题 6）

- 运行时谱面/文件存储：两边 `osu.Game/Database/RealmFileStore.cs` **逐字节一致**，均为 `Storage = storage.GetStorageForDirectory(@"files");`（RealmFileStore.cs:33），文件按 RealmFile 哈希两级目录存放（`files/<hash前缀>/<hash>`，与 stable 兼容的布局）。`Database/LegacyExporter.cs:52` 两边同样引用 `@"files"` 目录。
- 结论：**两客户端数据目录内部布局完全一致，`files/` 可直接整目录复制或（硬链接）共享**；唯一注意点是两边 client.realm 各自记录 RealmFile 行（Name/Hash），复制 files 后还需处理数据库侧的索引一致性（同步工具本来的课题）。

## 7. 对同步工具的启示（问题 7）

1. **Realm 侧无需字段映射/丢弃**：delta(v51) 与官方(v52) 的全部业务表字段级一致。官方→delta 方向，唯一需要处理的是官方库里的 `RealmOnlineAsset` 记录（delta 无此表）——它只是在线资源缓存，**直接整表丢弃无损失**；反向（delta→官方）甚至无需转换，官方客户端打开 v51 库会自动迁移。若同步工具直接写库，写入方应以其客户端的 schema 版本为准。
2. **`.osu` 文件是 fork 数据的唯一载体**：delta 谱面可能含 `[BeatmapSectionGimmicks]` / `[BeatmapHitObjectGimmicks]` 两个 section。规则：
   - 官方客户端**读**delta 谱面：安全（未知 section 仅记日志跳过）。
   - 官方客户端**编辑后保存**：gimmick section 静默丢失。同步工具若做双向同步，把 delta 谱面同步给官方后再回传，会永久丢失 gimmick 数据——**同步工具应以 delta 侧的 .osu 文件为权威源，或至少检测这两个 section 头的存在并在将被官方覆盖时告警/备份**。
   - 哈希问题：.osu 内容差异意味着同一 OnlineID 的谱面在两边会有不同文件哈希（RealmFile.Hash），files/ 不能按哈希盲等价。
3. **配置（framework.ini / OsuSetting）**：两边枚举互有对方不认识的键。delta 新增 6 键（`MenuCursorFromSkin` 等）官方不识别；官方新增 3 键（`SongSelectCollectionFilter`、`HitsoundVolume`、`PMFriendsOnly`）delta 不识别。INI 键值对加载时未知键会被忽略，不会崩溃；同步工具应**白名单拷贝共有键**，或显式丢弃上述 9 个互斥键。
4. **成绩**：delta 成绩全部为本地成绩（无 OnlineID、无法登录），结构同官方本地成绩。gimmick 谱面的成绩在官方端没有对应 gameplay 语义（HP/假音符等），**同步到官方后仅作历史记录展示，无法重放验证其判定**（回放帧本身是标准格式，但官方没有 SectionGimmickHealthProcessor / fake note 逻辑，重放结果会不一致）。
5. **共存**：数据目录 `deltalazer` vs `osu` 天然隔离；`client.realm` 文件名相同但目录不同；stable 探测逻辑一致（都指向同一 stable 安装），互不冲突。
6. **版本对齐**：delta 基线为官方 2026 年 7-8 月状态。同步工具若依赖官方 10 月新特性（如 `RealmOnlineAsset` 缓存、`HitsoundVolume`），在 delta 侧不可用；文档中所有「官方新增」条目都以官方 `2026.1019.0` 参考仓库为准。

## 附：本次实际执行的核对命令（供复核）

- `diff -rq lazer/osu.Game delta/osu.Game`（405 行差异清单，39 个 delta 独有文件 / 34 个官方独有文件 / 349 个内容不同文件）
- 逐文件 `diff -u`：`Program.cs`、`OsuGameBase.cs`、`OsuGameDesktop.cs`、`IO/OsuStorage.cs`、`IO/StableStorage.cs`、`Database/RealmAccess.cs`、`Database/RealmFileStore.cs`、`Configuration/OsuConfigManager.cs`、`Overlays/Login/LoginForm.cs`、`Online/ProductionEndpointConfiguration.cs`、`Online/API/APIAccess.cs`、`Scoring/ScoreInfo.cs`、`Screens/Play/SubmittingPlayer.cs`、`Beatmaps/Formats/LegacyBeatmapDecoder.cs`、`Beatmaps/Formats/LegacyBeatmapEncoder.cs`、`Input/Bindings/GlobalActionContainer.cs` 及 §2.3 列出的全部 Realm 模型类。
- `grep -rl ": RealmObject"` 两边全量比对（21 vs 20 个类）。
- git 元数据：两边均为 shallow clone（各仅 1 个 commit），delta remote = `https://github.com/deltalazer/delta`，无提交历史可比对。
