# g0v0（osu!lazer fork）与官方 lazer 差异调研

> 调研对象：`reference/g0v0`（GooGuTeam 维护的 osu!lazer fork，对外名称 **g0v0!**，对接 g0v0-server）与 `reference/lazer`（ppy 官方 master）。
> 本文档自包含：所有关键事实均摘录源码原文（类名、字段、枚举值、默认值、路径常量），不依赖 reference/ 目录存在。
> 调研日期：2026-10-24。

## 0. 调研方法与基线

- 两侧快照：`reference/lazer` 为官方 master，工作树 HEAD `c752ffb`（提交时间 2026-10-24，"Minor naming changes"）；`reference/g0v0` 为 **depth=1 shallow clone**（HEAD `691c28e`，2026-10-22，"Use monospace fonts for Markdown code blocks and inline code"），无可用 git 历史。
- 两侧 `global.json` 完全一致（`sdk.version = 10.0.100, rollForward: latestFeature`）。
- 因为 g0v0 无法提供 merge-base，直接 `diff -rq` 会混入「上游在 g0v0 基点之后、10-24 之前的演进」噪音（例如官方重构了开场流程：lazer 已无 `Screens/Menu/Disclaimer.cs`，新增 `Configuration/IntroSequence.cs`、`Screens/Menu/IntroTriangles.cs` 等 9 个文件，而 g0v0 仍是旧结构）。因此本调研以 **fork 指纹法** 为主：g0v0 的版权头规范（其 AGENTS.md 规定改过的文件第二行版权署名加 `& GooGuTeam`）使 `grep -rl GooGuTeam` 能可靠圈出 fork 侧改动/新增的文件——共 208 个（含 18 个测试文件）。
- 逐文件内容 diff 采用 `diff --strip-trailing-cr`，并剔除版权头行后判定实质差异。

## 1. 身份与数据目录

### 1.1 游戏名与程序集名

`osu.Desktop/Program.cs`（g0v0，fork 改动）：

```csharp
private const string base_game_name = @"g0v0-development";   // DEBUG 构建下（官方为 "osu-development"）
private const string base_game_name = @"g0v0";               // release 构建下（官方为 "osu"）
```

`osu.Desktop/osu.Desktop.csproj`：`<AssemblyName>g0v0!</AssemblyName>`（官方为 `<AssemblyName>osu!</AssemblyName>`）。`ApplicationIcon` 两侧同为 `lazer.ico`。

`osu.Game/OsuGameBase.cs`（g0v0）：

```csharp
public const string GAME_NAME = "g0v0! (development)";   // DEBUG
public const string GAME_NAME = "g0v0!";                 // release（官方为 "osu!"）
public const string CLIENT_DATABASE_FILENAME = @"client.realm";   // 与官方完全相同
```

版本字符串构造也带 fork 后缀：`$@"{version.Major}.{version.Minor}.{version.Build}-g0v0"`。

### 1.2 数据目录与共存性

- `osu.Game/IO/OsuStorage.cs` 与整个 `osu.Game/IO/` 目录 **零实质差异**（0 个文件带 GooGuTeam 指纹），即数据目录内部布局逻辑完全沿用上游，由框架按 game name（`g0v0`）派生根数据目录。
- 结论：**同一台机器上与官方版完全隔离、可共存**。桌面端默认数据目录为名称 `g0v0` 而非 `osu`（Linux `~/.local/share/g0v0`、Windows `%APPDATA%\g0v0`、macOS `~/Library/Application Support/g0v0`——路径形态由 osu-framework 按 game name 决定，本仓库内未见覆盖此行为的代码）。
- 版本/更新通道：`ReleaseStream` 枚举 fork 化——官方 `Lazer` / `Tachyon (Unstable)`，g0v0 为 `[Description("g0v0")] G0V0` 与 `[Description("robin (Unstable)")] Robin`；默认值 `SetDefault(OsuSetting.ReleaseStream, ReleaseStream.G0V0)`（官方默认 `ReleaseStream.Lazer`）。
- 更新源：桌面 Velopack `GithubSource("https://github.com/GooGuTeam/g0v0", ...)`（官方 `ppy/osu`）；移动端 `NoActionUpdateManager` 查询 `https://api.github.com/repos/GooGuTeam/g0v0/releases`。新增设置 `DisableAutomaticUpdates`（默认 false）可彻底关闭自动更新（`osu.Desktop/Updater/VelopackUpdateManager.cs` 检查 `config.Get<bool>(OsuSetting.DisableAutomaticUpdates)`）。

