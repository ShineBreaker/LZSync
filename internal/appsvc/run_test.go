package appsvc

// 一轮同步编排（§0.4 / G1-G6）端到端测试：两端 fixture（真实 realm 文件，
// native 库不可用时 SKIP）、配置角色、冲突/板块/仅目标端语义。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lzsync/internal/deploy"
	"lzsync/internal/filestore"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/inisettings"
	"lzsync/internal/realmc"
)

// newRunBackend 装配带 Files/Snapshots 的真实后端并注册两端 fixture。
// aSeed/bSeed 各自播入 lazer / delta 库；角色随后由调用方调整。
func newRunBackend(t *testing.T, aSeed, bSeed fixture.Seed) (*Backend, *hub.Store, forks.LibraryRef, forks.LibraryRef) {
	t.Helper()
	if _, err := realmc.Load(); err != nil {
		t.Skipf("native 库不可用，跳过编排集成测试: %v", err)
	}
	ctx := context.Background()
	dataRoot := t.TempDir()
	refA, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkLazer, SchemaVersion: fixture.VersionLazer,
		DataDir: t.TempDir(), Seed: aSeed,
	})
	if err != nil {
		t.Fatalf("fixture.Build(A): %v", err)
	}
	refB, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkDelta, SchemaVersion: fixture.VersionDelta,
		DataDir: t.TempDir(), Seed: bSeed,
	})
	if err != nil {
		t.Fatalf("fixture.Build(B): %v", err)
	}
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
		t.Fatalf("OpenSnapshotStore: %v", err)
	}
	iniStore, err := inisettings.OpenStore(filepath.Join(dir, "gameini"))
	if err != nil {
		t.Fatalf("inisettings.OpenStore: %v", err)
	}
	b, err := NewRealBackend(Deps{
		Hub: h, Files: filestore.NewEngine(store), Snapshots: snaps,
		Backups: mustBackups(t, filepath.Join(dir, "backups")),
		Guard:   guard.NewGuard(nil), GameIni: iniStore,
		DataRoot: dataRoot, DataDir: dir,
	}, nil)
	if err != nil {
		t.Fatalf("NewRealBackend: %v", err)
	}
	// fixture 库无 fork 指纹结构差异（版本 51/52 与指纹不能唯一指认
	// delta/lazer）→ 走用户指认路径 AssignFork。
	if _, err := b.Library.AssignFork(refA.RealmPath, "lazer"); err != nil {
		t.Fatalf("AssignFork(A): %v", err)
	}
	if _, err := b.Library.AssignFork(refB.RealmPath, "delta"); err != nil {
		t.Fatalf("AssignFork(B): %v", err)
	}
	return b, h, refA, refB
}

// setForkRole 调整 sync scope 中一端的 Source/Target 角色。
func setForkRole(t *testing.T, b *Backend, forkID string, source, target bool) {
	t.Helper()
	scope, err := b.Settings.GetSyncScope()
	if err != nil {
		t.Fatalf("GetSyncScope: %v", err)
	}
	found := false
	for i := range scope.Forks {
		if scope.Forks[i].ForkID == forkID {
			scope.Forks[i].Source, scope.Forks[i].Target = source, target
			found = true
		}
	}
	if !found {
		t.Fatalf("scope 缺端 %s", forkID)
	}
	if err := b.Settings.SetSyncScope(scope); err != nil {
		t.Fatalf("SetSyncScope: %v", err)
	}
}

