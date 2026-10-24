package deploy

// B2 批模型验收（design.md §7-M2-6 / §3.3 执行模型）：多批计划（谱面集
// >批大小阈值 50、成绩 >阈值 500，两类混合）+ 中途批失败注入 → 断言
// 失败批整批回滚、已提交批保留、报告三段式（已提交 / 失败+原因 / 未执行
// 清单）；重续入口重跑 → 已提交项判「已存在→跳过」、原失败批成功补齐
//（幂等）。批序 = 收藏→谱面集→皮肤→成绩（splitKindBatches），谱面集批
// 恒先于成绩批（§5.2-3 惰性索引依赖）。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// batchSetThreshold / batchScoreThreshold 与 §3.3 批切分默认值一致
// （deploy.go 常量的镜像；跨批断言的规模依据）。
const (
	batchSetThreshold   = 50
	batchScoreThreshold = 500
)

// buildManySetsFixture n 个单难度合成集（.osu 内容互异 → 内容键互异），
// 行 Guid 确定性派生（投放新建行的 origin_guid 断言锚点）。
func buildManySetsFixture(t *testing.T, n int) (map[string][]byte, []hub.BeatmapSet) {
	t.Helper()
	files := map[string][]byte{}
	sets := make([]hub.BeatmapSet, 0, n)
	for i := 0; i < n; i++ {
		set, err := fixture.MaterializeSet(int64(7000+i),
			fmt.Sprintf("Batch Artist %03d", i), fmt.Sprintf("Batch Title %03d", i), "mapperB",
			[]fixture.DiffMaterial{{
				Name:     "Only",
				OsuBytes: []byte(fmt.Sprintf("batch model set %03d osu bytes", i)),
			}}, nil, files)
		if err != nil {
			t.Fatalf("MaterializeSet(%d): %v", i, err)
		}
		set.ID = fmt.Sprintf("71%06d-1111-4111-8111-111111111111", i)
		set.Beatmaps[0].ID = fmt.Sprintf("72%06d-2222-4222-8222-222222222222", i)
		sets = append(sets, set)
	}
	return files, sets
}

// makeManyScores n 条自洽成绩，全部挂 anchor 难度（组合键互异——回放
// 字节互异），ID 确定性派生。
func makeManyScores(n int, anchor string, files map[string][]byte) []hub.Score {
	scores := make([]hub.Score, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("81%06d-3333-4333-8333-333333333333", i)
		replay := []byte(fmt.Sprintf("batch model replay bytes %04d", i))
		scores = append(scores, makeScore(id, anchor, "osu", replay, files))
	}
	return scores
}

// batchPlanItems 谱面集 + 成绩的混合计划项（create；成绩组合键编码）。
func batchPlanItems(sets []hub.BeatmapSet, scores []hub.Score) []PlanItem {
	items := make([]PlanItem, 0, len(sets)+len(scores))
	for _, bs := range sets {
		items = append(items, PlanItem{
			Kind: hub.KindBeatmapSet, ContentKey: bs.Hash, Action: ActionCreate,
			Summary: beatmapSetSummary(bs.Hash),
		})
	}
	for _, sc := range scores {
		items = append(items, PlanItem{
			Kind: hub.KindScore, ContentKey: hub.ScoreContentKey(sc.BeatmapHash, sc.Hash),
			Action: ActionCreate, Summary: "成绩 " + hashPrefix(sc.Hash) + "…",
		})
	}
	return items
}

// buildMalformedScoreLib 畸形列目标库：delta 镜像 + Score.TotalScore 列
// 声明为 String（正常 Int）。写入器 SetInt64 落到该列即类型错误 → 批内
// 写入异常 → 整批回滚（S1）。库不播种（空库）——规则集行由写入器的
// resolveRulesetObj 官方四规则集缺行新建面落地（§5.1）。
func buildMalformedScoreLib(t *testing.T, lib *realmc.Library) forks.LibraryRef {
	t.Helper()
	schema, err := fixture.SchemaFor(fixture.ForkDelta, fixture.VersionDelta)
	if err != nil {
		t.Fatalf("SchemaFor(delta): %v", err)
	}
	hit := false
	for i := range schema.Classes {
		if schema.Classes[i].Name != "Score" {
			continue
		}
		for j := range schema.Classes[i].Properties {
			if schema.Classes[i].Properties[j].Name == "TotalScore" {
				schema.Classes[i].Properties[j].Type = realmc.PropTypeString
				hit = true
			}
		}
	}
	if !hit {
		t.Fatal("夹具构造失败：delta 镜像未找到 Score.TotalScore 列")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionDelta)
	if err != nil {
		t.Fatalf("建畸形列库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭畸形列库: %v", err)
	}
	return forks.LibraryRef{
		ForkID: fixture.ForkDelta, DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionDelta, VersionSource: forks.VersionFromOpen,
	}
}