### 1.3 服务器端点

`osu.Game/Online/ProductionEndpointConfiguration.cs`（g0v0）：

```csharp
WebsiteUrl  = @"https://lazer.g0v0.top";
APIUrl      = @"https://lazer-api.g0v0.top";
APIClientSecret = @"FGc9GAtyHzeQDshWP5Ah7dega8hJACAJpQtw6OXk";
APIClientID = "5";
SpectatorUrl    = @"https://lazer-api.g0v0.top/signalr/spectator";
MultiplayerUrl  = @"https://lazer-api.g0v0.top/signalr/multiplayer";
MetadataUrl     = @"https://lazer-api.g0v0.top/signalr/metadata";
BeatmapSubmissionServiceUrl = @"https://lazer-api.g0v0.top/beatmap-submission";
```

（官方为 `osu.ppy.sh` / `spectator.osu.ppy.sh` / `bss.ppy.sh`，ClientID 同为 "5" 但 Secret 不同。）

自定义服务器：新增设置 `CustomApiUrl`（默认 `string.Empty`）。`OsuGameBase.CreateEndpoints()` 在该值非空时直接 `config.APIUrl = customUrl;` 返回；设置 UI 在 `Overlays/Settings/Sections/Online/ContentDownloadSettings.cs`（FormTextBox，占位文本为当前 `api.Endpoints.APIUrl`，带 URL 格式校验）。`Online/TrustedDomainOnlineStore.cs`（上游已有该类，fork 修改）在 `CustomApiUrl` 非空时放行任意在线资源域名，否则只放行 `.ppy.sh` 与 `.g0v0.top` 结尾的域名（上游仅 `.ppy.sh`）。

## 2. Realm schema 差异

**结论：g0v0 未对 Realm schema 做任何结构性改动。**

- schema 版本：两侧 `osu.Game/Database/RealmAccess.cs` 均为 `private const int schema_version = 52;`，该文件除品牌字符串（日志里的 osu!→g0v0!）外无差异。
- `osu.Game/Models/` 全部文件 diff 后仅版权头差异；全部 20 个 RealmObject 相关文件（含散布在 `Beatmaps/`、`Scoring/`、`Skinning/`、`Rulesets/` 等目录的定义）两侧清单与内容一致。
- 无新增/删除的表、字段、索引；`BeatmapOnlineStatus` 枚举无差异；无 `Migrations/` 目录（两侧都没有，Realm 迁移由 `RealmAccess` 的备份回滚机制处理）。

唯一碰过 Realm 相关定义的 fork 改动是 `osu.Game/Rulesets/RulesetInfo.cs`，但**只新增了常量与非持久化方法**，未动任何 `[Realm]` 持久化字段：