// TestRunAOnlyCollectionDeploysToB A 独有收藏（A=来源+目标、B=仅目标）：
// PrepareRun 给 B 计划 create；CommitRun 后 B 的 realm 中可读回（重收集
// presence 佐证），A 不落项。
func TestRunAOnlyCollectionDeploysToB(t *testing.T) {
	aSeed := fixture.Seed{Collections: []hub.Collection{
		{Name: "夹A", BeatmapMD5Hashes: []string{"md5-1"}},
	}}
	b, h, _, _ := newRunBackend(t, aSeed, fixture.Seed{})
	setForkRole(t, b, "delta", false, true) // B 仅目标

	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	var bItems, aItems int
	for _, tp := range preview.Targets {
		if tp.ForkID == "delta" {
			bItems = tp.Items
		}
		if tp.ForkID == "lazer" {
			aItems = tp.Items
		}
	}
	if bItems != 1 {
		t.Fatalf("B 计划项 = %d，期望 1（夹A create），preview=%+v", bItems, preview)
	}
	if aItems != 0 {
		t.Errorf("A 计划项 = %d，期望 0（A 已有夹A）", aItems)
	}
	if len(preview.Skipped) != 0 {
		t.Errorf("Skipped = %+v，期望无", preview.Skipped)
	}

	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	var bRep *ForkRunReport
	for i := range report.Forks {
		if report.Forks[i].ForkID == "delta" {
			bRep = &report.Forks[i]
		}
	}
	if bRep == nil || bRep.Deploy == nil || len(bRep.Deploy.Applied) != 1 {
		t.Fatalf("B 端投放报告 = %+v，期望 1 项 applied", bRep)
	}
	if bRep.Deploy.Applied[0].Action != deploy.ActionCreate {
		t.Errorf("B 投放动作 = %s，期望 create", bRep.Deploy.Applied[0].Action)
	}
	// CommitRun 重收集 B → presence 含夹A（读回佐证 realm 已写入）。
	var n int
	if err := h.DB().QueryRow(
		`SELECT COUNT(*) FROM presence WHERE fork_id='delta' AND kind='collection' AND content_key='夹A'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("B presence(夹A) = %d（err %v），期望 1", n, err)
	}
}

// TestRunSameHashSetConflictStaysPending 同一谱面身份（OnlineID 相同）
// 两端各持不同版本且无基线 → 候选多值 = 冲突：两端都不写、冲突入
// Pending（§0.3 规则 5/B4）。StatusInt 等端本地字段不参与取值指纹。
func TestRunSameHashSetConflictStaysPending(t *testing.T) {
	aSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-v1", OnlineID: 9001, StatusInt: 1},
	}}
	bSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-v2", OnlineID: 9001, StatusInt: 3},
	}}
	b, _, _, _ := newRunBackend(t, aSeed, bSeed)
	setForkRole(t, b, "delta", false, true)

	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if preview.Pending.Conflicts < 1 {
		t.Errorf("Pending.Conflicts = %d，期望 ≥1", preview.Pending.Conflicts)
	}
	for _, tp := range preview.Targets {
		if tp.Items != 0 {
			t.Errorf("目标端 %s 计划项 = %d，期望 0（冲突不写）", tp.ForkID, tp.Items)
		}
	}
	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	for _, fr := range report.Forks {
		if fr.Deploy != nil && len(fr.Deploy.Applied) != 0 {
			t.Errorf("端 %s 冲突仍落 write: %+v", fr.ForkID, fr.Deploy.Applied)
		}
	}
}

// TestPreviewDeployReflectsDecisions 数据页行态来自三方判定（G12）：同
// 谱面身份两端各持异版本 → 单位全局冲突/本端异值 → delta 侧该键的行
// 从「版本更新（可自动执行）」降格为「冲突、默认不勾选」。
func TestPreviewDeployReflectsDecisions(t *testing.T) {
	aSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-v1", OnlineID: 9001, StatusInt: 1},
	}}
	bSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-v2", OnlineID: 9001, StatusInt: 1},
	}}
	b, _, _, _ := newRunBackend(t, aSeed, bSeed)
	setForkRole(t, b, "delta", false, true)

	// 先跑一轮准备收集填 presence（PreviewDeploy 的数据面是最后已知值）。
	if _, err := b.Sync.PrepareRun(); err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	rows, err := b.Compare.PreviewDeploy("delta")
	if err != nil {
		t.Fatalf("PreviewDeploy: %v", err)
	}
	var hit *DiffRow
	for i := range rows {
		if rows[i].Kind == hub.KindBeatmapSet && rows[i].ContentKey == "hash-v1" {
			hit = &rows[i]
		}
	}
	if hit == nil {
		t.Fatalf("delta 预览缺谱面行: %+v", rows)
	}
	if hit.Category != CategoryConflict || hit.DefaultChecked {
		t.Errorf("行态 = {Category:%s DefaultChecked:%v}，期望 {conflict,false}"+
			"（判定面异值/分歧时不得按版本更新自动执行）", hit.Category, hit.DefaultChecked)
	}
	if hit.SkipReason == "" {
		t.Error("冲突行应带判定原因（SkipReason）")
	}
}

// TestRunCollectionsSectionOff 收藏板块关闭 → 不收集也不投放收藏数据。
func TestRunCollectionsSectionOff(t *testing.T) {
	aSeed := fixture.Seed{Collections: []hub.Collection{
		{Name: "夹A", BeatmapMD5Hashes: []string{"md5-1"}},
	}}
	bSeed := fixture.Seed{Collections: []hub.Collection{
		{Name: "B独有夹", BeatmapMD5Hashes: []string{"md5-b"}},
	}}
	b, h, _, _ := newRunBackend(t, aSeed, bSeed)
	setForkRole(t, b, "delta", false, true)
	scope, err := b.Settings.GetSyncScope()
	if err != nil {
		t.Fatalf("GetSyncScope: %v", err)
	}
	for i := range scope.Sections {
		if scope.Sections[i].Section == SectionCollections {
			scope.Sections[i].Enabled = false
		}
	}
	if err := b.Settings.SetSyncScope(scope); err != nil {
		t.Fatalf("SetSyncScope: %v", err)
	}

	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	for _, tp := range preview.Targets {
		if tp.Items != 0 {
			t.Errorf("目标端 %s 计划项 = %d，期望 0（板块关）", tp.ForkID, tp.Items)
		}
	}
	if preview.Pending.Conflicts != 0 {
		t.Errorf("Pending.Conflicts = %d，期望 0（板块关不参与冲突判）", preview.Pending.Conflicts)
	}
	// 不收：B 的收藏 presence 不存在。
	var n int
	if err := h.DB().QueryRow(
		`SELECT COUNT(*) FROM presence WHERE fork_id='delta' AND kind='collection'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("B collection presence = %d（err %v），期望 0（板块关不收集）", n, err)
	}
	if report, err := b.Sync.CommitRun(preview.RunID); err != nil {
		t.Fatalf("CommitRun: %v", err)
	} else {
		for _, fr := range report.Forks {
			if fr.Deploy != nil && len(fr.Deploy.Applied) != 0 {
				t.Errorf("板块关闭仍写端 %s: %+v", fr.ForkID, fr.Deploy.Applied)
			}
		}
	}
}

