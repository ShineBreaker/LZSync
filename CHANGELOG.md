# Changelog

本文件记录 LZSync 的全部版本变更，格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

## [1.1.1] - 2026-11-08

Windows 端安全与体验修复批次：写前两道占用防线（进程守卫 + 占用拒写）
在 Windows 全部失效的缺陷修复，CI 三平台补齐测试门禁，扫描零结果反馈。

### 修复

- **进程守卫 Windows 漏检**（#2）：`matchProcess` 把 `.exe` 后缀追加到
  tasklist 枚举名而非名单名（`osu!.exe` + `.exe` 永远匹配不上按程序集
  名登记的名单），官方 lazer / Ez2Lazer / g0v0 在 Windows 检测不到，
  游戏运行时守卫仍放行。改为把后缀加到名单名上对齐枚举名。
- **占用拒写（S5）Windows 未实现**（#3）：`lock_windows.go` 的占用探针
  恒返回 nil，目标库被其他进程持有写会话时 Deploy 不拒绝反而写入。
  落地命名互斥体试锁探测：realm-core 在 Windows 的跨进程写互斥是命名
  互斥体（`Local\realm_named_intermutex_…`，写事务期间由
  `DB::do_begin_write` 持有至 commit/rollback），试拿同名互斥体 0ms
  超时即判 `ErrTargetOccupied`——语义与 Unix flock 探测对齐，拦住真正
  的写冲突。
- **文档大小写冲突**（#4）：git 索引同时存在 `docs/DESIGN.md` 与
  `docs/design.md`，Windows/macOS 大小写不敏感文件系统上克隆即告警、
  工作区缺文件、`git status` 幽灵改动。UI 准则更名 `docs/ui-spec.md`，
  全部引用同步更新。

### 测试与 CI

- CI windows/macos job 补上与 Linux 对齐的 `go vet + go test` 门禁
  （#5）——此前仅 Linux 跑测试，Windows 专属回归发布前不可见。
- 测试三平台可移植化：TestExpandHome 按平台设 HOME/USERPROFILE；权限
  注入用例在 Windows 显式跳过（os.Chmod 无法表达目录权限剥夺，等价
  注入需 ACL）。
- 集成测试 hermetic 化：Guard 增进程枚举注入缝（`NewNoScanGuard`），
  宿主机恰好运行真实 osu! 时测试不再被误拦。

### 体验

- 扫描按结果数量反馈（#6）：干净环境零结果时不再静默无操作，弹引导
  toast（+ → 选择文件夹… 手动添加）；首次自动扫描失败不再吞错。

## [1.1.0] - 2026-11-07

新增**旧版 osu!（stable）端**接入：与四个 lazer fork 并列的第五端，
hub-and-spoke 模型下的双向同步（谱面集 / 收藏夹 / 皮肤；成绩、键位、
设置因两端模型不同构不适用）。

### stable 端（新端）

- **端形态**：无 realm 库——谱面为 `Songs/` 解包目录 + `osu!.db` 二进制
  索引、收藏为 `collection.db`、皮肤为 `Skins/` 目录。注册按目录特征
  （含 `osu!.db`）自动识别，安装目录任意（如 `~/Games/osu!`）。
- **收集**：以 Songs/ 目录扫描为真相源（投放后未开过游戏的目录立即
  可见，闭环不依赖游戏跑过一遍）；`osu!.db` 供难度 MD5/星数等补充
  元数据，缺失或损坏只计数不阻断。`internal/legacydb` 解析器经真机
  20260924 样本逐字节验证（208/208 难度 MD5 与 .osu 文件一致、解析
  终点与文件尾重合，覆盖三个历史版本形态分叉）。
- **投放（链接式）**：谱面集构造 `Songs/<setID> <artist> - <title>/`
  目录、逐文件链接到内容寻址仓（与收集回链同形态，磁盘只存一份字节）；
  收藏 `collection.db` 整文件原子重写（写前备份 → 临时 → 回读自检 →
  rename）；**永不写 `osu!.db`**，新目录由游戏启动自动导入。游戏内删
  谱面删除的只是链接，天然落入 tombstone/恢复模型。
