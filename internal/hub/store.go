package hub

import (
	"context"
	"database/sql"
	"errors"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动（无 CGO，CGO_ENABLED=0 必须可跑）
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("hub: 契约桩未实现")

// Store 是 hub.db 的访问面（单写者 = LZSync 自身，WAL 模式即可，§3.4）。
type Store struct {
	db *sql.DB
}

// Open 打开（不存在则创建）path 处的 hub.db，启用 WAL 并迁移到最新
// schema（版本化迁移自管）。path 支持 ":memory:" 与临时文件路径（hub 侧
// 测试无 realm 依赖、可高速并行，§8.2-5）。
func Open(path string) (*Store, error) {
	return nil, ErrNotImplemented
}

// Close 关闭底层连接池。
func (s *Store) Close() error {
	return ErrNotImplemented
}

// DB 暴露底层 *sql.DB，仅供 diff 层的只读对账查询（§3.2「对比 = hub 内
// SQL 查询」）；一切写入必须走本包领域方法以维持事务纪律。
func (s *Store) DB() *sql.DB {
	return s.db
}

// SchemaVersion 返回库内 meta 表记录的当前 schema 版本。
func (s *Store) SchemaVersion() (int, error) {
	return 0, ErrNotImplemented
}

// Origin 声明实体行 canonical 值的来源（§4.4）。
type Origin struct {
	GUID string // 首次收集该实体时的源 fork Guid（投放新建行时沿用，§4.2）
	Fork string // 本行 canonical 值的来源 fork（HB3）；派生 origin_realm 供 S8 判定
}

// LoadCollection 按内容键读取收藏夹 canonical 值及其来源声明。
// 不存在返回 (零值, Origin{}, false, nil)。
func (s *Store) LoadCollection(ctx context.Context, contentKey string) (Collection, Origin, bool, error) {
	return Collection{}, Origin{}, false, ErrNotImplemented
}

// BeatmapSetEntry 是当前版本谱面集查询结果行。
type BeatmapSetEntry struct {
	Set    BeatmapSet
	Origin Origin
}

// CurrentBeatmapSets 返回全部当前版本谱面集（superseded_by IS NULL——
// 版本链尾即「最新」，HB1；历史版本不进入任何投放面）。
func (s *Store) CurrentBeatmapSets(ctx context.Context) ([]BeatmapSetEntry, error) {
	return nil, ErrNotImplemented
}