```csharp
public const string OSU_MODE_SHORTNAME = "osu";
public const string TAIKO_MODE_SHORTNAME = "taiko";
public const string CATCH_MODE_SHORTNAME = "fruits";
// https://github.com/GooGuTeam/g0v0-server/blob/main/README.en.md#supported-rulesets
public const string OSU_RELAX_MODE_SHORTNAME = "osurx";
public const string OSU_AUTOPILOT_MODE_SHORTNAME = "osuap";
public const string TAIKO_RELAX_MODE_SHORTNAME = "taikorx";
public const string CATCH_RELAX_MODE_SHORTNAME = "fruitsrx";

public const int OSU_RELAX_ONLINE_ID = 4;
public const int OSU_AUTOPILOT_ONLINE_ID = 5;
public const int TAIKO_RELAX_ONLINE_ID = 6;
public const int CATCH_RELAX_ONLINE_ID = 7;

public RulesetInfo CreateSpecialRuleset(string newShortName, int onlineId) { /* Clone() 后改 OnlineID/ShortName/Name，内存对象 */ }
public RulesetInfo? CreateSpecialRulesetByScore(ScoreInfo score) { /* 按 score.Mods 含 ModRelax 或 APIMods 含 "AP" 映射，见 §5 */ }
public RulesetInfo CreateNormalRuleset() { /* 反向：去后缀恢复基础规则集 OnlineID 0/1/2 */ }
```

`Scoring/ScoreInfo.cs` 仅有版权头差异（`PP`、`Ranked` 等字段均为上游原有）。`Skinning/SkinInfo.cs` 的唯一差异是上游新近新增的 `internal static readonly Guid RETRO_SKIN = ...`（g0v0 基点尚无，属上游演进，且为非持久化常量）。

### 2.1 规则集管理走 rulesets.json 而非 Realm（fork 特有文件，不影响 schema）

`osu.Game/Rulesets/Configuration/RulesetManagementConfig.cs`（g0v0 新增，纯 GooGuTeam 文件）：

```csharp
[Serializable]
public class RulesetManagementConfig
{
    public BindableBool BlockUnseenRulesets = new BindableBool();  // 新装规则集默认不启用，需用户显式开启
    public List<RulesetInfo> KnownRulesets { get; } = [];          // 受信任（曾启用过）
    public List<RulesetInfo> DisabledRulesets { get; } = [];       // 用户禁用
    public List<string> BrokenRulesetFilenames { get; } = [];      // 加载即抛异常的 dll
    public string? RulesetCausedCrashLastTime { get; set; }        // 上次崩溃元凶
    public bool IsTrusted(RulesetInfo r) => !BlockUnseenRulesets.Value || KnownRulesets.Contains(r) || r.IsBuiltinRuleset();
    public bool ShouldBeLoaded(RulesetInfo r) => IsTrusted(r) && !DisabledRulesets.Contains(r);
}
```

序列化为 `rulesets.json`（`RulesetStore` 内 `private const string config_filename = @"rulesets.json";`，存于 `rulesets/` 子目录的 storage）。`RulesetStore`/`RealmRulesetStore` 的 fork 改动是用 `Config.ShouldBeLoaded(detached)` 过滤——**被禁用/不受信任的规则集不进入 Realm 的 AvailableRulesets**（上游是发现的都 upsert 进库）。

自定义（在线安装）规则集的 OnlineID 约定见 `osu.Game/Extensions/ModelExtensions.cs`（fork 改动）：

```csharp
public static bool IsSpecialRuleset(this IRulesetInfo ruleset) => ruleset.ShortName is RulesetInfo.OSU_RELAX_MODE_SHORTNAME or ...;
public static bool HasSpecialRuleset(this IRulesetInfo ruleset) => ruleset.ShortName is RulesetInfo.OSU_MODE_SHORTNAME or RulesetInfo.TAIKO_MODE_SHORTNAME or RulesetInfo.CATCH_MODE_SHORTNAME;
/// <remarks>g0v0-server numbers custom rulesets from 10.</remarks>
public static bool IsBuiltinRuleset(this IRulesetInfo ruleset) => IsLegacyRuleset(ruleset) && ruleset.OnlineID < 10;
```

即：OnlineID 0–3 内置四模式，4–7 fork 特殊规则集（不落库，见 §5），**≥10 为 g0v0-server 在线规则集市场的第三方规则集**（落库进 RulesetInfo 表）。