// TestDeployBatchModelMidwayFailure 畸形列触发中途批失败（§7-M2-6）：
// 51 集（2 批）+ 501 成绩（2 批）→ 谱面集批 0/1 先提交（已提交批保留），
// 成绩批 2 写 TotalScore 撞畸形列 → 整批回滚（Score 零行）并停止，批 3
// 未执行 → 报告三段式：Applied=51 谱面集 / FailedBatches=[{2, 500 项,
// 原因含列名}] / NotRun=1 项。
func TestDeployBatchModelMidwayFailure(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files, sets := buildManySetsFixture(t, batchSetThreshold+1) // 51 集 → 2 批
	scores := makeManyScores(batchScoreThreshold+1, sets[0].Beatmaps[0].Hash, files)

	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0,
		BeatmapSets: sets, Scores: scores})
	store := openStore(t)
	fillStore(t, store, files)

	refB := buildMalformedScoreLib(t, lib)
	plan, err := NewPlan(refB.ForkID, batchPlanItems(sets, scores))
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	plan.Target = refB

	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err == nil {
		t.Fatal("畸形列库投放应中途失败")
	}

	// 三段式之「已提交批」：51 项全部为谱面集 create。
	if len(report.Applied) != len(sets) {
		t.Fatalf("Applied = %d 项（期望 %d——谱面集批 0/1 已提交）", len(report.Applied), len(sets))
	}
	for _, ap := range report.Applied {
		if ap.Kind != hub.KindBeatmapSet || ap.Action != ActionCreate {
			t.Errorf("已提交项 = %+v，期望全部 beatmap_set create", ap)
		}
	}
	// 三段式之「失败批」：恰 1 批（批 2 = 首个成绩批）、500 项、原因含畸形列线索。
	if len(report.FailedBatches) != 1 {
		t.Fatalf("FailedBatches = %d 批（期望 1）: %+v", len(report.FailedBatches), report.FailedBatches)
	}
	fb := report.FailedBatches[0]
	if fb.BatchIndex != 2 {
		t.Errorf("失败批 index = %d（期望 2——谱面集批 0/1 之后的首个成绩批）", fb.BatchIndex)
	}
	if len(fb.Items) != batchScoreThreshold {
		t.Errorf("失败批项数 = %d（期望 %d——整批计入）", len(fb.Items), batchScoreThreshold)
	}
	if !strings.Contains(fb.Reason, "TotalScore") {
		t.Errorf("失败原因不含畸形列线索: %s", fb.Reason)
	}
	// 三段式之「未执行批清单」：第 501 个成绩（批 3）。
	if len(report.NotRun) != 1 || report.NotRun[0].Kind != hub.KindScore {
		t.Fatalf("NotRun = %+v（期望恰 1 项成绩——批 3 未执行）", report.NotRun)
	}
	// 备份在首批前已生成（S2），与批失败无关。
	if report.BackupPath == "" {
		t.Error("批失败前未生成备份（S2：首批前一次性生成）")
	}

	// 现场核对：已提交批保留（51 集）、失败批整批回滚（成绩零行）。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "BeatmapSet"); got != uint64(len(sets)) {
			t.Errorf("BeatmapSet 行数 = %d（期望 %d——已提交批保留）", got, len(sets))
		}
		if got := classRowCount(t, sr, "Score"); got != 0 {
			t.Errorf("Score 行数 = %d（期望 0——失败批整批回滚）", got)
		}
	})
}

// conflictMarkerHash 预置冲突行的 Hash 列标记值（与全部计划成绩内容键
// 互异——findScoreRow 现场匹配按 Hash 查行，标记行不构成任何计划项的既有行）。
const conflictMarkerHash = "lzsync-conflict-marker-hash"

