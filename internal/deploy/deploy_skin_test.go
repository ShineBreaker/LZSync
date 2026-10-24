package deploy

// 皮肤投放验收（design.md §7-M2-3 / §5.1-M2 / §5.3）：fixture 全链——
// 投放后目标库行数/逐字段相等、Files usage → RealmFile 行 → 游戏侧 files/
// 字节读回校验（链接先行）、结构档案零漂移、重续幂等；conflict → update
// 保目标 Guid；跳过防线（Protected / fork 专有指纹）逐条计数与人话原因。
// librealm 缺失时显式 SKIP（§8.1 补充门：不得静默绿）。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// buildSkinFixture 两个皮肤种子（.ini/.json/.png 混合文件面），返回字节池。
func buildSkinFixture(t *testing.T) (map[string][]byte, []hub.Skin) {
	t.Helper()
	files := map[string][]byte{}
	seedA, err := fixture.MaterializeSkin("测试皮肤甲", "skinmakerA", []fixture.SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:测试皮肤甲\n")},
		{Filename: "colours.json", Bytes: []byte(`{"Combo1":"255,128,64"}`)},
		{Filename: "cursor.png", Bytes: []byte("fake png bytes for cursor")},
	}, files)
	if err != nil {
		t.Fatalf("MaterializeSkin(甲): %v", err)
	}
	seedB, err := fixture.MaterializeSkin("测试皮肤乙", "skinmakerB", []fixture.SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:测试皮肤乙\nVersion:2.9\n")},
	}, files)
	if err != nil {
		t.Fatalf("MaterializeSkin(乙): %v", err)
	}
	skins := []hub.Skin{
		{
			ID:                "a1111111-1111-4111-8111-1111111111a1",
			Hash:              seedA.Hash,
			Name:              seedA.Name,
			Creator:           seedA.Creator,
			InstantiationInfo: seedA.InstantiationInfo,
			Files:             seedA.Files,
		},
		{
			ID:                "b2222222-2222-4222-8222-2222222222b2",
			Hash:              seedB.Hash,
			Name:              seedB.Name,
			Creator:           seedB.Creator,
			InstantiationInfo: seedB.InstantiationInfo,
			Files:             seedB.Files,
		},
	}
	return files, skins
}

// readSkinRow 读回目标库皮肤行（白名单 ∩ 实际列）。
func readSkinRow(t *testing.T, sr *setReader, contentHash string) (hub.Skin, bool) {
	t.Helper()
	ck := sr.class("Skin")
	obj, err := sr.r.FindFirstByString(ck, sr.prop("Skin", "Hash"), contentHash)
	if err != nil {
		t.Fatalf("查皮肤 %s: %v", contentHash, err)
	}
	if obj == nil {
		return hub.Skin{}, false
	}
	defer sr.r.ReleaseObject(obj)
	sk := hub.Skin{Hash: contentHash}
	if key, err := sr.r.FindProperty(ck, "ID"); err == nil {
		if b, err := sr.r.GetGuid(obj, key); err == nil {
			sk.ID = formatGuid(b)
		}
	}
	for _, c := range []struct {
		dst  *string
		name string
	}{
		{&sk.Name, "Name"}, {&sk.Creator, "Creator"}, {&sk.InstantiationInfo, "InstantiationInfo"},
	} {
		if key, err := sr.r.FindProperty(ck, c.name); err == nil {
			if v, err := sr.r.GetString(obj, key); err == nil {
				*c.dst = v
			}
		}
	}
	// Files usage 列表（usage → RealmFile.Hash + Filename）。
	filesLst, err := sr.r.GetList(obj, sr.prop("Skin", "Files"))
	if err != nil {
		t.Fatalf("取皮肤 Files 列表: %v", err)
	}
	n, _ := filesLst.Size()
	for i := uint64(0); i < n; i++ {
		usage, err := filesLst.GetLinkedObject(i)
		if err != nil || usage == nil {
			t.Fatalf("读 Files[%d]: %v", i, err)
		}
		nf := hub.NamedFile{Filename: sr.str(usage, "RealmNamedFileUsage", "Filename")}
		file := sr.linked(usage, "RealmNamedFileUsage", "File")
		nf.Hash = sr.str(file, "File", "Hash")
		sr.r.ReleaseObject(file)
		sr.r.ReleaseObject(usage)
		sk.Files = append(sk.Files, nf)
	}
	filesLst.Close()
	return sk, true
}

