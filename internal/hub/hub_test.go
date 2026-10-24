package hub

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// 固定基准时间：避免本地时钟与单调钟差异影响断言。
var t0 = time.UnixMilli(1770000000000)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("打开内存库: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func applySnap(t *testing.T, s *Store, snap Snapshot) ApplyResult {
	t.Helper()
	res, err := s.ApplySnapshot(context.Background(), snap)
	if err != nil {
		t.Fatalf("ApplySnapshot(fork=%s): %v", snap.ForkID, err)
	}
	return res
}

func mkCollection(name, guid string, hashes ...string) Collection {
	return Collection{ID: guid, Name: name, BeatmapMD5Hashes: hashes, LastModified: t0}
}

func mkSet(hash string, online int64, artist, title, author string, diffs ...string) BeatmapSet {
	bs := BeatmapSet{
		ID: "guid-" + hash, Hash: hash, OnlineID: online, StatusInt: 1,
		DateAdded: t0, Artist: artist, Title: title, AuthorUsername: author,
	}
	for i, d := range diffs {
		bs.Beatmaps = append(bs.Beatmaps, Beatmap{
			ID:               "guid-" + hash + "-" + d,
			Hash:             hash + "-d" + string(rune('0'+i)),
			MD5Hash:          "md5-" + hash + "-" + d,
			DifficultyName:   d,
			RulesetShortName: "osu",
		})
	}
	return bs
}

// queryNullString 直查表断言版本链状态（白盒）。
func queryNullString(t *testing.T, s *Store, query string, args ...any) sql.NullString {
	t.Helper()
	var v sql.NullString
	if err := s.DB().QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("查询 %q: %v", query, err)
	}
	return v
}

func countRows(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("计数 %q: %v", query, err)
	}
	return n
}

// --- 迁移与打开 ---

func TestOpenMigrateMemoryAndFile(t *testing.T) {
	s := openMem(t)
	if v, err := s.SchemaVersion(); err != nil || v != SchemaVersion {
		t.Fatalf("内存库 schema 版本 = (%d,%v)，期望 (%d,nil)", v, err, SchemaVersion)
	}

	path := filepath.Join(t.TempDir(), "hub.db")
	fs, err := Open(path)
	if err != nil {
		t.Fatalf("打开文件库: %v", err)
	}
	applySnap(t, fs, Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("Fav", "g1", "m1")},
	})
	if err := fs.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}

	// 重开：迁移幂等、数据存活。
	fs2, err := Open(path)
	if err != nil {
		t.Fatalf("重开文件库: %v", err)
	}
	defer fs2.Close()
	if v, err := fs2.SchemaVersion(); err != nil || v != SchemaVersion {
		t.Fatalf("重开后 schema 版本 = (%d,%v)", v, err)
	}
	if _, _, ok, err := fs2.LoadCollection(context.Background(), "Fav"); err != nil || !ok {
		t.Fatalf("重开后读收藏 = (%v,%v)", ok, err)
	}
}

// --- 收集 upsert 往返 ---