## 3. 设置差异（OsuConfigManager）

`osu.Game/Configuration/OsuConfigManager.cs` 中 fork 新增（`SetDefault` 与枚举 `OsuSetting` 双侧对照，官方均无）：

| 枚举值 | 默认值 | 用途 |
|---|---|---|
| `UseRulesetSkins` | `false` | 按规则集自动切换皮肤（映射存 `RulesetSkins`，JSON 字符串，键为规则集短名） |
| `RulesetSkins` | `"{}"` | 规则集→皮肤名映射 |
| `UseV2ResultsScreen` | `false` | 单人成绩界面用 fork 的 V2 全宽布局 |
| `ToriiLegacyFooterUseSkin` | `false` | Torii legacy 界面（端口自 ShikkesoraSIM/torii-osu，见 `docs/torii-legacy-port.md`） |
| `ToriiLegacySongSelectFooter` | `false` | 同上 |
| `ToriiStableResults` | `false` | 同上 |
| `ToriiLegacyFont` | `false` | 同上 |
| `UnslantedSongSelectUI` | `false` | 取消 UI 斜切（`OsuGame.SHEAR` 从 `DEFAULT_SHEAR = (0.2f, 0)` 置零） |
| `CustomApiUrl` | `string.Empty` | 自定义 API 服务器 URL |
| `DisableAutomaticUpdates` | `false` | 关闭自动更新（"GU specific settings"） |
| `HitsoundVolume` | `0.6`（范围 0–1，步进 0.01） | 打击音独立音量 |

默认值改动：`SetDefault(OsuSetting.ReleaseStream, ReleaseStream.G0V0)`（官方 `ReleaseStream.Lazer`）。

官方较 g0v0 基点多出的 `IntroSequence` 设置（`SetDefault(OsuSetting.IntroSequence, IntroSequence.Triangles)`）为上游演进，非 fork 删除。

## 4. files/ 目录布局与共存

- `osu.Game/IO/` 全目录（`OsuStorage.cs`、`MigratableStorage.cs`、`StableStorage.cs`、`FileInfo.cs` 等）**零 fork 改动**；`RealmFile.cs`（哈希寻址的文件存储模型）也无差异。
- 数据库文件名同为 `client.realm`；日志目录 `logs/`、规则集目录 `rulesets/`（`storage?.GetStorageForDirectory(@"rulesets")`）等子目录命名全部同上游。
- **结论：`files/` 目录（谱面音频/图片等按内容哈希存放的文件池）布局与官方完全一致，可直接共享或复制**。前提是目标库的 `RealmFile`/`BeatmapSetInfo` 表里有对应记录（osu!lazer 的 files 是纯哈希寻址、无元数据副作用，共享目录本体是安全的）。

## 5. 谱面状态语义与成绩上传机制

### 5.1 谱面在线状态

- `BeatmapOnlineStatus` 枚举与 `Beatmaps/` 目录（`BeatmapManager.cs`、`BeatmapSetInfo.cs` 等）**均无 fork 改动**；客户端侧没有强制 approval、绕过在线状态检查的代码。谱面状态语义的差异（若有）在 g0v0-server 侧，本仓库无法确认——**未确认**。
- 代码中可见的 approval 引用全部是上游原有内容（stable 库 SQL 查询 `approved in (1, 2, 4)`、`BeatmapApproval` 最近活动枚举等）。

### 5.2 成绩上传链路（fork 改动集中在三处）

