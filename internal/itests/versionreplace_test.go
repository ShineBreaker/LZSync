package itests

// B1 验收（design.md §5.4 版本更新三步 / 施工块 4）：目标有旧版本且旧难度
// 挂本地成绩、hub 有同 OnlineID 强键新版本 → 对账 version-update → 计划
// ActionVersionReplace → 投放后四组断言：① 目标恰有一个未软删版本（新
// Hash）；② 旧集 DeletePending=true（软删行保留，物理清理由客户端兑现）；
// ③ 旧难度成绩关系置 null 且 BeatmapHash 字符串列保留；④ 无孤儿难度行
//（新旧难度各挂各的父集，行数符合预期）。链路走真实编排（collect.Runner
// / deploy.Runner / hub 对账）。
//
// 成绩关系由现场注入：fixture 已知边界——播 Score 行但不预播 BeatmapInfo
// 关系（builder.go seedScores），真实游戏侧该关系由客户端挂接。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// TestVersionReplaceUnbindsLocalScores B1 主链：强键版本更新投放后新版本
// 落地、旧集软删、旧难度成绩解绑（BeatmapHash 保留）、无孤儿行；重续幂等。
func TestVersionReplaceUnbindsLocalScores(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	seedV1, seedV2, err := fixture.VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	v1, v2 := seedV1.BeatmapSets[0], seedV2.BeatmapSets[0]

	// 本地成绩挂 v1 首难度（回放字节并入池，File 行随 fixture 播种）。
	files := make(map[string][]byte, len(seedV1.Files)+1)
	for h, b := range seedV1.Files {
		files[h] = b
	}
	score, err := fixture.MaterializeScore(v1.Beatmaps[0], "lazer 2026.1001.0",
		[]byte("version replace replay bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeScore: %v", err)
	}

	// 目标库现场：v1 整链 + 本地成绩；注入 Score.BeatmapInfo → v1 难度。
	refB := buildLib(t, fixture.ForkDelta, fixture.VersionDelta, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{v1},
		Scores:      []fixture.SeedScore{score},
		Files:       files,
	})
	linkScoreToBeatmap(t, lib, refB.RealmPath, v1.Beatmaps[0].Hash)

	// 真实收集建立 presence；源座收集 v2（同 OnlineID 强键 → 版本链 v1→v2）。
	e.collectInto(refB)
	refV2 := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, seedV2)
	e.collectInto(refV2)

	// 对账：恰一项 version-update（强键：目标旧键 → hub 链尾新键）。
	items := e.reconcile(fixture.ForkDelta, hub.KindBeatmapSet)
	if len(items) != 1 {
		t.Fatalf("对账 = %d 项（%+v），期望仅 version-update", len(items), items)
	}
	if ri := items[0]; ri.Category != hub.ReconcileVersionUpdate ||
		ri.Evidence != hub.EvidenceStrong ||
		ri.ContentKey != v1.Hash || ri.CurrentKey != v2.Hash {
		t.Fatalf("对账项 = %+v，期望 strong version-update %s→%s", ri, v1.Hash, v2.Hash)
	}

	// 计划面：强键默认进（弱键默认不进的面由 deploy 包单测覆盖）。
	plan, err := deploy.BuildPlan(ctx, e.hub, refB, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 ||
		plan.Items[0].Action != deploy.ActionVersionReplace ||
		plan.Items[0].OldContentKey != v1.Hash ||
		plan.Items[0].ContentKey != v2.Hash {
		t.Fatalf("计划 = %+v，期望 1 项 version-replace（old=v1 new=v2）", plan.Items)
	}

	// 新版本实体入仓（files CopyMissing 先行的源，S6）。
	putToStore(t, e, seedV2.Files)

	runner := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 1 || report.Applied[0].Action != deploy.ActionVersionReplace {
		t.Fatalf("Applied = %+v（期望 1 项 version-replace）", report.Applied)
	}

	// ---- B1 四组断言 ----
	assertVersionReplaceOutcome(t, lib, refB.RealmPath, v1, v2, score, files)

	// 重续幂等：同计划重跑 → 新在+旧已软删 → 跳过计数、零 applied、形态不变。
	report2, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %+v（期望 0）", report2.Applied)
	}
	if len(report2.Skipped) != 1 || report2.Skipped[0].Count != 1 {
		t.Errorf("重跑 Skipped = %+v（期望 1 条计数 1）", report2.Skipped)
	}
	assertVersionReplaceOutcome(t, lib, refB.RealmPath, v1, v2, score, files)
}