- **跨端同键锚点**（经真机对拍验证）：难度 Hash = .osu SHA-256、难度
  MD5、集哈希 = 集 .osu 按文件名序拼接的 SHA-256——同一谱面两端同键，
  三方判定直接可比；集身份键 `online:<BeatmapSetID>` 两端对齐。
- **皮肤**：收集按目录规范化哈希（stable 变体自有其键）；跨端配对走
  Name 弱键，双侧防重复防线（同名不同内容键视为同皮肤变体，跳过不
  覆盖，确需导入改名重投）。
- **现场复核**：同内容（难度 Hash 集合一致——含 lazer 集哈希导入缓存
  stale 的兜底）/ 版本分歧（替换走确认不自动写）/ 仓缺字节（绝不落
  半套目录）三路跳过。

### 架构

- `ForkProfile.Storage` 端存储载体类型（realm / legacy 文件系统）：
  collect/deploy 按其分派投影器与写入后端，判定引擎、hub、板块开关
  零改动——「新增端 = 新增一份 Profile，不改内核」承诺在写入载体层
  兑现。
- 守卫消歧：wine 进程名 `osu!.exe` 与 lazer 的 `osu!` 精确匹配天然
  区分；Windows 上两端镜像同名属平台固有项（按可执行路径消歧留待
  后续）。
- 格式知识自包含固化于 `docs/research/stable-legacy.md`（ppy/osu 官方
  wiki 格式表 + 本机实测注记：lazer 集哈希 stale、NaN 净化、同内容
  重复目录去重等）。

## [1.0.0] - 2026-10-28

首个正式版本。LZSync 以 SQLite 中枢为单一真相源，对同一台机器上的多个
osu!lazer fork（官方 lazer、DeltaLazer、Ez2Lazer、g0v0）执行收集
（fork → hub）与投放（hub → fork）双向同步。

### 同步

- **Hub-and-spoke 架构**：SQLite 中枢持有版本链、tombstone、`origin_fork`
  与对账查询；游戏侧 realm 库只是投影/挂载点。
- **收集方向**：曲库、收藏夹、皮肤、成绩、键位、Mod 预设、ruleset 设置、
  game.ini 逐键快照——各端差异分别存、只增不改；实体文件入自有内容寻址仓，
  游戏侧以符号链接/硬链接/复制分级方式持有。
- **投放方向**：导出取全部目标端的全局交集；只传播基线三方判定能确定的
  改动，判不准保留差异；一切同步入口遵循设置（板块开关 + 端的来源/目标
  角色）。
- **基线三方判定**（§0.3）：谱面集逐成员、键位三段键组合集合、game.ini
  逐键；冲突默认不动，支持逐端拍板、「以某端为准」与「保留差异」处置。
- **数据安全**：写前备份、软删优先、占用拒写（进程守卫 + realm 写会话
  探测）、DriftGuard 结构零漂移校验、收集/投放失败自动回滚。

### 平台与打包

- Go + mygo（系统 webview：WebKitGTK / WebView2 / WKWebView），无 cgo，
  realm-core 20.1.1 经 purego Dlopen 接入，版本钉死与四端 Realm NuGet
  对齐（file format v24，绝不自动升级）。
- 分发形态：Linux（.deb / .rpm / .AppImage / tar.gz + install.sh）、
  Windows（NSIS 安装程序 + 便携 zip）、macOS（universal .dmg，Apple
  Silicon 与 Intel 通用）。
- GitHub Actions 自动发版：推送 `v*` 标签即三 runner 并行构建并发布
  Release（`.github/workflows/release.yml`）。
- realm-core 三平台构建脚本 `scripts/build-librealm.sh`（linux-amd64 /
  windows-x64 / macos-universal），产物随应用打包，运行时按打包布局解析。

### 界面

- 三页结构：同步台（拓扑 + 收件箱 + 决策卡）、资料库（聚合视图 + 逐端
  presence）、设置（板块开关 / 端角色 / 进程守卫名单）；中英双语切换；
  深色纯色风格。
- Mock 后端降级：`LZSYNC_MOCK=1` 或 native 库/数据目录不可用时降级演示
  模式，不闪退。