func TestApplySnapshotRoundtrip(t *testing.T) {
	s := openMem(t)
	snap := Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("Fav", "g-c1", "m1", "m2")},
		BeatmapSets: []BeatmapSet{mkSet("h1", 1, "AR", "TI", "AU", "Easy", "Hard")},
		Skips:       []SkipStat{{Reason: "投影测试跳过", Count: 2}},
	}
	res := applySnap(t, s, snap)

	if got := res.EntitiesWritten[KindCollection]; got != 1 {
		t.Errorf("EntitiesWritten[collection] = %d，期望 1", got)
	}
	if got := res.EntitiesWritten[KindBeatmapSet]; got != 1 {
		t.Errorf("EntitiesWritten[beatmap_set] = %d，期望 1", got)
	}
	if got := res.EntitiesWritten[KindBeatmap]; got != 2 {
		t.Errorf("EntitiesWritten[beatmap] = %d，期望 2", got)
	}

	c, origin, ok, err := s.LoadCollection(context.Background(), "Fav")
	if err != nil || !ok {
		t.Fatalf("LoadCollection = (%v,%v)", ok, err)
	}
	if c.Name != "Fav" || c.ID != "g-c1" || len(c.BeatmapMD5Hashes) != 2 ||
		!c.LastModified.Equal(t0) || c.BeatmapMD5Hashes[0] != "m1" || c.BeatmapMD5Hashes[1] != "m2" {
		t.Errorf("收藏往返不一致: %+v", c)
	}
	if origin != (Origin{GUID: "g-c1", Fork: "A"}) {
		t.Errorf("收藏 Origin = %+v", origin)
	}

	sets, err := s.CurrentBeatmapSets(context.Background())
	if err != nil {
		t.Fatalf("CurrentBeatmapSets: %v", err)
	}
	if len(sets) != 1 || sets[0].Set.Hash != "h1" || len(sets[0].Set.Beatmaps) != 2 ||
		sets[0].Origin != (Origin{GUID: "guid-h1", Fork: "A"}) {
		t.Errorf("谱面集往返不一致: %+v", sets)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM beatmap WHERE set_hash='h1'`); n != 2 {
		t.Errorf("难度行数 = %d，期望 2", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM presence WHERE fork_id='A'`); n != 2 {
		t.Errorf("presence 行数 = %d，期望 2（收藏+谱面集）", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM collect_log WHERE fork_id='A'`); n != 1 {
		t.Errorf("collect_log 行数 = %d，期望 1", n)
	}
}

// --- HB3：origin_fork 随行更新、origin_guid 首次固定 ---

func TestHB3OriginForkUpdatedGuidStable(t *testing.T) {
	s := openMem(t)
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("Fav", "g-A", "m1")},
		BeatmapSets: []BeatmapSet{mkSet("h1", 7, "AR", "TI", "AU", "Easy")},
	})
	// 同内容键从 fork B 再次收集（B 侧 Guid 不同）。
	applySnap(t, s, Snapshot{
		ForkID: "B", CollectedAt: t0.Add(time.Hour),
		Collections: []Collection{mkCollection("Fav", "g-B", "m1")},
		BeatmapSets: []BeatmapSet{mkSet("h1", 7, "AR", "TI", "AU", "Easy")},
	})

	_, oc, _, _ := s.LoadCollection(context.Background(), "Fav")
	if oc != (Origin{GUID: "g-A", Fork: "B"}) {
		t.Errorf("收藏 Origin = %+v，期望 {g-A B}（guid 首次固定、fork 随行更新）", oc)
	}
	sets, err := s.CurrentBeatmapSets(context.Background())
	if err != nil || len(sets) != 1 {
		t.Fatalf("CurrentBeatmapSets = (%d,%v)", len(sets), err)
	}
	if sets[0].Origin != (Origin{GUID: "guid-h1", Fork: "B"}) {
		t.Errorf("谱面集 Origin = %+v，期望 {guid-h1 B}", sets[0].Origin)
	}
}

// --- HB1：版本链（强键自动建链、链尾即当前版本、幂等）---

func TestHB1StrongKeyVersionChain(t *testing.T) {
	s := openMem(t)
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0,
		BeatmapSets: []BeatmapSet{mkSet("hv1", 100, "AR", "TI", "AU", "Easy")},
	})
	res := applySnap(t, s, Snapshot{
		ForkID: "B", CollectedAt: t0.Add(time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hv2", 100, "AR", "TI", "AU", "Easy", "Hard")},
	})

	if res.VersionLinksCreated != 1 {
		t.Errorf("VersionLinksCreated = %d，期望 1", res.VersionLinksCreated)
	}
	got := queryNullString(t, s, `SELECT superseded_by FROM beatmap_set WHERE content_hash='hv1'`)
	if !got.Valid || got.String != "hv2" {
		t.Errorf("hv1.superseded_by = %+v，期望 hv2", got)
	}
	tail := queryNullString(t, s, `SELECT superseded_by FROM beatmap_set WHERE content_hash='hv2'`)
	if tail.Valid {
		t.Errorf("hv2.superseded_by = %q，期望 NULL（链尾）", tail.String)
	}

	sets, err := s.CurrentBeatmapSets(context.Background())
	if err != nil || len(sets) != 1 || sets[0].Set.Hash != "hv2" {
		t.Fatalf("当前版本集合 = (%d,%v)，期望仅 hv2", len(sets), err)
	}

	// 幂等：重跑 B 快照不再新增链。
	res2 := applySnap(t, s, Snapshot{
		ForkID: "B", CollectedAt: t0.Add(2 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hv2", 100, "AR", "TI", "AU", "Easy", "Hard")},
	})
	if res2.VersionLinksCreated != 0 {
		t.Errorf("重跑后 VersionLinksCreated = %d，期望 0", res2.VersionLinksCreated)
	}

	// 三连链：v3 到场 → v2 也被取代。
	applySnap(t, s, Snapshot{
		ForkID: "C", CollectedAt: t0.Add(3 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hv3", 100, "AR", "TI", "AU", "Extra")},
	})
	if got := queryNullString(t, s, `SELECT superseded_by FROM beatmap_set WHERE content_hash='hv2'`); !got.Valid || got.String != "hv3" {
		t.Errorf("hv2.superseded_by = %+v，期望 hv3", got)
	}
	if got := queryNullString(t, s, `SELECT superseded_by FROM beatmap_set WHERE content_hash='hv1'`); !got.Valid || got.String != "hv2" {
		t.Errorf("hv1.superseded_by = %+v，期望仍 hv2（历史行不动）", got)
	}
}

// --- HB1：弱键不自动建链（需用户确认），确认后才建 ---

func TestHB1WeakKeyNeedsConfirmation(t *testing.T) {
	s := openMem(t)
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0,
		BeatmapSets: []BeatmapSet{mkSet("hw1", -1, "ar", "ti", "au", "Easy")},
	})
	res := applySnap(t, s, Snapshot{
		ForkID: "B", CollectedAt: t0.Add(time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hw2", -1, "ar", "ti", "au", "Easy")},
	})

	if res.VersionLinksCreated != 0 {
		t.Errorf("弱键 VersionLinksCreated = %d，期望 0（疑似配对不建链）", res.VersionLinksCreated)
	}
	for _, h := range []string{"hw1", "hw2"} {
		if got := queryNullString(t, s, `SELECT superseded_by FROM beatmap_set WHERE content_hash=?`, h); got.Valid {
			t.Errorf("%s.superseded_by = %q，期望 NULL", h, got.String)
		}
	}
	// 身份缓存已记录弱键证据。
	var evidence string
	if err := s.DB().QueryRow(
		`SELECT evidence FROM beatmap_identity WHERE identity_key='meta:ar|ti|au'`).Scan(&evidence); err != nil || evidence != EvidenceWeak {
		t.Fatalf("弱键证据 = (%q,%v)，期望 weak", evidence, err)
	}
	if n := countRows(t, s,
		`SELECT COUNT(*) FROM identity_version WHERE identity_key='meta:ar|ti|au'`); n != 2 {
		t.Errorf("identity_version 行数 = %d，期望 2", n)
	}

	// 用户确认后，下一版本到场即建链（两链尾均指向新版本）。
	if _, err := s.DB().Exec(
		`UPDATE beatmap_identity SET user_confirmed=1 WHERE identity_key='meta:ar|ti|au'`); err != nil {
		t.Fatalf("确认身份键: %v", err)
	}
	res2 := applySnap(t, s, Snapshot{
		ForkID: "C", CollectedAt: t0.Add(2 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hw3", -1, "ar", "ti", "au", "Easy")},
	})
	if res2.VersionLinksCreated != 2 {
		t.Errorf("确认后 VersionLinksCreated = %d，期望 2", res2.VersionLinksCreated)
	}
}

// --- HB2：tombstone 生成 / 清除 / 版本链取代不算删除 ---

func TestHB2TombstoneLifecycle(t *testing.T) {
	s := openMem(t)
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("C1", "g1", "m1"), mkCollection("C2", "g2", "m2")},
	})
	// C2 消失 → tombstone。
	res := applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(time.Hour),
		Collections: []Collection{mkCollection("C1", "g1", "m1")},
	})
	if res.TombstonesAdded != 1 || res.TombstonesCleared != 0 {
		t.Fatalf("差集结果 = (+%d,-%d)，期望 (+1,-0)", res.TombstonesAdded, res.TombstonesCleared)
	}
	if n := countRows(t, s,
		`SELECT COUNT(*) FROM tombstone WHERE fork_id='A' AND kind='collection'`); n != 1 {
		t.Fatalf("tombstone 行数 = %d，期望 1", n)
	}
	var at int64
	if err := s.DB().QueryRow(
		`SELECT deleted_at FROM tombstone WHERE fork_id='A' AND content_key='C2'`).Scan(&at); err != nil || at != t0.Add(time.Hour).UnixMilli() {
		t.Fatalf("deleted_at = (%d,%v)", at, err)
	}

	// C2 重现 → 自动清除。
	res = applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(2 * time.Hour),
		Collections: []Collection{mkCollection("C1", "g1", "m1"), mkCollection("C2", "g2", "m2")},
	})
	if res.TombstonesAdded != 0 || res.TombstonesCleared != 1 {
		t.Fatalf("重现结果 = (+%d,-%d)，期望 (+0,-1)", res.TombstonesAdded, res.TombstonesCleared)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM tombstone WHERE fork_id='A'`); n != 0 {
		t.Fatalf("重现后 tombstone 行数 = %d，期望 0", n)
	}

	// 版本更新取代的消失不算删除：v1 在场 → 只收 v2（同强键）。
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(3 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hv1", 100, "AR", "TI", "AU", "Easy")},
	})
	res = applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(4 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hv2", 100, "AR", "TI", "AU", "Easy")},
	})
	if res.TombstonesAdded != 0 {
		t.Errorf("版本取代后 TombstonesAdded = %d，期望 0（被链取代不算删除）", res.TombstonesAdded)
	}
	if n := countRows(t, s,
		`SELECT COUNT(*) FROM tombstone WHERE fork_id='A' AND content_key='hv1'`); n != 0 {
		t.Errorf("hv1 tombstone 行数 = %d，期望 0", n)
	}

	// 对照：无链取代的普通谱面消失 → 生成 tombstone。
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(5 * time.Hour),
		BeatmapSets: []BeatmapSet{
			mkSet("hx1", 900, "AR2", "TI2", "AU2", "Easy"),
			mkSet("hx2", 901, "AR3", "TI3", "AU3", "Easy"),
		},
	})
	res = applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(6 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hx1", 900, "AR2", "TI2", "AU2", "Easy")},
	})
	if res.TombstonesAdded != 1 {
		t.Errorf("普通消失 TombstonesAdded = %d，期望 1", res.TombstonesAdded)
	}
}

