package deploy

// delta gimmick 检测告警验收（design.md §5.4 R6 / §7-M3-4）：BuildPlan 期
// 对谱面集动作项扫描仓内 .osu section 头——命中集 PlanItem.Warning 标记
//（预演），Deploy 报告 GimmickWarnings 逐条计数（执行报告与预演一致），
// 正常谱面零误报，默认动作不变（R6：仍可同步）；仓未注入（Files=nil）与
// 仓缺 .osu 副本两条装配/数据边界。投放绝不改写 .osu 内容（§1 硬约束：
// 字节级不变断言）。librealm 缺失时显式 SKIP（§8.1）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
)

// gimmickEnv gimmick 告警链公共环境：delta 源库（DeltaGimmickSeed = 标准
// 集 + gimmick 集〔两类 section 头各一难度〕+ 收藏夹）收进 hub，lazer 目标
// 空库待投放。返回 hub、目标库引用、已入字节的仓与种子。
func gimmickEnv(t *testing.T) (*hub.Store, forks.LibraryRef, *filestore.Store, fixture.Seed) {
	t.Helper()
	ctx := context.Background()
	seed, err := fixture.DeltaGimmickSeed()
	if err != nil {
		t.Fatalf("DeltaGimmickSeed: %v", err)
	}
	refSrc, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: seed,
	})
	if err != nil {
		t.Fatalf("fixture.Build(delta 源): %v", err)
	}
	refDst, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(lazer 目标): %v", err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: refSrc.ForkID, CollectedAt: t0,
		BeatmapSets: seed.BeatmapSets, Collections: seed.Collections})
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, seed.Files)
	return h, refDst, store, seed
}