// TestVersionReplaceRepointed 执行期现场复核改判：计划后、投放前第三方已
// 向目标写入新版本裸行 → 跳过重建只补软删与解绑（②③），改判入报告，
// 新裸行不被 canonical 覆盖（非 LZSync 写面，尊重现场）。
func TestVersionReplaceRepointed(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	seedV1, seedV2, err := fixture.VersionChangeSeeds()
	if err != nil {
		t.Fatalf("VersionChangeSeeds: %v", err)
	}
	v1, v2 := seedV1.BeatmapSets[0], seedV2.BeatmapSets[0]

	files := make(map[string][]byte, len(seedV1.Files)+1)
	for h, b := range seedV1.Files {
		files[h] = b
	}
	score, err := fixture.MaterializeScore(v1.Beatmaps[0], "lazer 2026.1001.0",
		[]byte("repointed replay bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeScore: %v", err)
	}
	refB := buildLib(t, fixture.ForkDelta, fixture.VersionDelta, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{v1},
		Scores:      []fixture.SeedScore{score},
		Files:       files,
	})
	linkScoreToBeatmap(t, lib, refB.RealmPath, v1.Beatmaps[0].Hash)
	e.collectInto(refB)
	refV2 := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, seedV2)
	e.collectInto(refV2)

	plan, err := deploy.BuildPlan(ctx, e.hub, refB, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	putToStore(t, e, seedV2.Files)

	// 计划后、投放前：第三方写入新版本裸行（DateAdded 异于 canonical）。
	insertBareSetRow(t, lib, refB.RealmPath, v2.Hash)

	runner := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 1 || report.Applied[0].Action != deploy.ActionVersionReplace {
		t.Fatalf("Applied = %+v（期望 1 项 version-replace）", report.Applied)
	}
	if len(report.Repointed) != 1 {
		t.Fatalf("Repointed = %+v（期望 1 条改判）", report.Repointed)
	}

	// ②③ 照常兑现：旧集软删 + 成绩解绑；新裸行保持第三方形态。
	withRO(t, lib, refB.RealmPath, func(r *realmc.Realm) {
		if ok := setDeletePending(t, r, v1.Hash); !ok {
			t.Error("改判后旧版本行消失（期望软删保留）")
		}
		assertScoreUnbound(t, r, score)
		ck, _ := r.FindClass("BeatmapSet")
		hashKey, _ := r.FindProperty(ck, "Hash")
		dateKey, _ := r.FindProperty(ck, "DateAdded")
		obj, err := r.FindFirstByString(ck, hashKey, v2.Hash)
		if err != nil || obj == nil {
			t.Fatalf("读新裸行: %v", err)
		}
		defer r.ReleaseObject(obj)
		got, err := r.GetTimestamp(obj, dateKey)
		if err != nil {
			t.Fatalf("读新裸行 DateAdded: %v", err)
		}
		if got.Equal(v2.DateAdded) {
			t.Error("改判分支不应覆盖第三方新裸行的标量（跳过重建）")
		}
	})
}

// ---- 断言与注入辅助 ----