// --- 收集事务原子性：中途失败整体不生效 ---

func TestApplySnapshotFailureLeavesNoTrace(t *testing.T) {
	s := openMem(t)
	// 先建立一次成功基线（含可差集的 presence）。
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("C1", "g1", "m1")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预取消：事务首条写入即失败
	_, err := s.ApplySnapshot(ctx, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(time.Hour),
		Collections: []Collection{mkCollection("C2", "g2", "m2")},
	})
	if err == nil {
		t.Fatal("已取消 ctx 下 ApplySnapshot 应失败")
	}
	// 失败的收集不得产生任何痕迹：无新实体、无新 presence、无 tombstone、无审计行。
	if _, _, ok, e := s.LoadCollection(context.Background(), "C2"); e != nil || ok {
		t.Errorf("失败后 C2 实体 = (%v,%v)，期望不存在", ok, e)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM presence WHERE fork_id='A' AND content_key='C2'`); n != 0 {
		t.Errorf("失败后 C2 presence = %d，期望 0", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM tombstone WHERE fork_id='A'`); n != 0 {
		t.Errorf("失败后 tombstone = %d，期望 0（部分失败不生成，HB2 前置条件）", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM collect_log`); n != 1 {
		t.Errorf("collect_log = %d，期望仍为 1（只有基线那次）", n)
	}
}

// --- 对账四分类 + 恢复组 + 历史版本不进差集 ---

func TestReconcileFourCategories(t *testing.T) {
	s := openMem(t)
	ctx := context.Background()

	// fork A：普通集 S1、旧版 V1、收藏 C1/C2。
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("Fav", "g1", "m1", "m2"), mkCollection("Tmp", "g2", "m3")},
		BeatmapSets: []BeatmapSet{
			mkSet("h1", 1, "AR", "T1", "AU", "Easy"),
			mkSet("hv1", 100, "AR2", "T2", "AU2", "Easy"),
		},
	})
	// A 更新：V2 取代 V1；用户删掉收藏 Tmp。
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(time.Hour),
		Collections: []Collection{mkCollection("Fav", "g1", "m1", "m2")},
		BeatmapSets: []BeatmapSet{
			mkSet("h1", 1, "AR", "T1", "AU", "Easy"),
			mkSet("hv2", 100, "AR2", "T2", "AU2", "Easy", "Hard"),
		},
	})

	// hub vs A：Tmp → 恢复组；其余一致不产出（A 自己已升级到 hv2，其
	// presence 无 hv1，无版本更新项——版本更新是「目标仍持旧版」的场景）。
	items, err := s.ReconcileAgainst(ctx, "A", []EntityKind{KindCollection, KindBeatmapSet})
	if err != nil {
		t.Fatalf("ReconcileAgainst(A): %v", err)
	}
	want := map[string]ReconcileItem{}
	for _, it := range items {
		key := string(it.Kind) + "/" + it.ContentKey
		if _, dup := want[key]; dup {
			t.Fatalf("重复结果项 %s", key)
		}
		want[key] = it
	}
	if len(items) != 1 {
		t.Fatalf("结果项数 = %d（%v），期望 1", len(items), items)
	}
	tmp := want["collection/Tmp"]
	if !tmp.Restorable || tmp.DeletedAt != t0.Add(time.Hour).UnixMilli() {
		t.Errorf("Tmp 对账项 = %+v，期望恢复组（默认排除）", tmp)
	}

	// fork B：仍持旧版 hv1、S2、同键不同指纹的 C1、弱键 W1。
	applySnap(t, s, Snapshot{
		ForkID: "B", CollectedAt: t0.Add(2 * time.Hour),
		Collections: []Collection{mkCollection("Fav", "g9", "m1", "m9")},
		BeatmapSets: []BeatmapSet{
			mkSet("hv1", 100, "AR2", "T2", "AU2", "Easy"),
			mkSet("h2", 2, "AR", "T1", "AU", "Normal"),
			mkSet("hw1", -1, "wa", "wt", "wau", "Easy"),
		},
	})
	// fork C：与 W1 同元数据的 W2（弱键疑似配对）。
	applySnap(t, s, Snapshot{
		ForkID: "C", CollectedAt: t0.Add(3 * time.Hour),
		BeatmapSets: []BeatmapSet{mkSet("hw2", -1, "wa", "wt", "wau", "Easy")},
	})
	// fork A 第三次收集：Fav 的 canonical 回到 A 侧值——冲突 = canonical
	// （最近一次收集确立，§4.4）vs 目标 presence，B 侧仍是旧指纹。
	applySnap(t, s, Snapshot{
		ForkID: "A", CollectedAt: t0.Add(4 * time.Hour),
		Collections: []Collection{mkCollection("Fav", "g1", "m1", "m2")},
		BeatmapSets: []BeatmapSet{
			mkSet("h1", 1, "AR", "T1", "AU", "Easy"),
			mkSet("hv2", 100, "AR2", "T2", "AU2", "Easy", "Hard"),
		},
	})

	items, err = s.ReconcileAgainst(ctx, "B", []EntityKind{KindCollection, KindBeatmapSet})
	if err != nil {
		t.Fatalf("ReconcileAgainst(B): %v", err)
	}
	gotCats := map[string]ReconcileItem{}
	for _, it := range items {
		gotCats[string(it.Kind)+"/"+it.ContentKey] = it
	}

	// create：h1 与 hv2（链尾）——历史版本 hv1 不得以 create 出现（HB1
	// 投放差集只取链尾），它以版本更新分类呈现。
	if it, ok := gotCats["beatmap_set/h1"]; !ok || it.Category != ReconcileCreate || it.Restorable {
		t.Errorf("h1 项 = (%v,%+v)，期望 create 且非恢复组", ok, it)
	}
	if _, bad := gotCats["beatmap_set/hv2"]; bad {
		t.Errorf("hv2 已由 hv1 的版本更新项承载（CurrentKey=hv2），不得再单独 create")
	}
	// B 仍持旧版 hv1 → 版本更新（强键，hub 当前 = 链尾 hv2）。
	if it, ok := gotCats["beatmap_set/hv1"]; !ok || it.Category != ReconcileVersionUpdate ||
		it.CurrentKey != "hv2" || it.IdentityKey != "online:100" || it.Evidence != EvidenceStrong {
		t.Errorf("hv1 项 = (%v,%+v)，期望强键版本更新", ok, it)
	}
	// 冲突：Fav 同键不同 MD5 指纹。
	cf, ok := gotCats["collection/Fav"]
	if !ok || cf.Category != ReconcileConflict || len(cf.ConflictFields) != 1 ||
		cf.ConflictFields[0].Field != "BeatmapMD5Hashes" {
		t.Fatalf("Fav 项 = (%v,%+v)，期望单字段冲突", ok, cf)
	}
	if cf.ConflictFields[0].HubValue != "m1,m2" || cf.ConflictFields[0].ForkValue != "m1,m9" {
		t.Errorf("冲突字段值 = %+v", cf.ConflictFields[0])
	}
	// 弱键版本更新：目标 W1 ↔ hub W2，需用户确认。
	wk, ok := gotCats["beatmap_set/hw1"]
	if !ok || wk.Category != ReconcileVersionUpdate || wk.CurrentKey != "hw2" ||
		wk.IdentityKey != "meta:wa|wt|wau" || wk.Evidence != EvidenceWeak {
		t.Fatalf("hw1 项 = (%v,%+v)，期望弱键版本更新", ok, wk)
	}
	if _, bad := gotCats["beatmap_set/hw2"]; bad {
		t.Errorf("弱键配对中 hw2 不得再单独列为 create（去重）")
	}
	// Tmp 对 B 是普通 create（tombstone 是 per-fork 的，A 的删除不影响 B）。
	if it, ok := gotCats["collection/Tmp"]; !ok || it.Category != ReconcileCreate || it.Restorable {
		t.Errorf("Tmp→B 项 = (%v,%+v)，期望 create 且非恢复组（per-fork 生效）", ok, it)
	}
	// B 独有的 h2 不产出（hub→fork 方向无 target-only：hub 已含全部收集过的键）。
	if _, bad := gotCats["beatmap_set/h2"]; bad {
		t.Errorf("h2 在 hub→fork 方向不应产出")
	}

	// Tombstones 数据面：恢复组完整清单（diff 层数据源）。
	tombs, err := s.Tombstones(ctx, "A")
	if err != nil {
		t.Fatalf("Tombstones(A): %v", err)
	}
	if len(tombs[KindCollection]) != 1 {
		t.Errorf("A 的收藏 tombstone = %d 项，期望 1（Tmp）", len(tombs[KindCollection]))
	}
	if len(tombs[KindBeatmapSet]) != 0 {
		t.Errorf("A 的谱面集 tombstone = %d 项，期望 0（hv1 被版本链取代不算删除）", len(tombs[KindBeatmapSet]))
	}
}

// --- 幂等重跑：同快照第二次不再产生增量 ---

func TestApplySnapshotIdempotentRerun(t *testing.T) {
	s := openMem(t)
	snap := Snapshot{
		ForkID: "A", CollectedAt: t0,
		Collections: []Collection{mkCollection("C1", "g1", "m1")},
		BeatmapSets: []BeatmapSet{mkSet("h1", 1, "AR", "TI", "AU", "Easy")},
	}
	applySnap(t, s, snap)
	res := applySnap(t, s, snap)

	if res.TombstonesAdded != 0 || res.TombstonesCleared != 0 || res.VersionLinksCreated != 0 {
		t.Errorf("重跑增量 = (+%d,-%d,链%d)，期望全 0",
			res.TombstonesAdded, res.TombstonesCleared, res.VersionLinksCreated)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM collection`); n != 1 {
		t.Errorf("collection 行数 = %d，期望 1", n)
	}
	if n := countRows(t, s, `SELECT COUNT(*) FROM presence WHERE fork_id='A'`); n != 2 {
		t.Errorf("presence 行数 = %d，期望 2", n)
	}
}