// TestDeploySkinFullChain 皮肤全链：lazer 收集两皮肤入 hub → delta 空库
// 投放 → 行数/逐字段（含 Files usage→File 行）相等、files/ 字节读回校验
// （CopyMissing 链接先行）、结构零漂移、重续幂等；conflict → update 保
// 目标 Guid。
func TestDeploySkinFullChain(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files, skins := buildSkinFixture(t)
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	// 目标 B 先收空（无皮肤 presence）→ 源 lazer 收两皮肤（canonical）。
	applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0})
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0.Add(time.Hour), Skins: skins})

	plan, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("计划项 = %d（期望 2 skin create）: %+v", len(plan.Items), plan.Items)
	}
	for _, it := range plan.Items {
		if it.Kind != hub.KindSkin || it.Action != ActionCreate || it.Restore {
			t.Errorf("计划项 = %+v，期望 skin create 非 Restore", it)
		}
		if !strings.Contains(it.Summary, "皮肤") {
			t.Errorf("计划摘要 = %q，期望含皮肤名锚点", it.Summary)
		}
	}

	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	snapBefore := schemaSnapshotOf(t, lib, refB.RealmPath)
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#1: %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 2 || len(report.Skipped) != 0 ||
		len(report.Repointed) != 0 || len(report.FailedBatches) != 0 {
		t.Fatalf("报告 = applied %+v skipped %+v repointed %+v failed %+v",
			report.Applied, report.Skipped, report.Repointed, report.FailedBatches)
	}

	if !snapBefore.Equal(schemaSnapshotOf(t, lib, refB.RealmPath)) {
		t.Error("投放前后目标结构档案漂移")
	}

	// 逐字段读回（含 Files usage → File 行链）。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if got := classRowCount(t, sr, "Skin"); got != 6+2 { // fixture 恒播内置六皮肤 + 投放 2
			t.Errorf("Skin 行数 = %d（期望 8——内置六 + 投放二）", got)
		}
		if got := classRowCount(t, sr, "File"); got != uint64(len(files)) {
			t.Errorf("File 行数 = %d（期望 %d——皮肤全部实体哈希）", got, len(files))
		}
		for _, want := range skins {
			got, ok := readSkinRow(t, sr, want.Hash)
			if !ok {
				t.Errorf("目标库缺皮肤 %s", want.Hash)
				continue
			}
			if got.ID != want.ID && !guidEqual(got.ID, want.ID) {
				t.Errorf("皮肤 %q Guid = %s，期望 %s", want.Name, got.ID, want.ID)
			}
			if got.Name != want.Name || got.Creator != want.Creator ||
				got.InstantiationInfo != want.InstantiationInfo {
				t.Errorf("皮肤 %q 标量 = %q/%q/%q，期望 %q/%q/%q",
					want.Name, got.Name, got.Creator, got.InstantiationInfo,
					want.Name, want.Creator, want.InstantiationInfo)
			}
			if !namedFilesEqual(got.Files, want.Files) {
				t.Errorf("皮肤 %q Files = %v，期望 %v", want.Name, got.Files, want.Files)
			}
		}
	})

	// 链接先行：游戏侧 files/ 哈希实体齐全且字节读回校验（§5.3）。
	filesDir := filestore.ResolveFilesDir(refB.RealmPath)
	for hash, want := range files {
		got, err := os.ReadFile(filepath.Join(filesDir, hash[:1], hash[:2], hash))
		if err != nil {
			t.Errorf("目标 files/ 缺 %s: %v", hash, err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("目标 files/ %s 字节与仓内源不一致", hash)
		}
	}

	// 重续幂等：同计划重跑 → 全部「已存在 → 跳过」。
	report2, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %+v（期望 0）", report2.Applied)
	}
	skipTotal := 0
	for _, st := range report2.Skipped {
		skipTotal += st.Count
	}
	if skipTotal != 2 {
		t.Errorf("重跑 skip 计数 = %d（期望 2）: %+v", skipTotal, report2.Skipped)
	}

	// conflict → update：目标侧收集改名（presence 与 canonical 分叉）→
	// hub canonical 仍持原名 → 对账冲突 → update 计划保目标 Guid。
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		var target []hub.Skin
		for _, want := range skins {
			got, ok := readSkinRow(t, sr, want.Hash)
			if !ok {
				t.Fatalf("读回缺皮肤 %s（收集前）", want.Hash)
			}
			target = append(target, got)
		}
		applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0.Add(2 * time.Hour), Skins: target})
	})
	skinsV2 := append([]hub.Skin(nil), skins...)
	skinsV2[0].Name = "测试皮肤甲-改名版" // canonical 覆盖（HB3 整行覆盖）
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0.Add(3 * time.Hour), Skins: skinsV2})

	plan3, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan#3: %v", err)
	}
	if len(plan3.Items) != 1 || plan3.Items[0].Action != ActionUpdate {
		t.Fatalf("计划#3 = %+v（期望仅改名皮肤 update）", plan3.Items)
	}
	report3, err := runner.Deploy(ctx, plan3, nil)
	if err != nil {
		t.Fatalf("Deploy#3: %v", err)
	}
	if len(report3.Applied) != 1 || report3.Applied[0].Action != ActionUpdate {
		t.Errorf("Deploy#3 applied = %+v（期望 1 update）", report3.Applied)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := readSkinRow(t, sr, skins[0].Hash)
		if !ok {
			t.Fatal("update 后读回缺皮肤")
		}
		if got.Name != skinsV2[0].Name {
			t.Errorf("update 后 Name = %q，期望 canonical %q", got.Name, skinsV2[0].Name)
		}
		if got.ID != skins[0].ID && !guidEqual(got.ID, skins[0].ID) {
			t.Errorf("update 后 Guid = %s，期望保持目标值 %s（§4.2 保目标 Guid）", got.ID, skins[0].ID)
		}
	})
}