// TestRunTargetOnlyForkNeverBecomesCanonical B 仅目标：B 独有的收藏不
// 进 hub 实体、也不会投给 A（首见即定 + 仅目标端不做 canonical 来源）。
func TestRunTargetOnlyForkNeverBecomesCanonical(t *testing.T) {
	bSeed := fixture.Seed{Collections: []hub.Collection{
		{Name: "B独有夹", BeatmapMD5Hashes: []string{"md5-b"}},
	}}
	b, h, _, _ := newRunBackend(t, fixture.Seed{}, bSeed)
	setForkRole(t, b, "delta", false, true)

	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	for _, tp := range preview.Targets {
		if tp.Items != 0 {
			t.Errorf("目标端 %s 计划项 = %d，期望 0（B 独有数据不回投）", tp.ForkID, tp.Items)
		}
	}
	var n int
	if err := h.DB().QueryRow(
		`SELECT COUNT(*) FROM collection WHERE content_key='B独有夹'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("canonical 收藏 B独有夹 = %d（err %v），期望 0（仅目标端不入实体）", n, err)
	}
	if err := h.DB().QueryRow(
		`SELECT COUNT(*) FROM presence WHERE fork_id='delta' AND kind='collection' AND content_key='B独有夹'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("B presence(B独有夹) = %d（err %v），期望 1（快照照写）", n, err)
	}
}

// TestCommitRunUnknownRunID 未知/过期 runID → 「请重新同步」错误。
func TestCommitRunUnknownRunID(t *testing.T) {
	b, _, _, _ := newRunBackend(t, fixture.Seed{}, fixture.Seed{})
	if _, err := b.Sync.CommitRun("nonexistent"); err == nil ||
		!errors.Is(err, ErrNoPlan) && !strings.Contains(err.Error(), "重新同步") {
		t.Fatalf("未知 runID CommitRun = %v，期望「请重新同步」类错误", err)
	}
}