// assertVersionReplaceOutcome B1 四组断言：① 恰一个未软删版本（新 Hash）；
// ② 旧集软删；③ 成绩关系 null 且 BeatmapHash 保留；④ 无孤儿行。
func assertVersionReplaceOutcome(t *testing.T, lib *realmc.Library, realmPath string,
	v1, v2 hub.BeatmapSet, score fixture.SeedScore, seededFiles map[string][]byte) {
	t.Helper()
	withRO(t, lib, realmPath, func(r *realmc.Realm) {
		setCK, err := r.FindClass("BeatmapSet")
		if err != nil {
			t.Fatalf("FindClass(BeatmapSet): %v", err)
		}
		hashKey, err := r.FindProperty(setCK, "Hash")
		if err != nil {
			t.Fatalf("FindProperty(Hash): %v", err)
		}
		dpKey, err := r.FindProperty(setCK, "DeletePending")
		if err != nil {
			t.Fatalf("FindProperty(DeletePending): %v", err)
		}

		// ①④ 恰一个未软删版本（= v2）；总行数 = 新旧各一。
		live, total := 0, uint64(0)
		var liveHash string
		if err := r.ForEach(setCK, func(obj unsafe.Pointer) error {
			total++
			dp, err := r.GetBool(obj, dpKey)
			if err != nil {
				return err
			}
			if !dp {
				live++
				h, err := r.GetString(obj, hashKey)
				if err != nil {
					return err
				}
				liveHash = h
			}
			return nil
		}); err != nil {
			t.Fatalf("遍历 BeatmapSet: %v", err)
		}
		if total != 2 {
			t.Errorf("BeatmapSet 行数 = %d（期望 2——旧软删保留 + 新版本）", total)
		}
		if live != 1 || liveHash != v2.Hash {
			t.Errorf("未软删版本 = %d 个（首个 %q），期望恰 1 个 = 新 Hash %q", live, liveHash, v2.Hash)
		}

		// ② 旧集软删（非物理删）。
		if !setDeletePending(t, r, v1.Hash) {
			t.Error("旧版本集 DeletePending = false，期望 true（§5.4 三步之②）")
		}

		// ③ 成绩解绑：关系 null、BeatmapHash 保留。
		assertScoreUnbound(t, r, score)

		// ④ 无孤儿难度行：新旧难度各挂各的父集（软删行子行生命周期挂父集）。
		if got := rowCount(t, r, "Beatmap"); got != uint64(len(v1.Beatmaps)+len(v2.Beatmaps)) {
			t.Errorf("Beatmap 行数 = %d（期望 %d）", got, len(v1.Beatmaps)+len(v2.Beatmaps))
		}
		if got := rowCount(t, r, "BeatmapMetadata"); got != uint64(len(v1.Beatmaps)+len(v2.Beatmaps)) {
			t.Errorf("BeatmapMetadata 行数 = %d（期望 %d）", got, len(v1.Beatmaps)+len(v2.Beatmaps))
		}
		if got := rowCount(t, r, "Score"); got != 1 {
			t.Errorf("Score 行数 = %d（期望 1——解绑不动行本身）", got)
		}
		wantFiles := uint64(len(seededFiles) + len(v2.Files))
		if got := rowCount(t, r, "File"); got != wantFiles {
			t.Errorf("File 行数 = %d（期望 %d——目标池 + 新版本实体）", got, wantFiles)
		}
		// 新版本难度挂新父集（Beatmaps 列表逐行回收）。
		if got := setBeatmapCount(t, r, v2.Hash); got != len(v2.Beatmaps) {
			t.Errorf("新版本 Beatmaps 列表 = %d（期望 %d）", got, len(v2.Beatmaps))
		}
	})
}

// assertScoreUnbound ③ 的读回：BeatmapInfo 关系为 NULL、BeatmapHash 列值
// 保留原难度哈希（历史锚点）。
func assertScoreUnbound(t *testing.T, r *realmc.Realm, score fixture.SeedScore) {
	t.Helper()
	scoreCK, err := r.FindClass("Score")
	if err != nil {
		t.Fatalf("FindClass(Score): %v", err)
	}
	hashKey, err := r.FindProperty(scoreCK, "Hash")
	if err != nil {
		t.Fatalf("FindProperty(Score.Hash): %v", err)
	}
	obj, err := r.FindFirstByString(scoreCK, hashKey, score.Hash)
	if err != nil || obj == nil {
		t.Fatalf("读回成绩行 %s: %v", score.Hash, err)
	}
	defer r.ReleaseObject(obj)
	linkKey, err := r.FindProperty(scoreCK, "BeatmapInfo")
	if err != nil {
		t.Fatalf("FindProperty(Score.BeatmapInfo): %v", err)
	}
	null, err := r.IsNull(obj, linkKey)
	if err != nil {
		t.Fatalf("判空 BeatmapInfo: %v", err)
	}
	if !null {
		t.Error("成绩 BeatmapInfo 关系非 null，期望已解绑（§5.4 三步之③）")
	}
	bmHashKey, err := r.FindProperty(scoreCK, "BeatmapHash")
	if err != nil {
		t.Fatalf("FindProperty(Score.BeatmapHash): %v", err)
	}
	got, err := r.GetString(obj, bmHashKey)
	if err != nil {
		t.Fatalf("读 BeatmapHash: %v", err)
	}
	if got != score.BeatmapHash {
		t.Errorf("成绩 BeatmapHash = %q，期望保留 %q（历史锚点）", got, score.BeatmapHash)
	}
}

