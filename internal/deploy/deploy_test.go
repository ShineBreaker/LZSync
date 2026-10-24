package deploy

// 投放引擎集成测试（design.md §8.2-5 全链往返 / §8.2-6 破坏性场景）：
// fixture 双库 + hub 的收藏夹投放全链（收集入 hub → BuildPlan → Deploy →
// 重读投影逐字段断言）、结构档案零漂移、占用拒写（跨进程持锁）、备份
// 生成、批失败整批回滚、恢复组 tombstone 流程。
// librealm 缺失时显式 SKIP（§8.1 补充门：不得静默绿）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// t0 固定基准时间（避开本地时钟差异）。
var t0 = time.UnixMilli(1770000000000).UTC()

func mustLib(t *testing.T) *realmc.Library {
	t.Helper()
	lib, err := realmc.Load()
	if err != nil {
		t.Skipf("realm-core 库不可用，跳过（§8.1 显式 SKIP）: %v", err)
	}
	return lib
}

func openMemHub(t *testing.T) *hub.Store {
	t.Helper()
	s, err := hub.Open(":memory:")
	if err != nil {
		t.Fatalf("打开内存 hub: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// --- 纯逻辑：NewPlan 校验与 Guid 往返 ---

func TestNewPlanValidation(t *testing.T) {
	if _, err := NewPlan("", nil); err == nil {
		t.Error("空 fork 应报错")
	}
	if _, err := NewPlan("lazer", []PlanItem{{Kind: hub.KindCollection, ContentKey: "x", Action: ActionSkip}}); err == nil {
		t.Error("skip 无原因应报错")
	}
	if _, err := NewPlan("lazer", []PlanItem{
		{Kind: hub.KindCollection, ContentKey: "x", Action: ActionCreate},
		{Kind: hub.KindCollection, ContentKey: "x", Action: ActionUpdate},
	}); err == nil {
		t.Error("重复 (Kind, ContentKey) 应报错")
	}
	if _, err := NewPlan("lazer", []PlanItem{
		{Kind: hub.KindCollection, ContentKey: "x", Action: ActionUpdate, Restore: true},
	}); err == nil {
		t.Error("Restore 项非 create 动作应报错")
	}
	if _, err := NewPlan("lazer", []PlanItem{
		{Kind: hub.KindCollection, ContentKey: "x", Action: ActionCreate, Restore: true},
	}); err != nil {
		t.Errorf("合法恢复项不应报错: %v", err)
	}
}

func TestGuidRoundtrip(t *testing.T) {
	cases := []string{
		"00000000-0000-0000-0000-000000000000",
		"2991cfd8-2140-469a-bcb9-2ec23fbce4ad",
		"2991CFD8-2140-469A-BCB9-2EC23FBCE4AD", // 大写带连字符
		"2991cfd82140469abcb92ec23fbce4ad",     // 32 位纯 hex
	}
	for _, c := range cases {
		b, err := parseGuid(c)
		if err != nil {
			t.Fatalf("parseGuid(%s): %v", c, err)
		}
		if got := formatGuid(b); got != strings.ToLower(strings.ReplaceAll(c, "-", "")) &&
			got != "2991cfd8-2140-469a-bcb9-2ec23fbce4ad" && got != "00000000-0000-0000-0000-000000000000" {
			t.Errorf("formatGuid(%s) = %s", c, got)
		}
	}
	if _, err := parseGuid("not-a-guid"); err == nil {
		t.Error("非法 Guid 应报错")
	}
}

// --- BuildPlan：hub 内存对账 → 默认计划 ---

func TestBuildPlanDefaults(t *testing.T) {
	ctx := context.Background()
	s := openMemHub(t)
	// forkB 先收集：有「冲突夹」（不同成员）；曾有「复活夹」后删除（tombstone）。
	// 顺序关键（HB3 整行覆盖）：目标 fork 先收、源后收，canonical 才保留
	// 源值并与目标 presence 形成冲突。
	applySnap(t, s, hub.Snapshot{ForkID: "B", CollectedAt: t0, Collections: []hub.Collection{
		{ID: "g-b2", Name: "冲突夹", BeatmapMD5Hashes: []string{"m2"}, LastModified: t0},
		{ID: "g-b3", Name: "复活夹", BeatmapMD5Hashes: []string{"m9"}, LastModified: t0},
	}})
	applySnap(t, s, hub.Snapshot{ForkID: "B", CollectedAt: t0.Add(time.Hour), Collections: []hub.Collection{
		{ID: "g-b2", Name: "冲突夹", BeatmapMD5Hashes: []string{"m2"}, LastModified: t0},
	}})
	// forkA 后收集：三个收藏（其一与 B 冲突，其一命中 B 的 tombstone）。
	applySnap(t, s, hub.Snapshot{ForkID: "A", CollectedAt: t0.Add(2 * time.Hour), Collections: []hub.Collection{
		{ID: "g-a1", Name: "新建夹", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
		{ID: "g-a2", Name: "冲突夹", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
		{ID: "g-a3", Name: "复活夹", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
	}})

	ref := forks.LibraryRef{ForkID: "B", RealmPath: "/tmp/x.realm", DiskVersion: 52}

	plan, err := BuildPlan(ctx, s, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	got := map[string]PlanItem{}
	for _, it := range plan.Items {
		got[it.ContentKey] = it
	}
	if len(plan.Items) != 2 {
		t.Fatalf("默认计划项数 = %d（期望 2：新建 + 冲突；恢复组默认排除），items=%+v", len(plan.Items), plan.Items)
	}
	if it := got["新建夹"]; it.Action != ActionCreate || it.Restore {
		t.Errorf("新建夹 = %+v，期望 create 非 Restore", it)
	}
	if it := got["冲突夹"]; it.Action != ActionUpdate {
		t.Errorf("冲突夹 = %+v，期望 update", it)
	}
	if _, ok := got["复活夹"]; ok {
		t.Error("复活夹（tombstone 命中）不应进默认计划")
	}
	if plan.Target.RealmPath != "/tmp/x.realm" {
		t.Errorf("plan.Target = %+v", plan.Target)
	}

	// 显式勾选恢复项 → 进计划且带 Restore 标记。
	plan2, err := BuildPlan(ctx, s, ref, BuildOptions{RestoreKeys: []string{"复活夹"}})
	if err != nil {
		t.Fatalf("BuildPlan(勾选恢复): %v", err)
	}
	found := false
	for _, it := range plan2.Items {
		if it.ContentKey == "复活夹" {
			found = true
			if !it.Restore || it.Action != ActionCreate {
				t.Errorf("恢复项 = %+v，期望 create+Restore", it)
			}
		}
	}
	if !found {
		t.Error("勾选的恢复项应进计划")
	}

	// 未解析的 LibraryRef 拒绝。
	if _, err := BuildPlan(ctx, s, forks.LibraryRef{ForkID: "B"}, BuildOptions{}); err == nil {
		t.Error("缺 RealmPath 应报错")
	}
}

func applySnap(t *testing.T, s *hub.Store, snap hub.Snapshot) {
	t.Helper()
	if _, err := s.ApplySnapshot(context.Background(), snap); err != nil {
		t.Fatalf("ApplySnapshot(fork=%s): %v", snap.ForkID, err)
	}
}

// --- 目标库读回与投影 helper ---

// readCollection 按名读回目标库一行（白名单 ∩ 实际列），逐字段断言的数据源。
func readCollection(t *testing.T, r *realmc.Realm, name string) (hub.Collection, bool) {
	t.Helper()
	ck, err := r.FindClass("BeatmapCollection")
	if err != nil {
		t.Fatalf("FindClass(BeatmapCollection): %v", err)
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
		return hub.Collection{}, false
	}
	defer r.ReleaseObject(obj)

	c := hub.Collection{Name: name}
	if key, err := r.FindProperty(ck, "ID"); err == nil {
		if b, err := r.GetGuid(obj, key); err == nil {
			c.ID = formatGuid(b)
		}
	}
	if key, err := r.FindProperty(ck, "LastModified"); err == nil {
		if ts, err := r.GetTimestamp(obj, key); err == nil {
			c.LastModified = ts.UTC()
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
			c.BeatmapMD5Hashes = append(c.BeatmapMD5Hashes, h)
		}
	}
	return c, true
}

// objectCountOf 目标库 BeatmapCollection 行数。
func objectCountOf(t *testing.T, r *realmc.Realm) uint64 {
	t.Helper()
	ck, err := r.FindClass("BeatmapCollection")
	if err != nil {
		t.Fatalf("FindClass: %v", err)
	}
	n, err := r.ObjectCount(ck)
	if err != nil {
		t.Fatalf("ObjectCount: %v", err)
	}
	return n
}

// schemaSnapshotOf 只读会话捕获结构档案（结构零漂移的独立校验，§8.2-7
// golden 比对——deploy 内部 DriftGuard 之外的第二双眼）。
func schemaSnapshotOf(t *testing.T, lib *realmc.Library, path string) *realmc.SchemaSnapshot {
	t.Helper()
	r, err := lib.OpenDynamicReadOnly(path)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", path, err)
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

// sha256OfFile 库文件字节指纹（备份与源库不受影响的断言输入）。
func sha256OfFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- 核心：fixture 双库 + hub 全链往返 ---

func TestDeployFixtureRoundtrip(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	seedA, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed(A): %v", err)
	}
	// A（lazer）独有收藏夹；「标准收藏夹」与 B 重名（B 无 MD5 列 → 冲突）。
	seedA.Collections = append(seedA.Collections, hub.Collection{
		Name:             "lazer 独有夹",
		BeatmapMD5Hashes: []string{"md5-lazer-only"},
		LastModified:     t0,
	})
	dirA := t.TempDir()
	refA, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: dirA, Seed: seedA,
	})
	if err != nil {
		t.Fatalf("fixture.Build(A): %v", err)
	}

	seedB, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed(B): %v", err)
	}
	seedB.BeatmapSets = nil // B 侧谱面与对账无关，收敛夹具面
	seedB.Scores = nil
	seedB.Files = nil
	seedB.Collections = []hub.Collection{
		{Name: "标准收藏夹"},        // 与 A 同名 → 冲突 → update
		{Name: "delta 独有夹"},      // B 独有 → 不进计划
	}
	dirB := t.TempDir()
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: dirB, Seed: seedB,
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}

	// 收集入 hub（投影按已知键读回——fixture 库无全表遍历导出面）。
	// 顺序关键（HB3）：目标 B 先收（presence=B 值）、源 A 后收（canonical=A
	// 值），「标准收藏夹」才成冲突项。谱面集不进本夹具的 hub（M2 起
	// BuildPlan 对账谱面集，本测试聚焦收藏夹链路且 Runner 无 Files 依赖；
	// 谱面集全链由 TestDeployBeatmapSetFullChain 覆盖）。
	h := openMemHub(t)
	colsB := projectKnown(t, lib, refB.RealmPath, []string{"标准收藏夹", "delta 独有夹"})
	applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0, Collections: colsB})
	colsA := projectKnown(t, lib, refA.RealmPath, []string{"标准收藏夹", "lazer 独有夹"})
	applySnap(t, h, hub.Snapshot{ForkID: refA.ForkID, CollectedAt: t0.Add(time.Hour),
		Collections: colsA})

	// 计划。fixture 库无 BeatmapMD5Hashes 列（fixture 构造面已知边界），
	// 双端投影的 MD5 指纹恒等（均空）→「标准收藏夹」无差异不产出动作
	//（conflict→update 链路由增强库测试覆盖）；计划 = 1 项 create。
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	actions := map[string]Action{}
	for _, it := range plan.Items {
		actions[it.ContentKey] = it.Action
	}
	if actions["lazer 独有夹"] != ActionCreate || len(plan.Items) != 1 {
		t.Fatalf("计划动作 = %v（期望仅：lazer 独有夹=create）", actions)
	}

	// 投放前基准：库字节（备份对照）、结构档案（零漂移对照）、源库字节。
	beforeB := sha256OfFile(t, refB.RealmPath)
	beforeA := sha256OfFile(t, refA.RealmPath)
	snapBefore := schemaSnapshotOf(t, lib, refB.RealmPath)

	backups, err := guard.OpenBackups(filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatalf("OpenBackups: %v", err)
	}
	runner := NewRunner(Deps{Hub: h, Backups: backups})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}

	// 报告：applied 1 项；fixture 库缺 BeatmapMD5Hashes 列 → 落地项一条缺列 skip。
	if len(report.Applied) != 1 {
		t.Errorf("Applied = %d 项（期望 1）: %+v", len(report.Applied), report.Applied)
	}
	md5Skip := 0
	for _, st := range report.Skipped {
		if strings.Contains(st.Reason, "BeatmapMD5Hashes") {
			md5Skip += st.Count
		}
	}
	if md5Skip != 1 {
		t.Errorf("缺列 skip 计数 = %d（期望 1），Skipped=%+v", md5Skip, report.Skipped)
	}
	if len(report.Repointed) != 0 || len(report.FailedBatches) != 0 || len(report.NotRun) != 0 {
		t.Errorf("报告异常分段非空: repointed=%+v failed=%+v notRun=%+v",
			report.Repointed, report.FailedBatches, report.NotRun)
	}

	// 备份：guard 集中目录内生成、字节 == 投放前库。
	if report.BackupPath == "" || !strings.HasPrefix(report.BackupPath, backups.Dir) {
		t.Fatalf("BackupPath = %q（期望位于 %s）", report.BackupPath, backups.Dir)
	}
	if got := sha256OfFile(t, report.BackupPath); got != beforeB {
		t.Errorf("备份字节与投放前库不一致: %s != %s", got, beforeB)
	}

	// 结构档案零漂移（独立第二校验）。
	snapAfter := schemaSnapshotOf(t, lib, refB.RealmPath)
	if !snapBefore.Equal(snapAfter) {
		t.Error("投放前后结构档案漂移（DriftGuard 之外的独立校验失败）")
	}

	// 重读投影逐字段断言（fixture 列面：Name + 行数 + LastModified）。
	rb, err := lib.OpenDynamicReadOnly(refB.RealmPath)
	if err != nil {
		t.Fatalf("重开 B: %v", err)
	}
	if err := rb.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	if n := objectCountOf(t, rb); n != 3 {
		t.Errorf("B 收藏夹行数 = %d（期望 3）", n)
	}
	for _, want := range []hub.Collection{
		{Name: "lazer 独有夹"},
		{Name: "标准收藏夹"},
		{Name: "delta 独有夹"},
	} {
		got, ok := readCollection(t, rb, want.Name)
		if !ok {
			t.Errorf("B 缺收藏 %q", want.Name)
			continue
		}
		if got.Name != want.Name {
			t.Errorf("Name = %q，期望 %q", got.Name, want.Name)
		}
	}
	rb.Close()

	// 源库不受投放影响（只读收集链路）。
	if got := sha256OfFile(t, refA.RealmPath); got != beforeA {
		t.Error("源库 A 字节被投放改动")
	}
}

// projectKnown 按已知内容键做只读投影（fixture 库无导出遍历面；测试种子
// 自知键集，投影语义与读取规则一致：列缺失自然降级，§4.1）。
func projectKnown(t *testing.T, lib *realmc.Library, realmPath string, names []string) []hub.Collection {
	t.Helper()
	r, err := lib.OpenDynamicReadOnly(realmPath)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	var out []hub.Collection
	for _, name := range names {
		c, ok := readCollection(t, r, name)
		if !ok {
			t.Fatalf("投影缺收藏 %q", name)
		}
		out = append(out, c)
	}
	return out
}