// TestDeploySkinSkipProtectedAndOwnSkin 跳过防线：hub 出现 Protected 皮肤
// 或 fork 专有指纹皮肤（异常路径——投影端 §4.1 已跳过，正常不进 hub）时
// 写入器侧宁可不写：逐条计数 + 人话原因，目标库零落地。
func TestDeploySkinSkipProtectedAndOwnSkin(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files, skins := buildSkinFixture(t)
	protected := skins[0]
	protected.Protected = true // 异常路径夹具：Protected 行混入 hub

	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkDelta, CollectedAt: t0, Skins: []hub.Skin{protected, skins[1]}})

	plan, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("计划项 = %d（期望 2——防线在写入器侧，计划面不预判）", len(plan.Items))
	}
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望仅正常皮肤落地）", report.Applied)
	}
	protectedSkipped := 0
	for _, st := range report.Skipped {
		if strings.Contains(st.Reason, "Protected 内置皮肤") {
			protectedSkipped += st.Count
		}
	}
	if protectedSkipped != 1 {
		t.Errorf("Protected 跳过计数 = %d（期望 1），Skipped=%+v", protectedSkipped, report.Skipped)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if _, ok := readSkinRow(t, sr, protected.Hash); ok {
			t.Error("Protected 皮肤不应落地目标库")
		}
		if _, ok := readSkinRow(t, sr, skins[1].Hash); !ok {
			t.Error("正常皮肤应落地目标库")
		}
	})
}

// TestForkOwnSkinHit fork 专有皮肤指纹判定（注册表演进后的防线输入）：
// (Name, Creator) 对命中即真；当前注册表 OwnSkins 为空是数据现状而非
// 逻辑豁免——单测直接验证判定语义。
func TestForkOwnSkinHit(t *testing.T) {
	profiles := []forks.ForkProfile{
		{ID: "lazer"},
		{ID: "ez2lazer", OwnSkins: []forks.SkinFingerprint{{Name: "Ez Default", Creator: "Ez2Lazer"}}},
	}
	if !forkOwnSkinHit(profiles, "Ez Default", "Ez2Lazer") {
		t.Error("命中指纹的皮肤应判为 fork 专有")
	}
	if forkOwnSkinHit(profiles, "Ez Default", "someone-else") {
		t.Error("Creator 不符不应命中（指纹 = Name+Creator 对）")
	}
	if forkOwnSkinHit(profiles, "普通皮肤", "普通作者") {
		t.Error("未登记指纹不应命中")
	}
	// 真实注册表现状：OwnSkins 未登记 → 恒不命中（防线待注册表数据演进）。
	if forkOwnSkinHit(forks.All(), "anything", "anyone") {
		t.Error("当前注册表 OwnSkins 为空，不应命中任何指纹")
	}
}