// TestPrepareSelectedExplicitDisposition 数据页显式勾选：冲突项以 update
// 入计划（手动处置路径）。冲突用谱面集同 Hash 异 DateSubmitted——B7 后
// StatusInt 是端本地字段不构成冲突；收藏成员列在 fixture 形态下不落库，
// 成员冲突不可构造（见 decide 测试面）。
func TestPrepareSelectedExplicitDisposition(t *testing.T) {
	subA := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	subB := time.Date(2024, 3, 4, 0, 0, 0, 0, time.UTC)
	aSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-cx", OnlineID: 9002, StatusInt: 1, DateSubmitted: &subA},
	}}
	bSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-cx", OnlineID: 9002, StatusInt: 1, DateSubmitted: &subB},
	}}
	b, _, _, _ := newRunBackend(t, aSeed, bSeed)
	setForkRole(t, b, "delta", false, true)
	preview, err := b.Sync.PrepareSelected([]Selection{
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-cx"},
	})
	if err != nil {
		t.Fatalf("PrepareSelected: %v", err)
	}
	var bItems int
	for _, tp := range preview.Targets {
		if tp.ForkID == "delta" {
			bItems = tp.Items
		}
	}
	if bItems != 1 {
		t.Fatalf("B 计划项 = %d，期望 1（显式勾选的冲突→update）", bItems)
	}
	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	var sawUpdate bool
	for _, fr := range report.Forks {
		if fr.Deploy == nil {
			continue
		}
		for _, a := range fr.Deploy.Applied {
			if a.Action == deploy.ActionUpdate {
				sawUpdate = true
			}
		}
	}
	if !sawUpdate {
		t.Error("显式勾选的冲突项应以 update 落 B")
	}
}

// TestPrepareSelectedWinnerOverrides 胜者拍板（§0.3 手动处置）：规则集
// 设置两端异值、无基线 → 单位冲突（正常轮不写）。显式 Winner=delta →
// lazer 计划 delta 取值的就地更新；胜者端自身不落项；CommitRun 后
// lazer presence 收为 delta 值，下一轮收敛（基线已推进无冲突）。
func TestPrepareSelectedWinnerOverrides(t *testing.T) {
	aSeed := fixture.Seed{RulesetSettings: []fixture.SeedRulesetSetting{
		{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"topright"`},
	}}
	bSeed := fixture.Seed{RulesetSettings: []fixture.SeedRulesetSetting{
		{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"bottomleft"`},
	}}
	b, h, _, _ := newRunBackend(t, aSeed, bSeed)
	ck := hub.RulesetSettingContentKey("osu", 0, "hud_position")
	preview, err := b.Sync.PrepareSelected([]Selection{
		{Kind: hub.KindRulesetSetting, ContentKey: ck, Winner: "delta"},
	})
	if err != nil {
		t.Fatalf("PrepareSelected: %v", err)
	}
	got := map[string]int{}
	for _, tp := range preview.Targets {
		got[tp.ForkID] = tp.Items
	}
	if got["lazer"] != 1 {
		t.Fatalf("lazer 计划项 = %d，期望 1（以 delta 为准的就地更新）targets=%+v", got["lazer"], preview.Targets)
	}
	if got["delta"] != 0 {
		t.Fatalf("delta 计划项 = %d，期望 0（胜者端自身不落项）", got["delta"])
	}
	if _, err := b.Sync.CommitRun(preview.RunID); err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	var lv string
	if err := h.DB().QueryRow(
		`SELECT local_values FROM presence WHERE fork_id='lazer' AND kind='ruleset_setting' AND content_key=?`, ck).Scan(&lv); err != nil {
		t.Fatalf("读 lazer presence: %v", err)
	}
	if !strings.Contains(lv, "bottomleft") {
		t.Errorf("lazer presence 未收为 delta 值：%s", lv)
	}
	// 收敛验证：拍板值经基线推进后下一轮应零项零冲突。
	p2, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun(2): %v", err)
	}
	for _, tp := range p2.Targets {
		if tp.Items != 0 {
			t.Errorf("收敛后轮次 %s 计划项 = %d，期望 0", tp.ForkID, tp.Items)
		}
	}
	if p2.Pending.Conflicts != 0 {
		t.Errorf("收敛后冲突 = %d，期望 0", p2.Pending.Conflicts)
	}
}

