package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO，CGO_ENABLED=0 必须可跑）
)

// Store 是 hub.db 的访问面（单写者 = LZSync 自身，WAL 模式即可，§3.4）。
type Store struct {
	db *sql.DB
}

// Open 打开（不存在则创建）path 处的 hub.db，启用 WAL 并迁移到最新
// schema（版本化迁移自管）。path 支持 ":memory:" 与临时文件路径（hub 侧
// 测试无 realm 依赖、可高速并行，§8.2-5）。
//
// 连接池钉为单连接：":memory:" 库每连接独立（多连接会看到不同库），且
// hub 是本地单写者库，单连接同时杜绝自并发 SQLITE_BUSY。
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("hub: 打开 %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	// 内存库不支持 WAL（PRAGMA 返回 "memory"），文件库落到 WAL；两者都不报错。
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		db.Close()
		return nil, fmt.Errorf("hub: 设置 journal_mode: %w", err)
	}
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("hub: 设置 busy_timeout: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate 在单事务内执行版本化迁移（§3.4：hub 是 LZSync 自有格式，与游戏
// 库 schema 无关）：先落 v1 基础表（ddl 全 IF NOT EXISTS，新库需要、旧库
// 无害），再沿 migrations 链逐级升到 SchemaVersion。链上 DDL 全幂等，
// 重开/中断重跑安全。
func (s *Store) migrate() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("hub: 开迁移事务: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交成功后回滚是 no-op
	if _, err := tx.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("hub: 建 schema v1 表: %w", err)
	}
	var v string
	err = tx.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows): // 新库：v1 基础表已建，从初始版本起走链
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO meta(key,value) VALUES('schema_version','1')`); err != nil {
			return fmt.Errorf("hub: 写 schema 版本: %w", err)
		}
		v = "1"
	case err != nil:
		return fmt.Errorf("hub: 读 schema 版本: %w", err)
	}
	cur, convErr := strconv.Atoi(v)
	if convErr != nil {
		return fmt.Errorf("hub: schema 版本值 %q 非法: %w", v, convErr)
	}
	if cur < 1 || cur > SchemaVersion {
		return fmt.Errorf("hub: 库 schema 版本 %d 超出本程序支持范围 [1,%d]，请升级 LZSync", cur, SchemaVersion)
	}
	for cur < SchemaVersion {
		if _, err := tx.ExecContext(ctx, migrations[cur-1]); err != nil {
			return fmt.Errorf("hub: 迁移 v%d→v%d: %w", cur, cur+1, err)
		}
		cur++
		if _, err := tx.ExecContext(ctx,
			`UPDATE meta SET value=? WHERE key='schema_version'`, strconv.Itoa(cur)); err != nil {
			return fmt.Errorf("hub: 写 schema 版本 %d: %w", cur, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("hub: 提交迁移: %w", err)
	}
	return nil
}

// Close 关闭底层连接池。
func (s *Store) Close() error {
	return s.db.Close()
}

// DB 暴露底层 *sql.DB，仅供 diff 层的只读对账查询（§3.2「对比 = hub 内
// SQL 查询」）；一切写入必须走本包领域方法以维持事务纪律。
func (s *Store) DB() *sql.DB {
	return s.db
}

// SchemaVersion 返回库内 meta 表记录的当前 schema 版本。
func (s *Store) SchemaVersion() (int, error) {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		return 0, fmt.Errorf("hub: 读 schema 版本: %w", err)
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("hub: schema 版本值 %q 非法: %w", v, err)
	}
	return n, nil
}

// Origin 声明实体行 canonical 值的来源（§4.4）。
type Origin struct {
	GUID string // 首次收集该实体时的源 fork Guid（投放新建行时沿用，§4.2）
	Fork string // 本行 canonical 值的来源 fork（HB3）；派生 origin_realm 供 S8 判定
}

// LoadCollection 按内容键读取收藏夹 canonical 值及其来源声明。
// 不存在返回 (零值, Origin{}, false, nil)。
func (s *Store) LoadCollection(ctx context.Context, contentKey string) (Collection, Origin, bool, error) {
	var (
		payload    string
		originGUID string
		originFork string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT payload, origin_guid, origin_fork FROM collection WHERE content_key=?`,
		contentKey).Scan(&payload, &originGUID, &originFork)
	if errors.Is(err, sql.ErrNoRows) {
		return Collection{}, Origin{}, false, nil
	}
	if err != nil {
		return Collection{}, Origin{}, false, fmt.Errorf("hub: 读收藏 %q: %w", contentKey, err)
	}
	var c Collection
	if err := json.Unmarshal([]byte(payload), &c); err != nil {
		return Collection{}, Origin{}, false, fmt.Errorf("hub: 解码收藏 %q payload: %w", contentKey, err)
	}
	return c, Origin{GUID: originGUID, Fork: originFork}, true, nil
}