// TestDeploySkinMissingColumns 缺列降级：删 Creator 列的 lazer 镜像库投放
// 成功，缺列计数上报（人话原因），其余列正常落地。
func TestDeploySkinMissingColumns(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	schema, err := fixture.SchemaFor(fixture.ForkLazer, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("SchemaFor: %v", err)
	}
	for i, c := range schema.Classes {
		if c.Name != "Skin" {
			continue
		}
		var kept []realmc.PropertyDef
		for _, p := range c.Properties {
			if p.Name == "Creator" {
				continue
			}
			kept = append(kept, p)
		}
		schema.Classes[i].Properties = kept
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, schema, fixture.VersionLazer)
	if err != nil {
		t.Fatalf("建缺列库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭缺列库: %v", err)
	}
	refB := forks.LibraryRef{
		ForkID: fixture.ForkLazer, DataDir: dir, RealmPath: path,
		DiskVersion: fixture.VersionLazer, VersionSource: forks.VersionFromOpen,
	}

	files, skins := buildSkinFixture(t)
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkDelta, CollectedAt: t0, Skins: skins[:1]})
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	report, err := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy(缺列库): %v", err)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望 1）", report.Applied)
	}
	missing := 0
	for _, st := range report.Skipped {
		if strings.Contains(st.Reason, "Skin.Creator") {
			missing += st.Count
		}
	}
	if missing != 1 {
		t.Errorf("缺列计数 = %d（期望 1），Skipped=%+v", missing, report.Skipped)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		got, ok := readSkinRow(t, sr, skins[0].Hash)
		if !ok {
			t.Fatal("缺列库投放后读回缺皮肤")
		}
		if got.Name != skins[0].Name {
			t.Errorf("Name = %q，期望 %q（缺 Creator 不影响其他列）", got.Name, skins[0].Name)
		}
	})
}

// insertSoftDeletedSkin 向目标库现场插入一行软删皮肤（Hash + Name +
// DeletePending=true，S4），模拟计划构建后、投放前用户在游戏内删除该
// 皮肤（tombstone 尚未产生——收集才产 tombstone）。
func insertSoftDeletedSkin(t *testing.T, lib *realmc.Library, realmPath, hash, name string) {
	t.Helper()
	r, err := lib.OpenDynamicReadWrite(realmPath)
	if err != nil {
		t.Fatalf("写会话打开 %s: %v", realmPath, err)
	}
	defer r.Close()
	if err := r.BeginWrite(); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	ck, err := r.FindClass("Skin")
	if err != nil {
		t.Fatalf("FindClass(Skin): %v", err)
	}
	obj, err := r.CreateObject(ck)
	if err != nil {
		r.Rollback() //nolint:errcheck
		t.Fatalf("建行: %v", err)
	}
	defer r.ReleaseObject(obj)
	writes := []func() error{}
	for _, c := range []struct{ name, val string }{
		{"Hash", hash}, {"Name", name},
	} {
		key, err := r.FindProperty(ck, c.name)
		if err != nil {
			t.Fatalf("FindProperty(%s): %v", c.name, err)
		}
		val := c.val
		writes = append(writes, func() error { return r.SetString(obj, key, val) })
	}
	dpKey, err := r.FindProperty(ck, "DeletePending")
	if err != nil {
		t.Fatalf("FindProperty(DeletePending): %v", err)
	}
	writes = append(writes, func() error { return r.SetBool(obj, dpKey, true) })
	for _, w := range writes {
		if err := w(); err != nil {
			r.Rollback() //nolint:errcheck
			t.Fatalf("现场注入写入: %v", err)
		}
	}
	if err := r.Commit(); err != nil {
		t.Fatalf("提交注入: %v", err)
	}
}

