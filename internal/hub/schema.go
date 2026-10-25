package hub

// SchemaVersion 当前 hub.db schema 版本（版本化迁移自管，§3.4；hub 是
// LZSync 自有格式，与游戏库 schema 完全无关，S3 不受影响）。
const SchemaVersion = 3

// ddl 是 schema v1 全部建表语句（§4.4 表族）。时间戳一律 Unix 毫秒。
// 契约要点：
//   - HB1 版本链：beatmap_set.superseded_by，NULL = 当前版本（链尾），
//     「最新」只认链尾；历史版本行永久保留、永不进入投放计划。
//   - HB2 presence/tombstone：presence 按 fork 整段重写（快照语义，天然
//     幂等）；重写前与上次快照差集生成 tombstone，同键重现即清除。
//   - origin_fork/origin_guid：首见即定（§0-B2，insert-if-absent；HB3 的
//     整行覆盖与随行更新已废止——后到端取值留在 presence.local_values）；
//     派生 origin_realm（= ForkProfile.ServerRealm）供 S8 判定（B2 后它
//     只代表首见来源，G9 待办）。
//   - 仓清单 file：store/ 是否持有该哈希，收集/投放后更新。
//
// 实体行的规范字段载荷存 payload 列（中立 DTO JSON）；M2 若需字段级 SQL
// 再以 v2 迁移展开为列，v1 不预建。M2 实体（skin/score/…）表族同样由
// 后续迁移追加。
const ddl = `
CREATE TABLE IF NOT EXISTS meta(
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS collection(
  content_key TEXT PRIMARY KEY,
  payload     TEXT NOT NULL,
  origin_guid TEXT NOT NULL,
  origin_fork TEXT NOT NULL,
  first_seen  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS beatmap_set(
  content_hash TEXT PRIMARY KEY,
  payload      TEXT NOT NULL,
  origin_guid  TEXT NOT NULL,
  origin_fork  TEXT NOT NULL,
  superseded_by TEXT,
  first_seen   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS beatmap(
  content_hash TEXT PRIMARY KEY,
  set_hash     TEXT NOT NULL,
  payload      TEXT NOT NULL,
  first_seen   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS presence(
  fork_id      TEXT NOT NULL,
  kind         TEXT NOT NULL,
  content_key  TEXT NOT NULL,
  local_values TEXT NOT NULL,
  collected_at INTEGER NOT NULL,
  PRIMARY KEY(fork_id, kind, content_key)
);

CREATE TABLE IF NOT EXISTS tombstone(
  fork_id     TEXT NOT NULL,
  kind        TEXT NOT NULL,
  content_key TEXT NOT NULL,
  deleted_at  INTEGER NOT NULL,
  PRIMARY KEY(fork_id, kind, content_key)
);

CREATE TABLE IF NOT EXISTS file(
  hash       TEXT PRIMARY KEY,
  size       INTEGER NOT NULL,
  first_seen INTEGER NOT NULL,
  in_store   INTEGER NOT NULL DEFAULT 0
);

-- 谱面身份键配对缓存（§4.2 第二层 / §4.4 对账缓存）：
-- identity_key 形如 "online:<id>"（强键，同服务器）或
-- "meta:<artist>|<title>|<author>"（弱键，需用户确认）。
CREATE TABLE IF NOT EXISTS beatmap_identity(
  identity_key   TEXT PRIMARY KEY,
  evidence       TEXT NOT NULL,
  user_confirmed INTEGER NOT NULL DEFAULT 0,
  first_seen     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS identity_version(
  identity_key TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  PRIMARY KEY(identity_key, content_hash)
);

CREATE TABLE IF NOT EXISTS collect_log(
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  fork_id         TEXT NOT NULL,
  finished_at     INTEGER NOT NULL,
  entity_counts   TEXT NOT NULL,
  skip_reasons    TEXT NOT NULL,
  tombstone_added INTEGER NOT NULL
);
`