// buildGuidPkConflictLib 目标库：delta 镜像 + Score.ID 声明为主键（真实库
// 形态——写入器 scorePK 分支）+ 预播一行 Guid = conflictGuid、Hash = 标记
// 值的 Score 行。计划中 ID 与 conflictGuid 相同的成绩项走 createScore 的
// CreateObjectWithGuidPK 时撞主键 → 写入异常 → 整批回滚（内容键外的罕见
// 冲突如实上抛，writer_score.go createScore 的既定错误类）。
func buildGuidPkConflictLib(t *testing.T, lib *realmc.Library, conflictGuid string) forks.LibraryRef {
	t.Helper()
	schema, err := fixture.SchemaFor(fixture.ForkDelta, fixture.VersionDelta)
	if err != nil {
		t.Fatalf("SchemaFor(delta): %v", err)
	}
	hit := false
	for i := range schema.Classes {
		if schema.Classes[i].Name != "Score" {
			continue
		}
		schema.Classes[i].PrimaryKey = "ID"
		for j := range schema.Classes[i].Properties {
			if schema.Classes[i].Properties[j].Name == "ID" {
				schema.Classes[i].Properties[j].IsPrimaryKey = true
				hit = true
			}
		}
	}
	if !hit {
		t.Fatal("夹具构造失败：delta 镜像未找到 Score.ID 列")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionDelta)
	if err != nil {
		t.Fatalf("建主键形态库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭主键形态库: %v", err)
	}
	// 预播冲突行（主键必填，其余列零值合法；Hash 写标记值供修复时定位）。
	mutateScoreConflictRow(t, lib, path, conflictGuid, true)
	ref := forks.LibraryRef{
		ForkID: fixture.ForkDelta, DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionDelta, VersionSource: forks.VersionFromOpen,
	}
	return ref
}