// --- 快照参数校验 ---

func TestApplySnapshotRejectsEmptyFork(t *testing.T) {
	s := openMem(t)
	if _, err := s.ApplySnapshot(context.Background(), Snapshot{ForkID: "", CollectedAt: t0}); err == nil {
		t.Fatal("空 fork_id 应报错")
	}
}

// --- v2 版本化迁移：v1 旧库补齐、幂等、越界拒绝 ---

// buildV1DB 手工构造一个 v1 时代的库文件（v1 DDL + meta=1 + 一行既有数据），
// 模拟 schema v2 发布前落盘的合法中间态。
func buildV1DB(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开 v1 库: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("建 v1 表: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES('schema_version',?)`, "1"); err != nil {
		t.Fatalf("写 v1 版本号: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO collection(content_key, payload, origin_guid, origin_fork, first_seen, updated_at)
VALUES(?,?,?,?,?,?)`, "Fav", `{"Name":"Fav"}`, "g1", "A", 111, 222); err != nil {
		t.Fatalf("插 v1 收藏行: %v", err)
	}
}

func TestMigrateV1ToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	buildV1DB(t, path)

	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v1 库触发迁移: %v", err)
	}
	defer s.Close()
	if v, err := s.SchemaVersion(); err != nil || v != SchemaVersion {
		t.Fatalf("迁移后 schema 版本 = (%d,%v)，期望 (%d,nil)", v, err, SchemaVersion)
	}
	// v1 数据存活。
	if _, _, ok, err := s.LoadCollection(context.Background(), "Fav"); err != nil || !ok {
		t.Fatalf("迁移后收藏 = (%v,%v)，期望存在", ok, err)
	}
	// v2 新表就位：skin/score 可写可读（参数绑定直插验证）。
	if _, err := s.DB().Exec(
		`INSERT INTO skin(content_hash,payload,origin_guid,origin_fork,first_seen,updated_at)
VALUES(?,?,?,?,?,?)`, "sk1", "{}", "g", "A", 0, 0); err != nil {
		t.Fatalf("迁移后写 skin: %v", err)
	}
	if _, err := s.DB().Exec(
		`INSERT INTO score(beatmap_hash,content_hash,payload,origin_guid,origin_fork,first_seen,updated_at)
VALUES(?,?,?,?,?,?,?)`, "bh1", "sc1", "{}", "g", "A", 0, 0); err != nil {
		t.Fatalf("迁移后写 score: %v", err)
	}
	if n := countRows(t, s,
		`SELECT COUNT(*) FROM score WHERE beatmap_hash=? AND content_hash=?`, "bh1", "sc1"); n != 1 {
		t.Errorf("score 组合键行数 = %d，期望 1", n)
	}

	// 幂等：重开不再迁移、数据全部存活。
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重开已迁移库: %v", err)
	}
	defer s2.Close()
	if v, err := s2.SchemaVersion(); err != nil || v != SchemaVersion {
		t.Fatalf("重开后 schema 版本 = (%d,%v)", v, err)
	}
	if n := countRows(t, s2, `SELECT COUNT(*) FROM collection`); n != 1 {
		t.Errorf("重开后 collection 行数 = %d，期望 1", n)
	}
	if n := countRows(t, s2, `SELECT COUNT(*) FROM skin`); n != 1 {
		t.Errorf("重开后 skin 行数 = %d，期望 1", n)
	}
}

func TestMigrateRejectsFutureVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("开库: %v", err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		t.Fatalf("建 v1 表: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO meta(key,value) VALUES('schema_version',?)`, "99"); err != nil {
		t.Fatalf("写版本号: %v", err)
	}
	db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("高于本程序支持的 schema 版本应拒绝打开")
	}
}
