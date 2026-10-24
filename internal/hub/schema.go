package hub

// SchemaVersion 当前 hub.db schema 版本（版本化迁移自管，§3.4；hub 是
// LZSync 自有格式，与游戏库 schema 完全无关，S3 不受影响）。
const SchemaVersion = 1

// ddl 是 schema v1 全部建表语句（§4.4 表族）。时间戳一律 Unix 毫秒。
// 契约要点：
//   - HB1 版本链：beatmap_set.superseded_by，NULL = 当前版本（链尾），
//     「最新」只认链尾；历史版本行永久保留、永不进入投放计划。
//   - HB2 presence/tombstone：presence 按 fork 整段重写（快照语义，天然
//     幂等）；重写前与上次快照差集生成 tombstone，同键重现即清除。
//   - HB3 origin_fork：实体 upsert 整行覆盖并随行更新来源 fork；派生
//     origin_realm（= ForkProfile.ServerRealm）供 S8 判定。
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