1. **ruleset_hash 证明**（防改 dll 的非对称规则集校验）：
   - `osu.Game/Rulesets/RulesetHashCache.cs`（新增）：对 `store.AvailableRulesets` 中 `OnlineID >= 0 && OnlineID <= 3` 的跳过，其余（即自定义规则集）对 dll 文件算 MD5：`RulesetsHashes[instance.ShortName] = str.ComputeMD5Hash()`。
   - `Online/Solo/CreateSoloScoreRequest.cs`：构造函数追加 `string? rulesetHash = ""`；请求时 `if (!string.IsNullOrEmpty(rulesetHash)) req.AddParameter("ruleset_hash", rulesetHash);`（作为查询参数发给服务器）。
   - `Online/Rooms/CreateRoomScoreRequest.cs`：同样追加 rulesetHash。
   - `Online/HubClientConnector.cs`：SignalR 连接头 `options.Headers.Add(RULESET_HASH_HEADER /* "X-Osu-Ruleset-Hashes" */, JsonConvert.SerializeObject(rulesetHashCache.RulesetsHashes))`。
   - `APIAccess` 构造函数注入 `RulesetHashCache`（`APIAccess(this, LocalConfig, endpoints, VersionHash, RulesetHashCache)`）。
2. **PP 回填**：`Screens/Play/SubmittingPlayer.cs` 提交成功回调里新增 `score.ScoreInfo.PP = s.PP ?? score.ScoreInfo.PP;`（官方不回填）。`SoloScoreInfo.PP`（`public double? PP`，JSON `pp`）是上游已有字段。
3. **赛前 PB 预取**：`Screens/Play/SoloPlayer.cs` 在 `StartGameplay()` 中（登录、`BeatmapInfo.OnlineID > 0`、`Ruleset.Value.IsLegacyRuleset()` 时）发出 fork 新增的 `GetBeatmapUserScoresRequest`（`Target = $"beatmaps/{beatmapId}/scores/users/{userId}/all"`，`mode` 参数取规则集短名——特殊规则集时为 `osurx`/`osuap`/`taikorx`/`fruitsrx`），取 PP 最高的合格成绩 `OrderByDescending(s => s.PP).FirstOrDefault()` 作为成绩界面对比分 `ComparisonScore`。

### 5.3 Relax/Autopilot 特殊规则集（成绩统计语义）

- `ModRelax`/`OsuModRelax`/`OsuModAutopilot` 上游本就存在且 fork 未改其可用性（仅描述文案本地化）；fork 新增的是 **RX 专用难度计算**（`osu.Game.Rulesets.Osu/Difficulty/Skills/Relax.cs`、`Evaluators/RelaxAimEvaluator.cs`、`RelaxRhythmEvaluator.cs`，g0v0 独有文件）。
- 关键机制：**特殊规则集是「展示/统计层」概念，不持久化**。玩家以 RX/AP 游玩时：
  - 本地成绩的 `ScoreInfo.RulesetID` 仍是基础模式（0–3），RX/AP 体现在 mods 列表（提交 payload 用 `APIMods`，`Acronym == "AP"` 的判定见 `CreateSpecialRulesetByScore`）；
  - 需要按特殊规则集聚合时（用户统计 `Online/LocalUserStatisticsProvider.cs`、成绩对比、排行榜 mode 参数），用 `CreateSpecialRulesetByScore(score)` 在**内存中 Clone 出** `RulesetInfo { ShortName = "osurx", OnlineID = 4 }` 之类的临时对象发请求，不写回 Realm 的 RulesetInfo 表。
- 服务器侧把 RX/AP 成绩归入独立规则集排行，是 g0v0-server 的行为——**未确认**（客户端只发 mode 短名与 mods）。

## 6. fork 特有能力清单

