# Changelog

本文件记录 LZSync 的全部版本变更，格式遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)，
版本号遵循 [语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

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