// TestBuildPlanGimmickWarning 预演标记：gimmick 集逐项 Warning、正常集与
// 收藏夹零误报、默认动作不变。
func TestBuildPlanGimmickWarning(t *testing.T) {
	mustLib(t) // librealm 缺失时显式 SKIP（§8.1），而非 fixture.Build 报错
	h, refDst, store, seed := gimmickEnv(t)

	plan, err := BuildPlan(context.Background(), h, refDst, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	stdSet, gimmickSet := seed.BeatmapSets[0], seed.BeatmapSets[1]
	got := map[string]PlanItem{}
	for _, it := range plan.Items {
		got[string(it.Kind)+"\x00"+it.ContentKey] = it
	}
	// gimmick 集：Warning 标记 + 动作不变（create，默认可同步）。
	gk := string(hub.KindBeatmapSet) + "\x00" + gimmickSet.Hash
	it, ok := got[gk]
	if !ok {
		t.Fatalf("计划缺 gimmick 集项 %s（计划=%+v）", gimmickSet.Hash, plan.Items)
	}
	if it.Action != ActionCreate {
		t.Errorf("gimmick 集动作 = %s（期望 create——告警不改变默认动作）", it.Action)
	}
	if it.Warning != gimmickWarningText {
		t.Errorf("gimmick 集 Warning = %q，期望告警文案", it.Warning)
	}
	// 正常谱面集：零误报。
	sk := string(hub.KindBeatmapSet) + "\x00" + stdSet.Hash
	if it, ok := got[sk]; !ok || it.Warning != "" || it.Action != ActionCreate {
		t.Errorf("正常集计划项 = %+v（期望无 Warning 的 create）", it)
	}
	// 收藏夹：不受 gimmick 检测影响。
	ck := string(hub.KindCollection) + "\x00" + seed.Collections[0].Name
	if it, ok := got[ck]; !ok || it.Warning != "" {
		t.Errorf("收藏夹计划项 = %+v（期望存在且无 Warning）", it)
	}
}

// TestDeployGimmickReport 执行报告计数：GimmickWarnings 逐条呈现命中集，
// applied 动作不变，且投放全程不改写仓内 .osu 字节（§1 硬约束）。
func TestDeployGimmickReport(t *testing.T) {
	mustLib(t)
	h, refDst, store, seed := gimmickEnv(t)
	ctx := context.Background()

	plan, err := BuildPlan(ctx, h, refDst, BuildOptions{Files: store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	gimmickSet := seed.BeatmapSets[1]
	// 仓内 gimmick .osu 字节基准（两个难度的副本都锚定）。
	blobSHA := map[string]string{}
	for _, bm := range gimmickSet.Beatmaps {
		p := store.Path(bm.Hash)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("读仓内 .osu %s: %v", bm.Hash, err)
		}
		sum := sha256.Sum256(b)
		blobSHA[bm.Hash] = hex.EncodeToString(sum[:])
	}

	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	// 默认动作不变：gimmick 集照常提交。
	appliedKeys := map[string]bool{}
	for _, ap := range report.Applied {
		appliedKeys[string(ap.Kind)+"\x00"+ap.ContentKey] = true
	}
	if !appliedKeys[string(hub.KindBeatmapSet)+"\x00"+gimmickSet.Hash] {
		t.Errorf("Applied 缺 gimmick 集（= %+v），期望告警不阻断同步", report.Applied)
	}
	// 报告计数：恰好 1 条命中集逐条呈现（Kind/键/文案与预演一致）。
	if len(report.GimmickWarnings) != 1 {
		t.Fatalf("GimmickWarnings = %+v（期望恰 1 条）", report.GimmickWarnings)
	}
	w := report.GimmickWarnings[0]
	if w.Kind != hub.KindBeatmapSet || w.ContentKey != gimmickSet.Hash || w.Warning != gimmickWarningText {
		t.Errorf("GimmickWarnings[0] = %+v（期望 gimmick 集键与预演文案）", w)
	}
	// 绝不改写仓内 .osu 内容。
	for hash, want := range blobSHA {
		b, err := os.ReadFile(store.Path(hash))
		if err != nil {
			t.Fatalf("读回仓内 .osu %s: %v", hash, err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("仓内 .osu %s 字节被改写（%s → %s）", hash, want, got)
		}
	}
}

// TestBuildPlanGimmickNilFiles 装配边界：计划含谱面集动作项而仓未注入 →
// 如实报错（告警面缺失不静默，§1.5-4）。
func TestBuildPlanGimmickNilFiles(t *testing.T) {
	mustLib(t)
	h, refDst, _, _ := gimmickEnv(t)

	_, err := BuildPlan(context.Background(), h, refDst, BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "BuildOptions.Files 未配置") {
		t.Fatalf("缺仓应报错，got %v", err)
	}
}

// TestBuildPlanGimmickMissingBlob 数据边界：仓内缺 .osu 副本 → 检测跳过
//（无法检测 ≠ 无 gimmick），预演不报错不告警（缺文件的集在投放期
// CopyMissing 自然失败并给全链错误）。
func TestBuildPlanGimmickMissingBlob(t *testing.T) {
	mustLib(t)
	h, refDst, _, _ := gimmickEnv(t)
	emptyStore, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open(空仓): %v", err)
	}

	plan, err := BuildPlan(context.Background(), h, refDst, BuildOptions{Files: emptyStore})
	if err != nil {
		t.Fatalf("空仓 BuildPlan 不应报错: %v", err)
	}
	for _, it := range plan.Items {
		if it.Warning != "" {
			t.Errorf("空仓不应产生告警（无法检测 ≠ 命中）: %+v", it)
		}
	}
}

// TestOsuGimmickSectionScan 头扫描纯函数：两类头逐行精确命中、普通谱面
// 与非独立行（头名嵌在行内）不误报、缺文件如实报错。
func TestOsuGimmickSectionScan(t *testing.T) {
	write := func(content string) string {
		p := filepath.Join(t.TempDir(), "test.osu")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("写临时 .osu: %v", err)
		}
		return p
	}
	sectionOsu := "osu file format v14\n\n[Metadata]\nTitle:T\n\n[BeatmapSectionGimmicks]\n0,0,1500,EnableHPGimmick=True\n"
	if ok, err := osuHasGimmickSection(write(sectionOsu)); err != nil || !ok {
		t.Errorf("含 [BeatmapSectionGimmicks] 头 = (%v, %v)，期望命中", ok, err)
	}
	hitObjectOsu := "osu file format v14\n\n[BeatmapHitObjectGimmicks]\n1000.5,2,EnableCountLimits=True\n"
	if ok, err := osuHasGimmickSection(write(hitObjectOsu)); err != nil || !ok {
		t.Errorf("含 [BeatmapHitObjectGimmicks] 头 = (%v, %v)，期望命中", ok, err)
	}
	plainOsu := "osu file format v14\n\n[Difficulty]\nHPDrainRate:4\n\n[HitObjects]\n256,192,100,5,1,0:0:0:0\n"
	if ok, err := osuHasGimmickSection(write(plainOsu)); err != nil || ok {
		t.Errorf("普通谱面 = (%v, %v)，期望零误报", ok, err)
	}
	inlineOsu := "osu file format v14\n\n[Events]\n1,0,0,0,\"bg.png\" // [BeatmapSectionGimmicks] 仅注释提及\n"
	if ok, err := osuHasGimmickSection(write(inlineOsu)); err != nil || ok {
		t.Errorf("头名嵌在行内非独立 section 头 = (%v, %v)，期望不命中", ok, err)
	}
	if _, err := osuHasGimmickSection(filepath.Join(t.TempDir(), "absent.osu")); err == nil {
		t.Error("缺文件应报错")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("缺文件错误形态 = %v，期望 fs.ErrNotExist", err)
	}
	if _, err := osuHasGimmickSection(""); err == nil {
		t.Error("空路径（非规范哈希）应报错")
	}
}


// TestGimmickSectionHeadersFromProfiles R6 名单数据化验收：专有 section
// 头来自 ForkProfile 数据面（delta 的两个头在列），内核不再硬编码 fork
// 知识（§4.3：新增第五 fork = 只落 Profile 数据，内核零改动）。
func TestGimmickSectionHeadersFromProfiles(t *testing.T) {
	dp, ok := forks.Profile("delta")
	if !ok {
		t.Fatal("forks.Profile(delta) 未注册")
	}
	want := []string{"[BeatmapSectionGimmicks]", "[BeatmapHitObjectGimmicks]"}
	if len(dp.GimmickSectionHeaders) != len(want) || dp.GimmickSectionHeaders[0] != want[0] || dp.GimmickSectionHeaders[1] != want[1] {
		t.Errorf("delta Profile GimmickSectionHeaders = %v（期望 fork-delta §5.1 的 %v）", dp.GimmickSectionHeaders, want)
	}
	got := gimmickSectionHeaders()
	if len(got) == 0 {
		t.Fatalf("专有 section 头聚合名单为空（期望含 delta 的 %v）", want)
	}
	for _, w := range want {
		found := false
		for _, h := range got {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("聚合名单 %v 缺 %s（数据链路：Profile → forks.All() → 聚合）", got, w)
		}
	}
}