| 能力 | 关键代码与事实 |
|---|---|
| **特殊规则集 RX/AP** | 见 §5.3；短名 `osurx`/`osuap`/`taikorx`/`fruitsrx`，OnlineID 4–7 |
| **在线规则集市场** | 私有 API 路由 `private/gamemodes`（`PrivateAPIRequest` 基类 `Route => @"private"`，g0v0 新增）：`SupportedRulesetsRequest` → `APIRuleset { id, name, readable, is_official, is_custom_ruleset }`；`SupportedRulesetVersionsRequest` → `APIRulesetVersion { ..., latest_version, versions: Dictionary<string,string>, download_url }`。UI 在 `Settings/Sections/RulesetGeneric/OnlineRulesetsSection.cs`，下载 dll（`FileWebRequest`）后 `RulesetStore.TryInstallRulesetPackage()`（校验 PE 头 `MZ`/`IMAGE_FILE_DLL` 位）装入 `rulesets/` 目录，重启生效 |
| **本地奖章系统** | `osu.Game/Medals/AwarderContainer.cs` 反射发现所有 `IMedalAwarder`（接口：`MedalId`/`Enabled`/`CheckMedalCriteria(OsuGameBase)`，常量 `G0V0_ACHIEVEMENTS_ID_START = 100_000`）本地判定后调 `UnlockMedalRequest`（`PUT me/achievements/{MedalId}`）。内置奖章例：`Medals/Awarders/{CycloneAwarder, CourierCatapultAwarder, G0V0FirstAnniversaryAwarder, HamsterWheelAwarder, HotshotAwarder}.cs`。奖章图源：`Users/Medal.cs` 中 `InternalName.StartsWith("g0v0_") ? "https://lazer-data.g0v0.top/medals/{InternalName}@2x.png" : "https://s.ppy.sh/images/medals-client/{InternalName}@2x.png"` |
| **多人胜利条件** | `Online/Multiplayer/WinCondition.cs` 新枚举 `Score / Accuracy / Combo / Pp`；API DTO `Online/Rooms/PlaylistItem.cs` 新增 `[JsonProperty("win_condition")] public WinCondition? WinCondition`（非 Realm 字段，仅联机协议） |
| **ModScoreV1 (SV1)** | `Rulesets/Mods/ModScoreV1.cs`：`Acronym => "SV1"`，`ModType.System`，`UserPlayable = false`，`IncompatibleMods = [ModScoreV2]`；各规则集各有子类（如 `osu.Game.Rulesets.Osu/Mods/OsuModScoreV1.cs`）。标记 legacy ScoreV1 计分的系统 mod |
| **V2 成绩界面** | `Screens/Ranking/V2*`（`V2ResultsPanel`、`V2AccuracyRing`、`V2PerformanceChange`、`V2RankEmblem` 等约 10 个新文件）+ `Scoring/FullComboScoreInfo.cs` / `PerfectScoreInfo.cs`（不改原始成绩、基于 Statistics 反推 FC/理论完美的估算展示） |
| **Torii legacy 界面** | 端口自 `ShikkesoraSIM/torii-osu`（MIT）：stable 风格选歌、成绩、底栏；设置键 `Torii*` 四项（§3）。回放渲染与成绩注功能已于 2026-10-12 从 fork 移除 |
| **服务器信息通知** | `Overlays/Notifications/ServerInfoNotification.cs`（启动时 `OsuGame.cs` 内 `Notifications.Post(new ServerInfoNotification(currentServerUrl))`），显示当前连接的服务器 |
| **规则集皮肤** | §3 `UseRulesetSkins`/`RulesetSkins`；`OsuGame.cs` 在规则集切换时 `SkinManager.SetSkinFromConfiguration(...)` |
| **打击音独立音量** | §3 `HitsoundVolume`（`Overlays/VolumeOverlay.cs`、`Rulesets/UI/DrawableRuleset.cs`） |

## 7. 对同步工具的启示