// TestPrepareSelectedWinnerBeatmapVersion 谱面集版本拍板：同一身份
// （OnlineID）两端各持不同版本冲突 → Winner=delta → lazer 计划
// version-replace 到 delta 的版本；delta 不落项。
func TestPrepareSelectedWinnerBeatmapVersion(t *testing.T) {
	aSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-win-a", OnlineID: 9010, StatusInt: 1},
	}}
	bSeed := fixture.Seed{BeatmapSets: []hub.BeatmapSet{
		{Hash: "hash-win-b", OnlineID: 9010, StatusInt: 1},
	}}
	b, _, _, _ := newRunBackend(t, aSeed, bSeed)
	preview, err := b.Sync.PrepareSelected([]Selection{
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-win-a", Winner: "delta"},
	})
	if err != nil {
		t.Fatalf("PrepareSelected: %v", err)
	}
	got := map[string]int{}
	for _, tp := range preview.Targets {
		got[tp.ForkID] = tp.Items
	}
	if got["lazer"] != 1 {
		t.Fatalf("lazer 计划项 = %d，期望 1（version-replace → delta 版本）targets=%+v", got["lazer"], preview.Targets)
	}
	if got["delta"] != 0 {
		t.Fatalf("delta 计划项 = %d，期望 0", got["delta"])
	}
	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	var sawReplace bool
	for _, fr := range report.Forks {
		if fr.Deploy == nil {
			continue
		}
		for _, a := range fr.Deploy.Applied {
			if a.Action == deploy.ActionVersionReplace {
				sawReplace = true
			}
		}
	}
	if !sawReplace {
		t.Errorf("拍板应以 version-replace 落 lazer，applied=%+v", report.Forks)
	}
}

