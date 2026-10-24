// Package itests 是 M1 集成验收测试（design.md §7-M1 修订后的断言面）：
// 版本链（HB1）、tombstone（HB2）、四分类对账、收藏夹全链往返（含占用
// 拦截与备份）与收仓四步失败注入（HB4）。链路全部走真实编排层
// （collect.Runner / deploy.Runner / hub 对账），fixture 造库、realmc 动态
// 会话做游戏侧现场注入（软删/物理删）。librealm 缺失时显式 SKIP
//（§8.1 补充门：不得静默绿）。
package itests

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// t0 固定基准时间（避开本地时钟差异；收藏夹 LastModified 逐字段断言用）。
var t0 = time.UnixMilli(1770000000000).UTC()

// mustLib 加载 native 库；缺失时显式 SKIP（§8.1）。
func mustLib(t *testing.T) *realmc.Library {
	t.Helper()
	lib, err := realmc.Load()
	if err != nil {
		t.Skipf("realm-core 库不可用，跳过集成验收（§8.1 显式 SKIP）: %v", err)
	}
	return lib
}

// env 聚合一次集成场景的全部依赖（hub.db + 仓 + 结构档案 + 收集/投放编排）。
type env struct {
	t       *testing.T
	dir     string // hub 根（hub.db / store / snapshots / backups 同居，§3.4）
	hub     *hub.Store
	engine  *filestore.Engine
	snaps   *forks.SnapshotStore
	backups *guard.Backups
}

// newEnv 建独立 hub 目录布局（每测试一座，互不污染）。
func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	h, err := hub.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatalf("hub.Open: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	store, err := filestore.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	snaps, err := forks.OpenSnapshotStore(filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatalf("forks.OpenSnapshotStore: %v", err)
	}
	backups, err := guard.OpenBackups(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("guard.OpenBackups: %v", err)
	}
	return &env{t: t, dir: dir, hub: h, engine: filestore.NewEngine(store), snaps: snaps, backups: backups}
}

// collectRunner 空名单守卫（恒放行——占用场景由专门测试注入）。
func (e *env) collectRunner() *collect.Runner {
	return collect.NewRunner(collect.Deps{
		Hub: e.hub, Files: e.engine, Snapshots: e.snaps, Guard: guard.NewGuard(nil),
	})
}

// deployRunner 集中备份目录形态（S2）。
func (e *env) deployRunner() *deploy.Runner {
	return deploy.NewRunner(deploy.Deps{Hub: e.hub, Backups: e.backups, Guard: guard.NewGuard(nil)})
}

// buildLib fixture 造库（§8.2：测试库是 LZSync 自产合成产物）。
func buildLib(t *testing.T, forkID string, version uint64, seed fixture.Seed) forks.LibraryRef {
	t.Helper()
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID: forkID, SchemaVersion: version, DataDir: t.TempDir(), Seed: seed,
	})
	if err != nil {
		t.Fatalf("fixture.Build(%s): %v", forkID, err)
	}
	return ref
}

// collectInto 真实收集一次并断言成功。
func (e *env) collectInto(ref forks.LibraryRef) collect.Report {
	e.t.Helper()
	report, err := e.collectRunner().Collect(context.Background(), ref, nil)
	if err != nil {
		e.t.Fatalf("Collect(%s %s): %v", ref.ForkID, ref.RealmPath, err)
	}
	return report
}

// ---- hub 直查断言面（presence / tombstone / 版本链） ----

func (e *env) supersededOf(hash string) string {
	e.t.Helper()
	var next sql.NullString
	if err := e.hub.DB().QueryRow(
		`SELECT superseded_by FROM beatmap_set WHERE content_hash=?`, hash).Scan(&next); err != nil {
		e.t.Fatalf("读 %s 的 superseded_by: %v", hash, err)
	}
	return next.String
}