// setDeletePending 读指定集行的 DeletePending（行缺失返回 false 并报错）。
func setDeletePending(t *testing.T, r *realmc.Realm, setHash string) bool {
	t.Helper()
	ck, err := r.FindClass("BeatmapSet")
	if err != nil {
		t.Fatalf("FindClass(BeatmapSet): %v", err)
	}
	hashKey, err := r.FindProperty(ck, "Hash")
	if err != nil {
		t.Fatalf("FindProperty(Hash): %v", err)
	}
	obj, err := r.FindFirstByString(ck, hashKey, setHash)
	if err != nil || obj == nil {
		t.Fatalf("读集行 %s: %v", setHash, err)
	}
	defer r.ReleaseObject(obj)
	dpKey, err := r.FindProperty(ck, "DeletePending")
	if err != nil {
		t.Fatalf("FindProperty(DeletePending): %v", err)
	}
	dp, err := r.GetBool(obj, dpKey)
	if err != nil {
		t.Fatalf("读 DeletePending: %v", err)
	}
	return dp
}

// rowCount 某 class 行数。
func rowCount(t *testing.T, r *realmc.Realm, cls string) uint64 {
	t.Helper()
	ck, err := r.FindClass(cls)
	if err != nil {
		t.Fatalf("FindClass(%s): %v", cls, err)
	}
	n, err := r.ObjectCount(ck)
	if err != nil {
		t.Fatalf("ObjectCount(%s): %v", cls, err)
	}
	return n
}

// setBeatmapCount 集行 Beatmaps 列表长度。
func setBeatmapCount(t *testing.T, r *realmc.Realm, setHash string) int {
	t.Helper()
	ck, err := r.FindClass("BeatmapSet")
	if err != nil {
		t.Fatalf("FindClass(BeatmapSet): %v", err)
	}
	hashKey, err := r.FindProperty(ck, "Hash")
	if err != nil {
		t.Fatalf("FindProperty(Hash): %v", err)
	}
	lstKey, err := r.FindProperty(ck, "Beatmaps")
	if err != nil {
		t.Fatalf("FindProperty(Beatmaps): %v", err)
	}
	obj, err := r.FindFirstByString(ck, hashKey, setHash)
	if err != nil || obj == nil {
		t.Fatalf("读集行 %s: %v", setHash, err)
	}
	defer r.ReleaseObject(obj)
	lst, err := r.GetList(obj, lstKey)
	if err != nil {
		t.Fatalf("GetList(Beatmaps): %v", err)
	}
	defer lst.Close()
	n, err := lst.Size()
	if err != nil {
		t.Fatalf("Size(Beatmaps): %v", err)
	}
	return int(n)
}