// skinDeletePendingOf 读回皮肤行 DeletePending（软删防复活断言面）。
func skinDeletePendingOf(t *testing.T, sr *setReader, contentHash string) bool {
	t.Helper()
	ck := sr.class("Skin")
	obj, err := sr.r.FindFirstByString(ck, sr.prop("Skin", "Hash"), contentHash)
	if err != nil || obj == nil {
		t.Fatalf("查皮肤 %s: %v", contentHash, err)
	}
	defer sr.r.ReleaseObject(obj)
	return sr.boolOf(obj, "Skin", "DeletePending")
}

// TestDeploySkinSoftDeleteGuard HB2 执行面闭环（皮肤侧，谱面集写入器同款）：
// 计划后游戏内删除的现场软删行不被就地更新复活；恢复项（用户显式勾选）
// 才允许写回 DeletePending=false。
func TestDeploySkinSoftDeleteGuard(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()

	files, skins := buildSkinFixture(t)
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: fixture.Seed{},
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: refB.ForkID, CollectedAt: t0})
	applySnap(t, h, hub.Snapshot{ForkID: fixture.ForkLazer, CollectedAt: t0.Add(time.Hour),
		Skins: skins[:1]})
	plan, err := BuildPlan(ctx, h, refB, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].Action != ActionCreate {
		t.Fatalf("计划 = %+v（期望 1 skin create）", plan.Items)
	}
	store, err := filestore.Open(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("filestore.Open: %v", err)
	}
	fillStore(t, store, files)
	runner := NewRunner(Deps{Hub: h, Files: filestore.NewEngine(store)})

	// 计划后、投放前：游戏内软删该皮肤（S4）。
	insertSoftDeletedSkin(t, lib, refB.RealmPath, skins[0].Hash, skins[0].Name)

	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy(现场软删): %v（report=%+v）", err, report)
	}
	if len(report.Applied) != 0 {
		t.Errorf("applied = %+v（期望 0——软删行不复活）", report.Applied)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Count != 1 ||
		!strings.Contains(report.Skipped[0].Reason, "防复活") {
		t.Errorf("Skipped = %+v（期望 1 条防复活计数）", report.Skipped)
	}
	if len(report.Repointed) != 1 || report.Repointed[0].Reason != "目标已出现同键行：新建 → 就地更新" {
		t.Errorf("Repointed = %+v（期望 1 条新建→就地更新改判）", report.Repointed)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if !skinDeletePendingOf(t, sr, skins[0].Hash) {
			t.Error("现场软删皮肤被复活：DeletePending = false，期望保持 true（HB2）")
		}
	})

	// 恢复项（用户显式勾选）：写回 DeletePending=false 是明确意图。
	restorePlan, err := NewPlan(refB.ForkID, []PlanItem{{
		Kind: hub.KindSkin, ContentKey: skins[0].Hash, Action: ActionCreate, Restore: true,
	}})
	if err != nil {
		t.Fatalf("NewPlan(恢复): %v", err)
	}
	restorePlan.Target = refB
	report2, err := runner.Deploy(ctx, restorePlan, nil)
	if err != nil {
		t.Fatalf("Deploy(恢复): %v（report=%+v）", err, report2)
	}
	if len(report2.Applied) != 1 || report2.Applied[0].Action != ActionUpdate {
		t.Errorf("恢复 applied = %+v（期望 1 项 update——软删行现场命中改判）", report2.Applied)
	}
	withSetReader(t, lib, refB.RealmPath, func(sr *setReader) {
		if skinDeletePendingOf(t, sr, skins[0].Hash) {
			t.Error("恢复后 DeletePending = true（期望 false——复活是用户显式意图）")
		}
		got, ok := readSkinRow(t, sr, skins[0].Hash)
		if !ok {
			t.Fatal("恢复后读回缺皮肤")
		}
		if got.Name != skins[0].Name {
			t.Errorf("恢复后 Name = %q，期望 canonical %q", got.Name, skins[0].Name)
		}
	})
}