1. **Realm 库结构层面无障碍**：两侧 schema 同为版本 52、模型逐字段一致，g0v0 的 `client.realm` 理论上可被官方 lazer 直接打开（未实际运行验证）。无需做表/列转换。
2. **必须处理的数据语义差异**：
   - `ScoreInfo.RulesetID` 可能指向 g0v0 特有的自定义规则集行（`RulesetInfo` 表中 OnlineID ≥ 10 的行，来自 g0v0-server 规则集市场）。同步到官方库时这些 RulesetInfo 行不存在，成绩会成为孤儿或需降级处理（RX/AP 本地成绩 RulesetID 是 0–3、mods 带 RX/AP，官方服务器不认这类成绩，但本地库层面可读）。
   - `ScoreInfo.PP`：官方服务器不回填 PP（提交成功回调不写此字段），g0v0 会把服务器返回的实时 PP 写进本地成绩。同步时 g0v0 侧的 PP 值对官方侧是有意义的差异（官方侧该列常为 null，由官方服务器另行计算）。
   - `OsuSetting` 新增键（§3 表格）：同步 `framework` 设置/`game.ini` 类文件时这些键在官方侧是未知值；`ReleaseStream` 的枚举名 `G0V0`/`Robin` 与官方 `Lazer`/`Tachyon` 不通用。
3. **可以直接复制的资产**：`files/` 哈希池目录（§4）可整体共享/复制，无 fork 语义；`screenshots/`、`skins/` 同理（SkinInfo 无差异）。`rulesets/` 目录里的 dll 与 `rulesets.json` 是 g0v0 特有（官方读 `rulesets/` 但没有 json 管理层，dll 本身两边都能加载——但 g0v0-server 市场安装的规则集 OnlineID ≥10 是服务器侧约定，官方环境下无意义）。
4. **不需要担心的**：谱面（BeatmapSetInfo/BeatmapInfo/BeatmapMetadata）、皮肤（SkinInfo）、收藏（BeatmapCollection）、按键绑定（RealmKeyBinding）、ModPreset、RealmRulesetSetting 的持久化结构两侧完全一致，可按官方语义直接同步。
5. **在线身份不互通**：账号体系独立（OAuth ClientSecret/服务器不同），两边的 `OnlineID`（用户/谱面/成绩）分属不同命名空间，同步时凡 `OnlineID`/`has_replay` 等在线字段都应视为服务器本地标识，不可跨侧对拷（复制谱面本地文件没问题，但 BeatmapSetInfo.OnlineID 在官方侧指向不同谱面）。

## 8. 未确认事项

- g0v0-server 侧的谱面 approval/在线状态语义（是否放宽、是否接受任意状态谱面的成绩）——仅能确认客户端未做任何绕过，服务器行为需查 g0v0-server 仓库。
- RX/AP 成绩在服务器侧的排行归属细节（客户端只发 `mode=osurx` 等短名与 mods，服务端如何聚合未验证）。
- g0v0 的 `client.realm` 被官方 lazer 实际打开的兼容性（仅静态比对 schema 52 与模型一致，未运行验证；且 g0v0 侧若装有 OnlineID ≥10 的自定义规则集，官方侧打开时成绩引用的行为未测）。
- lazer 侧 2026-10-22 基点到 10-24 之间的上游演进是否波及上述结论（已通过指纹法排除 fork 改动面，但上游两天内的零星改动可能混入少量「g0v0 基点较旧」的误报，例如 `SkinInfo.RETRO_SKIN`、`IntroSequence` 均已在上文标注为上游演进）。

## 附：调研中执行的命令

- 差异面统计：`diff -rq lazer/osu.Game g0v0/osu.Game`（2793 行原始输出，多为版权头/上游演进噪音）
- fork 指纹：`grep -rl "GooGuTeam" --include="*.cs" .`（208 个文件，清单存 `/tmp/g0v0-fingerprint-files.txt`）
- Realm 模型核对：`grep -rln ": RealmObject" osu.Game/`（两侧各 20 个文件，逐一 `diff --strip-trailing-cr`）
- schema 版本：`grep -n "schema_version" osu.Game/Database/RealmAccess.cs`（两侧均 52）
- 独有文件：`comm -23 <(lazer/osu.Game 下 find *.cs) <(g0v0/osu.Game 下 find *.cs)`（lazer 独有 9 个，均上游演进）