// mutateScoreConflictRow 预播（insert=true）/ 删除（insert=false）冲突标记行。
func mutateScoreConflictRow(t *testing.T, lib *realmc.Library, realmPath, guid string, insert bool) {
	t.Helper()
	r, err := lib.OpenDynamicReadWrite(realmPath)
	if err != nil {
		t.Fatalf("写会话打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginWrite(); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	fail := func(err error) {
		r.Rollback() //nolint:errcheck
		t.Fatalf("冲突行注入失败: %v", err)
	}
	ck, err := r.FindClass("Score")
	if err != nil {
		fail(err)
	}
	hashKey, err := r.FindProperty(ck, "Hash")
	if err != nil {
		fail(err)
	}
	if insert {
		id, err := parseGuid(guid)
		if err != nil {
			fail(err)
		}
		obj, err := r.CreateObjectWithGuidPK(ck, id)
		if err != nil {
			fail(err)
		}
		defer r.ReleaseObject(obj)
		if err := r.SetString(obj, hashKey, conflictMarkerHash); err != nil {
			fail(err)
		}
	} else {
		obj, err := r.FindFirstByString(ck, hashKey, conflictMarkerHash)
		if err != nil {
			fail(err)
		}
		if obj == nil {
			fail(fmt.Errorf("找不到冲突标记行"))
		}
		defer r.ReleaseObject(obj)
		if err := r.DeleteObject(obj); err != nil {
			fail(err)
		}
	}
	if err := r.Commit(); err != nil {
		fail(err)
	}
}

// TestDeployBatchModelResume 重续入口（§7-M2-6 / §3.3「从失败处重续」）：
// 52 集（2 批）+ 501 成绩（2 批）；批 2 中间一项的成绩 Guid 撞目标库预置
// 主键行 → 该项 createScore 报错 → 批 2 整批回滚、批 3 未执行、谱面集批
// 0/1 已提交保留。修复（删除目标库冲突行）后同一计划重跑：已提交的 52 集
// 全部判「已存在→跳过」，原失败批与未执行批成功补齐（幂等）。
//
// 注入选型说明：库级畸形列（上一个用例）的修复必经重建库——realm 库不可
// 原地改列（S3 不迁移），重建会连同已提交批一并丢弃，与「已提交项判已
// 存在→跳过」的断言前提矛盾；故重续面注入用内容键外的 Guid 主键冲突
// （写入器既定的「罕见冲突如实上抛整批回滚」错误类），修复只动冲突行，
// 库与已提交批原封不动。
func TestDeployBatchModelResume(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files, sets := buildManySetsFixture(t, batchSetThreshold+2)                      // 52 集 → 批 [50, 2]
	scores := makeManyScores(batchScoreThreshold+1, sets[0].Beatmaps[0].Hash, files) // 501 → 批 [500, 1]
	const conflictIdx = 250                                                          // 批 2（批序列第 3 批）内第 251 项 → 批内先行项写毕随批回滚

	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0,
		BeatmapSets: sets, Scores: scores})
	store := openStore(t)
	fillStore(t, store, files)

	refB := buildGuidPkConflictLib(t, lib, scores[conflictIdx].ID)

	plan, err := NewPlan(refB.ForkID, batchPlanItems(sets, scores))
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	plan.Target = refB
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})

	report1, err := runner.Deploy(ctx, plan, nil)
	if err == nil {
		t.Fatal("撞 Guid 主键的投放应中途失败")
	}
	if len(report1.Applied) != len(sets) {
		t.Fatalf("P1 Applied = %d（期望 %d——谱面集批已提交）", len(report1.Applied), len(sets))
	}
	if len(report1.FailedBatches) != 1 || report1.FailedBatches[0].BatchIndex != 2 {
		t.Fatalf("P1 FailedBatches = %+v（期望恰批 2 失败）", report1.FailedBatches)
	}
	if !strings.Contains(report1.FailedBatches[0].Reason, "Guid 主键") {
		t.Errorf("P1 失败原因不含主键冲突线索: %s", report1.FailedBatches[0].Reason)
	}
	if len(report1.NotRun) != 1 || report1.NotRun[0].Kind != hub.KindScore {
		t.Fatalf("P1 NotRun = %+v（期望恰 1 项成绩——批 3 未执行）", report1.NotRun)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "BeatmapSet"); got != uint64(len(sets)) {
			t.Fatalf("P1 后 BeatmapSet 行数 = %d（期望 %d）", got, len(sets))
		}
		// 失败批零落地：Score 仅剩预置冲突行。
		if got := classRowCount(t, sr, "Score"); got != 1 {
			t.Fatalf("P1 后 Score 行数 = %d（期望 1——失败批整批回滚，仅预置冲突行）", got)
		}
	})

	// 修复：删除目标库冲突行（只动冲突行，已提交批所在库原封不动）。
	mutateScoreConflictRow(t, lib, refB.RealmPath, "", false)

	report2, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("修复后重跑 Deploy: %v", err)
	}

	// 已提交项判「已存在→跳过」（§3.3 重续幂等）：52 集零重写。
	if got := skipCountOf(report2, "已存在 → 跳过"); got != len(sets) {
		t.Errorf("重跑重续跳过计数 = %d（期望 %d）: %+v", got, len(sets), report2.Skipped)
	}
	// 原失败批 + 未执行批补齐：恰 501 项成绩 create。
	if len(report2.Applied) != len(scores) {
		t.Fatalf("重跑 Applied = %d 项（期望 %d 成绩）: %+v", len(report2.Applied), len(scores), report2.Applied)
	}
	for _, ap := range report2.Applied {
		if ap.Kind != hub.KindScore || ap.Action != ActionCreate {
			t.Errorf("重跑落地项 = %+v，期望全部 score create", ap)
		}
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "Score"); got != uint64(len(scores)) {
			t.Errorf("补齐后 Score 行数 = %d（期望 %d）", got, len(scores))
		}
		// 抽查读回：撞过主键的原失败批项 + 原未执行批项逐字段（含挂接
		// 难度、回放 usage 全链）。
		got, ok := readScoreRow(t, sr, scores[conflictIdx])
		if !ok {
			t.Fatal("读回缺原失败批的冲突项（修复后应补齐）")
		}
		assertScoreDeepEqual(t, got, scores[conflictIdx], scores[conflictIdx].OnlineID)
		gotLast, ok := readScoreRow(t, sr, scores[len(scores)-1])
		if !ok {
			t.Fatal("读回缺末条成绩（原未执行批补齐项）")
		}
		assertScoreDeepEqual(t, gotLast, scores[len(scores)-1], scores[len(scores)-1].OnlineID)
	})
}

// openStore 独立内容寻址仓（CopyMissing 的源）。
func openStore(t *testing.T) *filestore.Store {
	t.Helper()
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	return store
}