// TestListConflictsEnumeratesAllKinds 全类冲突枚举：数据页未展示的设置
// 类（ruleset_setting）与 game.ini 冲突也入清单，ContentKey 可直接回填
// PrepareSelected+Winner 处置。
func TestListConflictsEnumeratesAllKinds(t *testing.T) {
	aSeed := fixture.Seed{RulesetSettings: []fixture.SeedRulesetSetting{
		{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"topright"`},
	}}
	bSeed := fixture.Seed{RulesetSettings: []fixture.SeedRulesetSetting{
		{RulesetName: "osu", Variant: 0, Key: "hud_position", Value: `"bottomleft"`},
	}}
	b, _, refA, refB := newRunBackend(t, aSeed, bSeed)
	// ini 分歧：两端同一白名单键异值（无基线 → 冲突）。
	if _, err := fixture.WriteGameIni(refA.DataDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatalf("WriteGameIni(A): %v", err)
	}
	if _, err := fixture.WriteGameIni(refB.DataDir, map[string]string{"ShowFpsDisplay": "false"}); err != nil {
		t.Fatalf("WriteGameIni(B): %v", err)
	}
	// 先跑一轮收集把 presence/ini 快照落 hub（ListConflicts 读快照态）。
	if _, err := b.Sync.PrepareRun(); err != nil {
		t.Fatalf("PrepareRun(收集): %v", err)
	}

	rows, err := b.Compare.ListConflicts()
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	byKey := map[string]ConflictRow{}
	for _, r := range rows {
		byKey[r.Kind+"\x00"+r.ContentKey] = r
	}
	rsKey := hub.RulesetSettingContentKey("osu", 0, "hud_position")
	if r, ok := byKey[string(hub.KindRulesetSetting)+"\x00"+rsKey]; !ok {
		t.Errorf("ruleset_setting 冲突未枚举（keys=%v）", keysOf(byKey))
	} else {
		if !r.CanPickHub || r.Values < 2 || len(r.Forks) != 2 {
			t.Errorf("ruleset_setting 行面 = %+v", r)
		}
	}
	if r, ok := byKey["game_ini\x00ShowFpsDisplay"]; !ok {
		t.Errorf("game_ini 冲突未枚举（keys=%v）", keysOf(byKey))
	} else if r.CanPickHub {
		t.Errorf("game_ini 行不应允许「以中枢为准」: %+v", r)
	}
}

func keysOf(m map[string]ConflictRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPrepareSelectedWinnerGameIni ini 键拍板：两端白名单键异值冲突 →
// Winner=delta → 两端 ini 决定值=delta 值；CommitRun 后 lazer 的
// game.ini 实写为 delta 取值，下一轮该键不再冲突。
func TestPrepareSelectedWinnerGameIni(t *testing.T) {
	b, _, refA, refB := newRunBackend(t, fixture.Seed{}, fixture.Seed{})
	if _, err := fixture.WriteGameIni(refA.DataDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatalf("WriteGameIni(A): %v", err)
	}
	if _, err := fixture.WriteGameIni(refB.DataDir, map[string]string{"ShowFpsDisplay": "false"}); err != nil {
		t.Fatalf("WriteGameIni(B): %v", err)
	}
	preview, err := b.Sync.PrepareSelected([]Selection{
		{Kind: hub.EntityKind(hub.BaselineKindGameIni), ContentKey: "ShowFpsDisplay", Winner: "delta"},
	})
	if err != nil {
		t.Fatalf("PrepareSelected: %v", err)
	}
	if _, err := b.Sync.CommitRun(preview.RunID); err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(refA.DataDir, "game.ini"))
	if err != nil {
		t.Fatalf("读 lazer game.ini: %v", err)
	}
	if got, _ := inisettings.Parse(after).Value("ShowFpsDisplay"); got != "false" {
		t.Errorf("lazer game.ini ShowFpsDisplay = %q，期望 false（以 delta 为准）", got)
	}
	// 收敛：下一轮该键不再报冲突。
	p2, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun(2): %v", err)
	}
	if p2.Pending.ByKind["game_ini"] != 0 {
		t.Errorf("收敛后 game_ini 冲突 = %d，期望 0（pending=%+v）", p2.Pending.ByKind["game_ini"], p2.Pending)
	}
}

func mustBackups(t *testing.T, dir string) *guard.Backups {
	t.Helper()
	b, err := guard.OpenBackups(dir)
	if err != nil {
		t.Fatalf("OpenBackups: %v", err)
	}
	return b
}

// TestMockRunFlow mock 一轮同步冒烟（§0.4 + M-4 前端演示面）：
// scope 读写、PrepareRun 计数与 g0v0 守卫跳过、CommitRun 逐端报告、
// 未知 runID 报错。
func TestMockRunFlow(t *testing.T) {
	b := NewMockBackend("mock", "测试", nil)
	scope, err := b.Settings.GetSyncScope()
	if err != nil || len(scope.Forks) == 0 || len(scope.Sections) != len(AllSections) {
		t.Fatalf("GetSyncScope = (%+v,%v)", scope, err)
	}
	if err := b.Settings.SetSyncScope(scope); err != nil {
		t.Fatalf("SetSyncScope(不变更): %v", err)
	}
	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if preview.RunID == "" || len(preview.Targets) == 0 {
		t.Fatalf("预览 = %+v，期望有 runID 与目标端", preview)
	}
	var sawSkip bool
	for _, sk := range preview.Skipped {
		if sk.ForkID == "g0v0" {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Errorf("mock 应演示 g0v0 守卫跳过: %+v", preview.Skipped)
	}
	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	var applied, sawSkipped int
	for _, fr := range report.Forks {
		if fr.Skipped {
			sawSkipped++
		}
		if fr.Deploy != nil {
			applied += len(fr.Deploy.Applied)
		}
	}
	if sawSkipped == 0 {
		t.Error("报告缺跳过端")
	}
	if applied == 0 {
		t.Error("报告缺投放项")
	}
	// runID 单次消费：二次提交报错。
	if _, err := b.Sync.CommitRun(preview.RunID); err == nil {
		t.Fatal("重复 CommitRun 应报错（计划单次性）")
	}
	// PrepareSelected：预览计数 = 勾选数。
	preview2, err := b.Sync.PrepareSelected([]Selection{{Kind: hub.KindCollection, ContentKey: "x"}})
	if err != nil {
		t.Fatalf("PrepareSelected: %v", err)
	}
	for _, tp := range preview2.Targets {
		if tp.Items != 1 {
			t.Errorf("PrepareSelected 目标 %s 项数 = %d，期望 1", tp.ForkID, tp.Items)
		}
	}
}

// TestCommitRunGameIniOnlyWrites realm 无差异、目标端 game.ini 缺白名单
// 键行时 CommitRun 仍把决定值补缺写入（§0.4 步骤 7 独立于 realm 计划；
// 键缺席 = 无基线缺席 → 可补缺，非冲突）。
func TestCommitRunGameIniOnlyWrites(t *testing.T) {
	b, _, refA, refB := newRunBackend(t, fixture.Seed{}, fixture.Seed{})
	// A(lazer, 源+目标) 写白名单键真值；B(delta, 仅目标) 只留一行红灯键——
	// 白名单键在 B 文件里缺席 → 逐键判定为「可补缺」而非异值冲突。
	if _, err := fixture.WriteGameIni(refA.DataDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatalf("WriteGameIni(A): %v", err)
	}
	if err := os.WriteFile(filepath.Join(refB.DataDir, "game.ini"),
		[]byte("Token = delta-own-token\n"), 0o644); err != nil {
		t.Fatalf("写 delta 极简 game.ini: %v", err)
	}

	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	for _, tg := range preview.Targets {
		if tg.ForkID == "delta" && tg.Items != 0 {
			t.Fatalf("delta 计划项 = %d，期望 0（仅 ini 差异）", tg.Items)
		}
	}
	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	var got string
	var ok bool
	for _, fr := range report.Forks {
		if fr.ForkID != "delta" {
			continue
		}
		if fr.Error != "" {
			t.Fatalf("delta 报告错误: %s", fr.Error)
		}
		if fr.Deploy == nil || fr.Deploy.GameIni == nil {
			t.Fatalf("delta 报告缺 game.ini 结果: %+v", fr)
		}
		if fr.Deploy.GameIni.Applied["ShowFpsDisplay"] != "true" {
			t.Fatalf("game.ini applied = %+v，期望 ShowFpsDisplay=true", fr.Deploy.GameIni.Applied)
		}
	}
	after, err := os.ReadFile(filepath.Join(refB.DataDir, "game.ini"))
	if err != nil {
		t.Fatalf("读 delta game.ini: %v", err)
	}
	if got, ok = inisettings.Parse(after).Value("ShowFpsDisplay"); !ok || got != "true" {
		t.Errorf("delta game.ini ShowFpsDisplay = %q，期望 true", got)
	}
	if got, _ := inisettings.Parse(after).Value("Token"); got != "delta-own-token" {
		t.Errorf("delta 红灯键 Token 被触碰: %q", got)
	}
}

// TestRunGameIniDivergenceStaysPending 两端同一白名单键取值不同且无
// 基线 → 逐键冲突（§0.3 规则 5：无基线异值 = 冲突，保留差异不写）。
func TestRunGameIniDivergenceStaysPending(t *testing.T) {
	b, _, refA, refB := newRunBackend(t, fixture.Seed{}, fixture.Seed{})
	if _, err := fixture.WriteGameIni(refA.DataDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatalf("WriteGameIni(A): %v", err)
	}
	if _, err := fixture.WriteGameIni(refB.DataDir, map[string]string{"ShowFpsDisplay": "false"}); err != nil {
		t.Fatalf("WriteGameIni(B): %v", err)
	}
	preview, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if preview.Pending.Conflicts < 1 {
		t.Errorf("Pending.Conflicts = %d，期望 ≥1（ini 异值冲突）", preview.Pending.Conflicts)
	}
	report, err := b.Sync.CommitRun(preview.RunID)
	if err != nil {
		t.Fatalf("CommitRun: %v", err)
	}
	for _, fr := range report.Forks {
		if fr.Deploy != nil && fr.Deploy.GameIni != nil &&
			len(fr.Deploy.GameIni.Applied) != 0 {
			t.Errorf("ini 冲突端 %s 仍落写: %+v", fr.ForkID, fr.Deploy.GameIni.Applied)
		}
	}
	after, err := os.ReadFile(filepath.Join(refB.DataDir, "game.ini"))
	if err != nil {
		t.Fatalf("读 delta game.ini: %v", err)
	}
	if got, _ := inisettings.Parse(after).Value("ShowFpsDisplay"); got != "false" {
		t.Errorf("冲突键 delta ShowFpsDisplay = %q，期望保持 false", got)
	}
}

// TestRealKeepDifference 「接受差异」（§0-B12/G13）：双端 game.ini 异值无
// 基线 → 冲突枚举；KeepDifference 把各端基线推进为各自当前值后——冲突
// 不再枚举、判定面收敛；一端再改动照常传播（A→「maybe」决定值写入 B 的
// game.ini）。
func TestRealKeepDifference(t *testing.T) {
	b, _, refA, refB := newRunBackend(t, fixture.Seed{}, fixture.Seed{})
	if _, err := fixture.WriteGameIni(refA.DataDir, map[string]string{"ShowFpsDisplay": "true"}); err != nil {
		t.Fatalf("WriteGameIni(A): %v", err)
	}
	if _, err := fixture.WriteGameIni(refB.DataDir, map[string]string{"ShowFpsDisplay": "false"}); err != nil {
		t.Fatalf("WriteGameIni(B): %v", err)
	}
	sel := []Selection{{Kind: hub.EntityKind(hub.BaselineKindGameIni), ContentKey: "ShowFpsDisplay"}}

	// 空勾选与未命中键都如实报错，不静默成功。
	if err := b.Sync.KeepDifference(nil); err == nil {
		t.Fatalf("空勾选应报错")
	}
	if err := b.Sync.KeepDifference([]Selection{
		{Kind: hub.EntityKind(hub.BaselineKindGameIni), ContentKey: "NotWhitelisted"}},
	); err == nil {
		t.Fatalf("未命中判定单位的勾选应报错")
	}

	if _, err := b.Sync.PrepareRun(); err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	rows, err := b.Compare.ListConflicts()
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	var found bool
	for _, r := range rows {
		if r.Kind == hub.BaselineKindGameIni && r.ContentKey == "ShowFpsDisplay" {
			found = true
		}
	}
	if !found {
		t.Fatalf("两端异值应枚举为冲突: %+v", rows)
	}

	if err := b.Sync.KeepDifference(sel); err != nil {
		t.Fatalf("KeepDifference: %v", err)
	}
	rows, err = b.Compare.ListConflicts()
	if err != nil {
		t.Fatalf("ListConflicts(after): %v", err)
	}
	for _, r := range rows {
		if r.Kind == hub.BaselineKindGameIni && r.ContentKey == "ShowFpsDisplay" {
			t.Fatalf("保留差异后冲突不应再枚举: %+v", r)
		}
	}
	p, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun(after): %v", err)
	}
	for _, tp := range p.Targets {
		if tp.Items > 0 {
			t.Fatalf("收敛后不应有写计划: %+v", p.Targets)
		}
	}

	// A 再改动 → 照常传播：决定值「maybe」写入 B 的 game.ini。
	if _, err := fixture.WriteGameIni(refA.DataDir, map[string]string{"ShowFpsDisplay": "maybe"}); err != nil {
		t.Fatalf("改 A ini: %v", err)
	}
	p2, err := b.Sync.PrepareRun()
	if err != nil {
		t.Fatalf("PrepareRun(again): %v", err)
	}
	if _, err := b.Sync.CommitRun(p2.RunID); err != nil {
		t.Fatalf("CommitRun(again): %v", err)
	}
	after, err := os.ReadFile(filepath.Join(refB.DataDir, "game.ini"))
	if err != nil {
		t.Fatalf("读 delta game.ini: %v", err)
	}
	if got, _ := inisettings.Parse(after).Value("ShowFpsDisplay"); got != "maybe" {
		t.Errorf("保留差异后的新改动应传播到 B: ShowFpsDisplay = %q，期望 maybe", got)
	}
}

// TestPrepareRunDecideInputErrorFails 判定输入装载 fail-closed：presence
// 读不出 → PrepareRun 整体报错（判不准不动升级为不跑——半个判定面产出
// 的计划比没有计划更危险）。注入方式：收集用缓存 runner 的原 hub 指针，
// 判定面读的是 deps.Hub——换成已关闭的 store。
func TestPrepareRunDecideInputErrorFails(t *testing.T) {
	aSeed, err := fixture.StandardSeed()
	if err != nil {
		t.Fatalf("StandardSeed: %v", err)
	}
	b, _, _, _ := newRunBackend(t, aSeed, fixture.Seed{})
	rs := b.Sync.(*realSync)
	rs.cRunner() // 预热缓存：收集器持原 hub
	bad, err := hub.Open(filepath.Join(t.TempDir(), "bad.db"))
	if err != nil {
		t.Fatalf("hub.Open(bad): %v", err)
	}
	bad.Close()
	rs.deps.Hub = bad

	if _, err := b.Sync.PrepareRun(); err == nil ||
		!strings.Contains(err.Error(), "三方判定") {
		t.Fatalf("PrepareRun = %v，期望判定面报错", err)
	}
}