// linkScoreToBeatmap 现场注入：Score.BeatmapInfo 关系 → 指定 Hash 的难度行
// （模拟真实客户端挂接成绩；fixture 不预播此关系）。注入后即时读回验证。
func linkScoreToBeatmap(t *testing.T, lib *realmc.Library, realmPath, beatmapHash string) {
	t.Helper()
	mutate(t, lib, realmPath, func(r *realmc.Realm) error {
		scoreCK, err := r.FindClass("Score")
		if err != nil {
			return err
		}
		bmCK, err := r.FindClass("Beatmap")
		if err != nil {
			return err
		}
		bmHashKey, err := r.FindProperty(bmCK, "Hash")
		if err != nil {
			return err
		}
		bmObj, err := r.FindFirstByString(bmCK, bmHashKey, beatmapHash)
		if err != nil || bmObj == nil {
			t.Fatalf("注入锚定：找不到 Hash=%s 的难度行: %v", beatmapHash, err)
		}
		defer r.ReleaseObject(bmObj)
		scoreHashKey, err := r.FindProperty(scoreCK, "BeatmapHash")
		if err != nil {
			return err
		}
		scoreObj, err := r.FindFirstByString(scoreCK, scoreHashKey, beatmapHash)
		if err != nil || scoreObj == nil {
			t.Fatalf("注入锚定：找不到 BeatmapHash=%s 的成绩行: %v", beatmapHash, err)
		}
		defer r.ReleaseObject(scoreObj)
		linkKey, err := r.FindProperty(scoreCK, "BeatmapInfo")
		if err != nil {
			return err
		}
		return r.SetLinkedObject(scoreObj, linkKey, bmObj)
	})
	// 注入生效验证（关系非 null 才构成有效的前置态）。
	withRO(t, lib, realmPath, func(r *realmc.Realm) {
		scoreCK, err := r.FindClass("Score")
		if err != nil {
			t.Fatalf("FindClass(Score): %v", err)
		}
		scoreHashKey, _ := r.FindProperty(scoreCK, "BeatmapHash")
		obj, err := r.FindFirstByString(scoreCK, scoreHashKey, beatmapHash)
		if err != nil || obj == nil {
			t.Fatalf("注入验证：读回成绩行: %v", err)
		}
		defer r.ReleaseObject(obj)
		linkKey, _ := r.FindProperty(scoreCK, "BeatmapInfo")
		null, err := r.IsNull(obj, linkKey)
		if err != nil {
			t.Fatalf("注入验证判空: %v", err)
		}
		if null {
			t.Fatal("注入失败：Score.BeatmapInfo 仍为 null")
		}
	})
}

// insertBareSetRow 现场插入一行最小形态集（Hash + DateAdded + Status），
// 模拟第三方向目标写入同键裸行（§3.3 执行期现场复核夹具）。
func insertBareSetRow(t *testing.T, lib *realmc.Library, realmPath, hash string) {
	t.Helper()
	mutate(t, lib, realmPath, func(r *realmc.Realm) error {
		ck, err := r.FindClass("BeatmapSet")
		if err != nil {
			return err
		}
		obj, err := r.CreateObject(ck)
		if err != nil {
			return err
		}
		defer r.ReleaseObject(obj)
		hashKey, err := r.FindProperty(ck, "Hash")
		if err != nil {
			return err
		}
		dateKey, err := r.FindProperty(ck, "DateAdded")
		if err != nil {
			return err
		}
		statusKey, err := r.FindProperty(ck, "Status")
		if err != nil {
			return err
		}
		for _, w := range []func() error{
			func() error { return r.SetString(obj, hashKey, hash) },
			func() error { return r.SetTimestamp(obj, dateKey, t0.Add(999*time.Hour)) },
			func() error { return r.SetInt64(obj, statusKey, 4) },
		} {
			if err := w(); err != nil {
				return err
			}
		}
		return nil
	})
}

// putToStore 把字节池逐哈希入仓（files CopyMissing 先行的源）。
func putToStore(t *testing.T, e *env, files map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	for h, b := range files {
		src := filepath.Join(dir, h)
		if err := os.WriteFile(src, b, 0o644); err != nil {
			t.Fatalf("落源文件 %s: %v", h, err)
		}
		if err := e.engine.Store.Put(src); err != nil {
			t.Fatalf("入仓 %s: %v", h, err)
		}
	}
}

// withRO 开只读会话执行 fn 后即关（会话不残留，占用探测不受影响）。
func withRO(t *testing.T, lib *realmc.Library, path string, fn func(*realmc.Realm)) {
	t.Helper()
	r, err := lib.OpenDynamicReadOnly(path)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", path, err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	fn(r)
}