// LoadSkin 按内容键读取皮肤 canonical 值及其来源声明（v2）。
// 不存在返回 (零值, Origin{}, false, nil)。
func (s *Store) LoadSkin(ctx context.Context, contentHash string) (Skin, Origin, bool, error) {
	var (
		payload    string
		originGUID string
		originFork string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT payload, origin_guid, origin_fork FROM skin WHERE content_hash=?`,
		contentHash).Scan(&payload, &originGUID, &originFork)
	if errors.Is(err, sql.ErrNoRows) {
		return Skin{}, Origin{}, false, nil
	}
	if err != nil {
		return Skin{}, Origin{}, false, fmt.Errorf("hub: 读皮肤 %q: %w", contentHash, err)
	}
	var sk Skin
	if err := json.Unmarshal([]byte(payload), &sk); err != nil {
		return Skin{}, Origin{}, false, fmt.Errorf("hub: 解码皮肤 %q payload: %w", contentHash, err)
	}
	return sk, Origin{GUID: originGUID, Fork: originFork}, true, nil
}

// LoadScore 按组合内容键 (BeatmapHash, Hash) 读取成绩 canonical 值及其
// 来源声明（v2）。
func (s *Store) LoadScore(ctx context.Context, beatmapHash, hash string) (Score, Origin, bool, error) {
	var (
		payload    string
		originGUID string
		originFork string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT payload, origin_guid, origin_fork FROM score WHERE beatmap_hash=? AND content_hash=?`,
		beatmapHash, hash).Scan(&payload, &originGUID, &originFork)
	if errors.Is(err, sql.ErrNoRows) {
		return Score{}, Origin{}, false, nil
	}
	if err != nil {
		return Score{}, Origin{}, false,
			fmt.Errorf("hub: 读成绩 %q: %w", ScoreContentKey(beatmapHash, hash), err)
	}
	var sc Score
	if err := json.Unmarshal([]byte(payload), &sc); err != nil {
		return Score{}, Origin{}, false,
			fmt.Errorf("hub: 解码成绩 %q payload: %w", ScoreContentKey(beatmapHash, hash), err)
	}
	return sc, Origin{GUID: originGUID, Fork: originFork}, true, nil
}

// BeatmapSetEntry 是当前版本谱面集查询结果行。
type BeatmapSetEntry struct {
	Set    BeatmapSet
	Origin Origin
}

// CurrentBeatmapSets 返回全部当前版本谱面集（superseded_by IS NULL——
// 版本链尾即「最新」，HB1；历史版本不进入任何投放面）。
func (s *Store) CurrentBeatmapSets(ctx context.Context) ([]BeatmapSetEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT content_hash, payload, origin_guid, origin_fork
FROM beatmap_set WHERE superseded_by IS NULL ORDER BY content_hash`)
	if err != nil {
		return nil, fmt.Errorf("hub: 查当前版本谱面集: %w", err)
	}
	defer rows.Close()
	var out []BeatmapSetEntry
	for rows.Next() {
		var (
			hash       string
			payload    string
			originGUID string
			originFork string
		)
		if err := rows.Scan(&hash, &payload, &originGUID, &originFork); err != nil {
			return nil, fmt.Errorf("hub: 读当前版本谱面集行: %w", err)
		}
		var bs BeatmapSet
		if err := json.Unmarshal([]byte(payload), &bs); err != nil {
			return nil, fmt.Errorf("hub: 解码谱面集 %q payload: %w", hash, err)
		}
		out = append(out, BeatmapSetEntry{Set: bs, Origin: Origin{GUID: originGUID, Fork: originFork}})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hub: 遍历当前版本谱面集: %w", err)
	}
	return out, nil
}