func (e *env) tombstoneCount(forkID string, kind hub.EntityKind, key string) int {
	e.t.Helper()
	var n int
	if err := e.hub.DB().QueryRow(
		`SELECT COUNT(*) FROM tombstone WHERE fork_id=? AND kind=? AND content_key=?`,
		forkID, kind, key).Scan(&n); err != nil {
		e.t.Fatalf("查 tombstone: %v", err)
	}
	return n
}

// presenceDiff fork↔fork 两份快照的差集（在 forkB 不在 forkA 的内容键，
// §4.4「fork↔fork 对比 = 两份 presence 的差集」的 SQL 面）。
func (e *env) presenceDiff(forkA, forkB string, kind hub.EntityKind) []string {
	e.t.Helper()
	rows, err := e.hub.DB().Query(`
SELECT content_key FROM presence WHERE fork_id=? AND kind=?
  AND content_key NOT IN (
      SELECT content_key FROM presence WHERE fork_id=? AND kind=?)`,
		forkB, kind, forkA, kind)
	if err != nil {
		e.t.Fatalf("presence 差集查询: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			e.t.Fatalf("读差集行: %v", err)
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// reconcile hub→fork 投放预演差异（§4.4 对账语义）。
func (e *env) reconcile(forkID string, kinds ...hub.EntityKind) []hub.ReconcileItem {
	e.t.Helper()
	items, err := e.hub.ReconcileAgainst(context.Background(), forkID, kinds)
	if err != nil {
		e.t.Fatalf("ReconcileAgainst(%s): %v", forkID, err)
	}
	return items
}

// ---- 游戏侧现场注入（realmc 动态写会话） ----

// setBeatmapSetDeleted 模拟游戏内删除谱面集（S4：写 DeletePending 软删标记；
// 收集投影把软删行视为不存在，design §5.4）。
func setBeatmapSetDeleted(t *testing.T, lib *realmc.Library, realmPath, setHash string, deleted bool) {
	t.Helper()
	mutate(t, lib, realmPath, func(r *realmc.Realm) error {
		ck, err := r.FindClass("BeatmapSet")
		if err != nil {
			return err
		}
		hashKey, err := r.FindProperty(ck, "Hash")
		if err != nil {
			return err
		}
		dpKey, err := r.FindProperty(ck, "DeletePending")
		if err != nil {
			return err
		}
		obj, err := r.FindFirstByString(ck, hashKey, setHash)
		if err != nil {
			return err
		}
		if obj == nil {
			t.Fatalf("注入软删：找不到 Hash=%s 的 BeatmapSet 行", setHash)
		}
		defer r.ReleaseObject(obj)
		return r.SetBool(obj, dpKey, deleted)
	})
}

// deleteCollectionRow 模拟游戏内删除收藏夹（收藏夹无软删语义，官方即整行
// Remove，design §1.1-S4 例外）。
func deleteCollectionRow(t *testing.T, lib *realmc.Library, realmPath, name string) {
	t.Helper()
	mutate(t, lib, realmPath, func(r *realmc.Realm) error {
		ck, err := r.FindClass("BeatmapCollection")
		if err != nil {
			return err
		}
		nameKey, err := r.FindProperty(ck, "Name")
		if err != nil {
			return err
		}
		obj, err := r.FindFirstByString(ck, nameKey, name)
		if err != nil {
			return err
		}
		if obj == nil {
			t.Fatalf("注入删除：找不到 Name=%s 的收藏夹行", name)
		}
		defer r.ReleaseObject(obj)
		return r.DeleteObject(obj)
	})
}

// mutate 开写会话执行 fn 后提交（游戏侧现场注入的公共骨架）。
func mutate(t *testing.T, lib *realmc.Library, realmPath string, fn func(r *realmc.Realm) error) {
	t.Helper()
	r, err := lib.OpenDynamicReadWrite(realmPath)
	if err != nil {
		t.Fatalf("写会话打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginWrite(); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	if err := fn(r); err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("现场注入失败: %v", err)
	}
	if err := r.Commit(); err != nil {
		t.Fatalf("提交注入: %v", err)
	}
}

// ---- 增强形态收藏夹库（完整列面：Guid 主键 + MD5 列表 + LastModified） ----
// fixture 镜像不承载集合列/主键（fixture 包注释的已知边界），全链往返的
// 「逐字段相等」断言需要完整列面，此处以 realmc 显式 schema 自建（§8.2-2
// 构造路径；产品路径仍严格钉死动态打开）。

func fullCollectionSchema() *realmc.Schema {
	return &realmc.Schema{Classes: []realmc.ClassDef{
		{Name: "BeatmapCollection", PrimaryKey: "ID", Properties: []realmc.PropertyDef{
			{Name: "ID", Type: realmc.PropTypeUUID, IsPrimaryKey: true},
			{Name: "Name", Type: realmc.PropTypeString},
			{Name: "BeatmapMD5Hashes", Type: realmc.PropTypeString, Collection: realmc.CollectionList},
			{Name: "LastModified", Type: realmc.PropTypeTimestamp},
		}},
	}}
}

// buildFullLib 建增强形态空库（forkID 仅作 hub 侧 presence 键）。
func buildFullLib(t *testing.T, lib *realmc.Library, forkID string) forks.LibraryRef {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, fullCollectionSchema(), 52)
	if err != nil {
		t.Fatalf("建增强库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭增强库: %v", err)
	}
	return forks.LibraryRef{
		ForkID: forkID, DataDir: dir, RealmPath: path,
		DiskVersion: 52, VersionSource: forks.VersionFromOpen,
	}
}

// seedFullCollection 向增强库播种一行收藏夹（全列面）。
func seedFullCollection(t *testing.T, lib *realmc.Library, realmPath string, c hub.Collection) {
	t.Helper()
	mutate(t, lib, realmPath, func(r *realmc.Realm) error {
		ck, err := r.FindClass("BeatmapCollection")
		if err != nil {
			return err
		}
		nameKey, err := r.FindProperty(ck, "Name")
		if err != nil {
			return err
		}
		lmKey, err := r.FindProperty(ck, "LastModified")
		if err != nil {
			return err
		}
		md5Key, err := r.FindProperty(ck, "BeatmapMD5Hashes")
		if err != nil {
			return err
		}
		obj, err := r.CreateObjectWithGuidPK(ck, guidBytes(t, c.ID))
		if err != nil {
			return err
		}
		defer r.ReleaseObject(obj)
		if err := r.SetString(obj, nameKey, c.Name); err != nil {
			return err
		}
		if err := r.SetTimestamp(obj, lmKey, c.LastModified); err != nil {
			return err
		}
		lst, err := r.GetList(obj, md5Key)
		if err != nil {
			return err
		}
		defer lst.Close()
		for _, h := range c.BeatmapMD5Hashes {
			if err := lst.AddString(h); err != nil {
				return err
			}
		}
		return nil
	})
}

// readFullCollection 只读会话按名读回一行（白名单全列面，逐字段断言的数据源）。
// 读毕即关：残留只读会话会让下一次 Deploy 的占用探测误判（deploy 施工期实证）。
func readFullCollection(t *testing.T, lib *realmc.Library, realmPath, name string) (hub.Collection, bool) {
	t.Helper()
	var out hub.Collection
	r, err := lib.OpenDynamicReadOnly(realmPath)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	ck, err := r.FindClass("BeatmapCollection")
	if err != nil {
		t.Fatalf("FindClass: %v", err)
	}
	nameKey, err := r.FindProperty(ck, "Name")
	if err != nil {
		t.Fatalf("FindProperty(Name): %v", err)
	}
	obj, err := r.FindFirstByString(ck, nameKey, name)
	if err != nil {
		t.Fatalf("FindFirstByString(%s): %v", name, err)
	}
	if obj == nil {
		return out, false
	}
	defer r.ReleaseObject(obj)

	out.Name = name
	if key, err := r.FindProperty(ck, "ID"); err == nil {
		if b, err := r.GetGuid(obj, key); err == nil {
			out.ID = hex.EncodeToString(b[:])
		}
	}
	if key, err := r.FindProperty(ck, "LastModified"); err == nil {
		if ts, err := r.GetTimestamp(obj, key); err == nil {
			out.LastModified = ts.UTC()
		}
	}
	if key, err := r.FindProperty(ck, "BeatmapMD5Hashes"); err == nil {
		lst, err := r.GetList(obj, key)
		if err != nil {
			t.Fatalf("GetList(BeatmapMD5Hashes): %v", err)
		}
		defer lst.Close()
		n, _ := lst.Size()
		for i := uint64(0); i < n; i++ {
			h, err := lst.GetString(i)
			if err != nil {
				t.Fatalf("读成员 %d: %v", i, err)
			}
			out.BeatmapMD5Hashes = append(out.BeatmapMD5Hashes, h)
		}
	}
	return out, true
}

// schemaSnapshotOf 只读会话捕获结构档案（零漂移的独立校验，§8.2-7）。
func schemaSnapshotOf(t *testing.T, lib *realmc.Library, realmPath string) *realmc.SchemaSnapshot {
	t.Helper()
	r, err := lib.OpenDynamicReadOnly(realmPath)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	snap, err := r.CaptureSchemaSnapshot()
	if err != nil {
		t.Fatalf("CaptureSchemaSnapshot: %v", err)
	}
	return snap
}

// ---- 通用小工具 ----

func guidBytes(t *testing.T, s string) [16]byte {
	t.Helper()
	clean := strings.ReplaceAll(s, "-", "")
	b, err := hex.DecodeString(clean)
	if err != nil || len(b) != 16 {
		t.Fatalf("非法 Guid %q: %v", s, err)
	}
	var out [16]byte
	copy(out[:], b)
	return out
}

func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func equalUnordered(a, b []string) bool {
	sa := append([]string(nil), a...)
	sb := append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// guidEqual 归一化比较：读回/投影形态是 32 位 hex，播种夹具常带连字符。
func guidEqual(a, b string) bool {
	return strings.ToLower(strings.ReplaceAll(a, "-", "")) ==
		strings.ToLower(strings.ReplaceAll(b, "-", ""))
}

// assertCollectionEqual 逐字段断言（Guid / Name / MD5 逐元素 / LastModified）。
func assertCollectionEqual(t *testing.T, lib *realmc.Library, realmPath string, want hub.Collection) {
	t.Helper()
	got, ok := readFullCollection(t, lib, realmPath, want.Name)
	if !ok {
		t.Errorf("读回缺 %q", want.Name)
		return
	}
	if !guidEqual(got.ID, want.ID) {
		t.Errorf("%s Guid = %s，期望 %s", want.Name, got.ID, want.ID)
	}
	if !equalUnordered(got.BeatmapMD5Hashes, want.BeatmapMD5Hashes) {
		t.Errorf("%s MD5 = %v，期望 %v", want.Name, got.BeatmapMD5Hashes, want.BeatmapMD5Hashes)
	}
	if !got.LastModified.Equal(want.LastModified) {
		t.Errorf("%s LastModified = %v，期望 %v", want.Name, got.LastModified, want.LastModified)
	}
}

// mustSet 单难度谱面集夹具（.osu 内容决定全链哈希，§8.2-3）。
func mustSet(t *testing.T, onlineID int64, artist, title, osuContent string, files map[string][]byte) hub.BeatmapSet {
	t.Helper()
	set, err := fixture.MaterializeSet(onlineID, artist, title, "mapper",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte(osuContent)}}, nil, files)
	if err != nil {
		t.Fatalf("MaterializeSet(%s): %v", title, err)
	}
	return set
}