// ddlV2 是 schema v2 增量（M2 实体扩面）：新表 skin 与 score。只做加法，
// v1 语句一字不改——已落盘的 v1 库是合法中间态，由迁移链补齐到 v2。
// 全部 IF NOT EXISTS，中断重跑幂等。实体 Files 沿用 v1 形态内联进 payload
// JSON（不做 skin_file 关联表：与 beatmap_set 的 Files 载荷形态一致）。
// presence/tombstone 天然按 kind 泛化，无需新表——skin 按内容键 Hash、
// score 按组合内容键（ScoreContentKey 编码）各占一席。
const ddlV2 = `
CREATE TABLE IF NOT EXISTS skin(
  content_hash TEXT PRIMARY KEY,
  payload      TEXT NOT NULL,
  origin_guid  TEXT NOT NULL,
  origin_fork  TEXT NOT NULL,
  first_seen   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

-- score 内容键 = (BeatmapHash, Hash) 组合键列（§4.2 第一层），不做单列
-- 拼接主键。
CREATE TABLE IF NOT EXISTS score(
  beatmap_hash TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  payload      TEXT NOT NULL,
  origin_guid  TEXT NOT NULL,
  origin_fork  TEXT NOT NULL,
  first_seen   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY(beatmap_hash, content_hash)
);
`

// ddlV3 是 schema v3 增量（M3 设置类实体，design.md §5.1-M3）：key_binding
// / ruleset_setting / mod_preset 三表，组合内容键做复合主键（§4.2 第一层，
// score 两列复合主键的同款延续；不做单列拼接主键）。只做加法，v1/v2
// 语句一字不改。全部 IF NOT EXISTS，中断重跑幂等。presence/tombstone 仍
// 天然按 kind 泛化——内容键经包内编码函数（KeyBindingContentKey 等）单列
// 化，NULL 语义段（全局键位/无变体）在编码里保真，不进表列。
//
// 归一化哨兵：key_binding 的 RulesetName（realm 侧 string?，null = 全局
// 键位）落库为 ”；Variant（int?，null = 无变体）落库为 -1（真实域是
// mania 键数等非负值，-1 不碰撞）。KeyCombination 入主键是 2026-10-25
// 真实库实测修订（v3 随 M3 发布前修订，无旧库在野）：lazer 一个 action
// 允许多行多键，三段主键会把副键位行当重复吞掉（真库探针：495 行塌缩
// 311 三段键、183 组同键不同键组合）。ruleset_setting 的 realm 侧 Variant
// 是非空 int，无需哨兵；RulesetSetting 无 Guid 列（realm 侧无主键表，
// lazer-baseline §2.13），origin_guid 列不设。mod_preset 无内容哈希列，
// 取业务键 (RulesetShortName, Name)（§4.2）。
const ddlV3 = `
CREATE TABLE IF NOT EXISTS key_binding(
  ruleset_name TEXT NOT NULL,
  variant      INTEGER NOT NULL,
  action_int   INTEGER NOT NULL,
  key_combination TEXT NOT NULL DEFAULT '',
  payload      TEXT NOT NULL,
  origin_guid  TEXT NOT NULL,
  origin_fork  TEXT NOT NULL,
  first_seen   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY(ruleset_name, variant, action_int, key_combination)
);

CREATE TABLE IF NOT EXISTS ruleset_setting(
  ruleset_name TEXT NOT NULL,
  variant      INTEGER NOT NULL,
  key          TEXT NOT NULL,
  payload      TEXT NOT NULL,
  origin_fork  TEXT NOT NULL,
  first_seen   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY(ruleset_name, variant, key)
);

CREATE TABLE IF NOT EXISTS mod_preset(
  ruleset_short_name TEXT NOT NULL,
  name               TEXT NOT NULL,
  payload            TEXT NOT NULL,
  origin_guid        TEXT NOT NULL,
  origin_fork        TEXT NOT NULL,
  first_seen         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  PRIMARY KEY(ruleset_short_name, name)
);
`

// migrations 是版本化迁移链：migrations[i] 把库从 schema 版本 i+1 升到
// i+2（migrations[0] = v1→v2）。新增版本 = 追加一段 DDL 并抬高
// SchemaVersion；禁止改写既有条目（旧库可能已按其落盘）。
var migrations = []string{ddlV2, ddlV3}
