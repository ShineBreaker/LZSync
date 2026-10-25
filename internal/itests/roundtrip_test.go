package itests

// 收藏夹全链往返（design §7-M1-7 / §8.2-5）：收集（真实 collect.Runner）
// → diff（BuildPlan）→ 投放（deploy.Runner）→ 目标库重读投影与预期逐字段
// 相等（Guid / Name / MD5 逐元素 / LastModified——增强形态库承载完整列面）；
// 目标结构档案写前后零差异；备份生成断言（S2）；恢复组 tombstone 全链
//（默认不勾选 → 显式勾选 → 投放成功即清除，HB2/§5.4 删除不复活）。
// 占用拦截在 occupied_test.go（S5）。

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

func TestCollectionFullChainRoundtrip(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	fav1 := hub.Collection{
		ID: "11111111-1111-1111-1111-111111111111", Name: "满配夹",
		BeatmapMD5Hashes: []string{"md5-a", "md5-b", "md5-c"}, LastModified: t0,
	}
	fav2 := hub.Collection{
		ID: "22222222-2222-2222-2222-222222222222", Name: "第二夹",
		BeatmapMD5Hashes: []string{"md5-z"}, LastModified: t0,
	}

	// ① 收集：增强形态源库（完整列面）→ hub canonical。
	srcRef := buildFullLib(t, lib, "lazer")
	seedFullCollection(t, lib, srcRef.RealmPath, fav1)
	seedFullCollection(t, lib, srcRef.RealmPath, fav2)
	rep := e.collectInto(srcRef)
	if got := rep.Entities[hub.KindCollection]; got != 2 {
		t.Fatalf("收藏入仓 = %d，期望 2", got)
	}
	got, origin, ok, err := e.hub.LoadCollection(ctx, "满配夹")
	if err != nil || !ok {
		t.Fatalf("LoadCollection(满配夹) = ok=%v err=%v", ok, err)
	}
	if !guidEqual(got.ID, fav1.ID) || !equalUnordered(got.BeatmapMD5Hashes, fav1.BeatmapMD5Hashes) || !got.LastModified.Equal(t0) {
		t.Errorf("收集后 canonical = %+v，期望逐字段 == %v", got, fav1)
	}
	if origin.Fork != "lazer" || !guidEqual(origin.GUID, fav1.ID) {
		t.Errorf("canonical 来源 = %+v，期望 {lazer, 源 Guid}（HB3）", origin)
	}

	// ② diff → 计划：目标库（delta 座）空 → 2 create。
	dstRef := buildFullLib(t, lib, "delta")
	plan, err := deploy.BuildPlan(ctx, e.hub, dstRef, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("计划项 = %d（期望 2 create）: %+v", len(plan.Items), plan.Items)
	}
	for _, it := range plan.Items {
		if it.Action != deploy.ActionCreate || it.Restore {
			t.Errorf("计划项 %s = %+v，期望 create 非 Restore", it.ContentKey, it)
		}
	}

	// ③ 投放（备份与零漂移断言的输入：投放前字节与结构档案）。
	beforeBytes := sha256OfFile(t, dstRef.RealmPath)
	snapBefore := schemaSnapshotOf(t, lib, dstRef.RealmPath)
	report, err := e.deployRunner().Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 2 || len(report.FailedBatches) != 0 || len(report.Repointed) != 0 {
		t.Fatalf("报告 = applied %+v failed %+v repointed %+v，期望 2 applied 零异常",
			report.Applied, report.FailedBatches, report.Repointed)
	}
	// 备份：集中目录内生成、字节 == 投放前库（S2）。
	if report.BackupPath == "" || !strings.HasPrefix(report.BackupPath, e.backups.Dir) {
		t.Fatalf("BackupPath = %q，期望位于 %s", report.BackupPath, e.backups.Dir)
	}
	if got := sha256OfFile(t, report.BackupPath); got != beforeBytes {
		t.Errorf("备份字节 %s != 投放前库 %s", got, beforeBytes)
	}
	// 结构档案写前后零差异（DriftGuard 之外的独立第二校验，§8.2-7）。
	if !snapBefore.Equal(schemaSnapshotOf(t, lib, dstRef.RealmPath)) {
		t.Error("投放前后目标结构档案漂移")
	}

	// ④ 重读投影逐字段相等。
	assertCollectionEqual(t, lib, dstRef.RealmPath, fav1)
	assertCollectionEqual(t, lib, dstRef.RealmPath, fav2)

	// ⑤ 冲突→update 半链（§0-B2/B4：canonical 首见即定——第三方 fork 收
	// 集的同名异值收藏不改 canonical 且 BuildPlan 冲突默认不落地；update
	// 覆盖路径由显式构造的动作项承载，「canonical 为新值」场景换第二个
	// hub 重演）：收集目标库（presence 建立）→ g0v0 收集进 hub2 → 显式
	// update 计划投放 → 目标 Guid/LastModified 保目标值、成员更新为
	// canonical（§0-B7：LastModified 是端本地字段，就地更新不覆写）。
	e.collectInto(dstRef)
	fav1v2 := hub.Collection{
		ID: "33333333-3333-3333-3333-333333333333", Name: "满配夹",
		BeatmapMD5Hashes: []string{"md5-a", "md5-b", "md5-c", "md5-d"},
		LastModified:     t0.Add(2 * time.Hour),
	}
	src2 := buildFullLib(t, lib, "g0v0")
	seedFullCollection(t, lib, src2.RealmPath, fav1v2)
	h2, err := hub.Open(filepath.Join(t.TempDir(), "hub2.db"))
	if err != nil {
		t.Fatalf("hub.Open(h2): %v", err)
	}
	defer h2.Close()
	if _, err := collect.NewRunner(collect.Deps{
		Hub: h2, Files: e.engine, Snapshots: e.snaps, Guard: guard.NewGuard(nil),
	}).Collect(ctx, src2, nil); err != nil {
		t.Fatalf("h2 收集 g0v0: %v", err)
	}

	plan2, err := deploy.NewPlan(dstRef.ForkID, []deploy.PlanItem{{
		Kind: hub.KindCollection, ContentKey: "满配夹",
		Action: deploy.ActionUpdate, Summary: "收藏夹「满配夹」",
	}})
	if err != nil {
		t.Fatalf("NewPlan#2: %v", err)
	}
	plan2.Target = dstRef
	report2, err := deploy.NewRunner(deploy.Deps{
		Hub: h2, Backups: e.backups, Guard: guard.NewGuard(nil),
	}).Deploy(ctx, plan2, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 1 || report2.Applied[0].Action != deploy.ActionUpdate {
		t.Errorf("Deploy#2 applied = %+v，期望 1 update", report2.Applied)
	}
	got2, ok := readFullCollection(t, lib, dstRef.RealmPath, "满配夹")
	if !ok {
		t.Fatal("update 后读回缺「满配夹」")
	}
	if !guidEqual(got2.ID, fav1.ID) {
		t.Errorf("就地更新后 Guid = %s，期望保持目标值 %s（§4.2 保目标 Guid）", got2.ID, fav1.ID)
	}
	if !equalUnordered(got2.BeatmapMD5Hashes, fav1v2.BeatmapMD5Hashes) {
		t.Errorf("update 后 MD5 = %v，期望 %v", got2.BeatmapMD5Hashes, fav1v2.BeatmapMD5Hashes)
	}
	if !got2.LastModified.Equal(fav1.LastModified) {
		t.Errorf("update 后 LastModified = %v，期望保目标原值 %v（§0-B7 端本地字段不写）",
			got2.LastModified, fav1.LastModified)
	}
}

// TestCollectionTombstoneRestoreFullChain 恢复组全链（HB2/§5.4）：
// 投放落地 → 游戏侧删除收藏夹 → 再收集生成 tombstone → 默认计划排除
//（「恢复（已删除）」默认不勾选）→ 显式勾选投放 → 复活且 tombstone 同步清除。
func TestCollectionTombstoneRestoreFullChain(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	fav := hub.Collection{
		ID: "66666666-6666-6666-6666-666666666666", Name: "复活夹",
		BeatmapMD5Hashes: []string{"md5-r1", "md5-r2"}, LastModified: t0,
	}
	srcRef := buildFullLib(t, lib, "lazer")
	seedFullCollection(t, lib, srcRef.RealmPath, fav)
	e.collectInto(srcRef)

	dstRef := buildFullLib(t, lib, "delta")
	plan, err := deploy.BuildPlan(ctx, e.hub, dstRef, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := e.deployRunner().Deploy(ctx, plan, nil); err != nil {
		t.Fatalf("首次 Deploy: %v", err)
	}

	// 收集目标库 → presence；游戏侧删除（收藏夹整行 Remove，官方语义）；
	// 再收集 → tombstone。
	e.collectInto(dstRef)
	deleteCollectionRow(t, lib, dstRef.RealmPath, fav.Name)
	e.collectInto(dstRef)
	if got := e.tombstoneCount("delta", hub.KindCollection, fav.Name); got != 1 {
		t.Fatalf("删除后再收集 tombstone = %d，期望 1", got)
	}

	// 默认计划：恢复组排除（默认不勾选，§6.2）。
	planDefault, err := deploy.BuildPlan(ctx, e.hub, dstRef, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan(默认): %v", err)
	}
	if len(planDefault.Items) != 0 {
		t.Fatalf("默认计划 = %+v，期望空（唯一项命中 tombstone）", planDefault.Items)
	}

	// 显式勾选 → 投放成功 → tombstone 同步清除 + 复活。
	planRestore, err := deploy.BuildPlan(ctx, e.hub, dstRef, deploy.BuildOptions{RestoreKeys: []string{fav.Name}})
	if err != nil {
		t.Fatalf("BuildPlan(勾选恢复): %v", err)
	}
	if len(planRestore.Items) != 1 || !planRestore.Items[0].Restore || planRestore.Items[0].Action != deploy.ActionCreate {
		t.Fatalf("恢复计划 = %+v，期望 1 项 create+Restore", planRestore.Items)
	}
	report, err := e.deployRunner().Deploy(ctx, planRestore, nil)
	if err != nil {
		t.Fatalf("恢复 Deploy: %v", err)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("恢复 Applied = %+v，期望 1", report.Applied)
	}
	if got := e.tombstoneCount("delta", hub.KindCollection, fav.Name); got != 0 {
		t.Errorf("恢复投放成功后 tombstone = %d，期望同步清除（§5.4 确认动作即清除）", got)
	}
	assertCollectionEqual(t, lib, dstRef.RealmPath, fav)
}
