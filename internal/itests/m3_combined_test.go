package itests

// M3 组合全链验收（design.md §7-M3-3/4/5、§5.1-M3）：设置类三类 + game.ini
// 白名单键 + delta gimmick 告警 + .osz 导出，两 fork（delta 源 → lazer
// 目标）全类型一轮：真实收集（含 ini 面）→ BuildPlan（gimmick 集告警
// 标记 + 全类型 create）→ Deploy（三类落地 + 四条跳过分支计数〔bms
// 设置/预设、EZX 名录外、键位 D20 越域〕+ 报告 GimmickWarnings 与预演
// 一致）→ SyncGameIni（白名单键跨端落地、红灯键
// 原样）→ WriteOSZ（hub 链尾 + 仓 → 解包逐条与仓内字节一致，gimmick
// section 头随 .osu 完整带出）→ 重投影（M3 三类 presence 计数收口）。
// librealm 缺失时显式 SKIP（§8.1）。

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"lzsync/internal/container"
	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
	"lzsync/internal/inisettings"
)

// buildSeedLibWithIni 造库（带种子）+ 数据目录 game.ini 夹具（组合场景的
// buildIniLib 全种子形态：设置类/谱面集与 ini 面同库承载）。
func buildSeedLibWithIni(t *testing.T, forkID string, version uint64, seed fixture.Seed, overrides map[string]string) (string, forks.LibraryRef) {
	t.Helper()
	mustLib(t)
	dir := t.TempDir()
	ref, err := fixture.Build(context.Background(), fixture.Spec{
		ForkID: forkID, SchemaVersion: version, DataDir: dir, Seed: seed,
	})
	if err != nil {
		t.Fatalf("fixture.Build(%s): %v", forkID, err)
	}
	if _, err := fixture.WriteGameIni(dir, overrides); err != nil {
		t.Fatal(err)
	}
	return dir, ref
}

func TestM3CombinedRoundtrip(t *testing.T) {
	mustLib(t)
	ctx := context.Background()
	e := newEnv(t)
	iniStore, err := inisettings.OpenStore(filepath.Join(e.dir, "gameini"))
	if err != nil {
		t.Fatalf("OpenStore(gameini): %v", err)
	}

	// 源 delta 座：gimmick 种子 + M3 设置类三类 + bms 规则集行（预设/设置
	// 引用解析面）+ game.ini 夹具（ShowFpsDisplay=true——目标模板为 false，
	// 该键应经全链落地）。
	seed, err := fixture.DeltaGimmickSeed()
	if err != nil {
		t.Fatalf("DeltaGimmickSeed: %v", err)
	}
	settings, err := fixture.SettingsSeed()
	if err != nil {
		t.Fatalf("SettingsSeed: %v", err)
	}
	seed.KeyBindings = settings.KeyBindings
	seed.RulesetSettings = settings.RulesetSettings
	seed.ModPresets = settings.ModPresets
	seed.ExtraRulesets = []fixture.SeedRuleset{{
		ShortName:         "bms",
		OnlineID:          -1,
		Name:              "Beatmap Spectrum",
		InstantiationInfo: "osu.Game.Rulesets.BMS.BMSRuleset, osu.Game.Rulesets.BMS",
	}}
	_, refSrc := buildSeedLibWithIni(t, fixture.ForkDelta, fixture.VersionDelta, seed,
		map[string]string{"ShowFpsDisplay": "true"})
	dstDir, refDst := buildSeedLibWithIni(t, fixture.ForkLazer, fixture.VersionLazer,
		fixture.Seed{}, nil)

	// ---- 收集（含 ini 面）：全类型入仓 + 软删预设跳过计数 ----
	rep := e.collectIntoWithIni(iniStore, refSrc)
	for kind, want := range map[hub.EntityKind]int{
		hub.KindBeatmapSet: 2, hub.KindCollection: 1, hub.KindScore: 1,
		hub.KindKeyBinding: 5, hub.KindRulesetSetting: 3, hub.KindModPreset: 4,
	} {
		if got := rep.Entities[kind]; got != want {
			t.Fatalf("源收集 Entities[%s] = %d，期望 %d", kind, got, want)
		}
	}
	if got := collectSkip(rep, "软删模组预设"); got != 1 {
		t.Errorf("收集侧软删预设跳过 = %d，期望 1（Skips=%+v）", got, rep.Skips)
	}
	if rep.IniKeysCollected <= 0 {
		t.Fatalf("IniKeysCollected = %d，期望 > 0", rep.IniKeysCollected)
	}
	values, _, ok, err := iniStore.Load("delta")
	if err != nil || !ok {
		t.Fatalf("ini 快照 Load = ok:%v err:%v", ok, err)
	}
	if values["ShowFpsDisplay"] != "true" {
		t.Fatalf("delta 快照 ShowFpsDisplay = %q，期望 true", values["ShowFpsDisplay"])
	}

	// ---- 计划：gimmick 集告警标记（R6）+ 全类型 create ----
	plan, err := deploy.BuildPlan(ctx, e.hub, refDst, deploy.BuildOptions{Files: e.engine.Store})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	gimmickSet, stdSet := seed.BeatmapSets[1], seed.BeatmapSets[0]
	counts := map[string]int{}
	for _, it := range plan.Items {
		if it.Action != deploy.ActionCreate {
			t.Fatalf("计划项 %+v 非预期动作（空目标全 create）", it)
		}
		switch {
		case it.Kind == hub.KindBeatmapSet && it.ContentKey == gimmickSet.Hash:
			if it.Warning == "" {
				t.Error("gimmick 集计划项缺告警标记（R6）")
			}
		case it.Kind == hub.KindBeatmapSet && it.ContentKey == stdSet.Hash:
			if it.Warning != "" {
				t.Error("标准谱面集误报告警")
			}
		}
		counts[string(it.Kind)]++
	}
	for kind, want := range map[hub.EntityKind]int{
		hub.KindBeatmapSet: 2, hub.KindCollection: 1,
		hub.KindKeyBinding: 5, hub.KindRulesetSetting: 3, hub.KindModPreset: 4,
	} {
		if got := counts[string(kind)]; got != want {
			t.Fatalf("计划 %s 项 = %d，期望 %d（plan=%+v）", kind, got, want, plan.Items)
		}
	}

	// ---- 投放：三类落地 + 三条跳过分支计数 + gimmick 报告与预演一致 ----
	report, err := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: guard.NewGuard(nil),
	}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v（report=%+v）", err, report)
	}
	if len(report.FailedBatches) != 0 || len(report.NotRun) != 0 {
		t.Fatalf("失败/未执行段非空: %+v / %+v", report.FailedBatches, report.NotRun)
	}
	appliedKinds := map[hub.EntityKind]int{}
	for _, ap := range report.Applied {
		appliedKinds[ap.Kind]++
	}
	// 键位 4 行落地：SettingsSeed 的全局 ActionInt=100 越源端 delta 枚举
	// 域 [0,99) 被 D20 拒（官方代新增、delta 基线无此枚举，域数据见
	// ForkProfile.KeyBindingAction）。
	for kind, want := range map[hub.EntityKind]int{
		hub.KindBeatmapSet: 2, hub.KindCollection: 1,
		hub.KindKeyBinding: 4, hub.KindRulesetSetting: 2, hub.KindModPreset: 2,
	} {
		if got := appliedKinds[kind]; got != want {
			t.Errorf("Applied[%s] = %d，期望 %d（Applied=%+v）", kind, got, want, report.Applied)
		}
	}
	if got := deploySkip(report, "规则集设置引用规则集 \"bms\" 在目标不可解析"); got != 1 {
		t.Errorf("fork 专有规则集设置跳过 = %d，期望 1（Skipped=%+v）", got, report.Skipped)
	}
	if got := deploySkip(report, "模组预设 \"BMS Preset\" 引用规则集"); got != 1 {
		t.Errorf("fork 专有规则集预设跳过 = %d，期望 1", got)
	}
	if got := deploySkip(report, "含目标端不认识的 mod \"EZX\""); got != 1 {
		t.Errorf("D7 名录外 mod 预设跳过 = %d，期望 1", got)
	}
	// 键位 D20（ActionInt 枚举域交集过滤，§7-M3-3）：全局 ActionInt=100
	// 超出源端 delta 枚举域——越域跳过且不落地。
	if got := deploySkip(report, "D20 越域跳过"); got != 1 {
		t.Errorf("键位 D20 越域跳过 = %d，期望 1（Skipped=%+v）", got, report.Skipped)
	}
	if len(report.GimmickWarnings) != 1 ||
		report.GimmickWarnings[0].Kind != hub.KindBeatmapSet ||
		report.GimmickWarnings[0].ContentKey != gimmickSet.Hash {
		t.Errorf("GimmickWarnings = %+v（期望恰 1 条 gimmick 集，与预演一致）", report.GimmickWarnings)
	}

	// ---- game.ini：delta 快照 → lazer 目标（白名单键落地、红灯键原样）----
	iniPath := filepath.Join(dstDir, "game.ini")
	before, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatalf("读目标 game.ini: %v", err)
	}
	iniRes, err := deploy.SyncGameIni(refDst, "delta", nil, iniStore, e.backups, guard.NewGuard(nil))
	if err != nil {
		t.Fatalf("SyncGameIni: %v", err)
	}
	if iniRes.Applied["ShowFpsDisplay"] != "true" {
		t.Errorf("ini Applied = %v，期望含 ShowFpsDisplay=true", iniRes.Applied)
	}
	after, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatalf("重读目标 game.ini: %v", err)
	}
	for _, line := range bytes.Split(after, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("Token = ")) {
			if !bytes.Contains(before, line) {
				t.Errorf("目标 Token 行被触碰: %q（红灯键 S10）", line)
			}
		}
	}
	if iniRes.BackupPath == "" {
		t.Error("ini 投放缺备份路径（S2/S12）")
	}

	// ---- .osz 导出：hub 链尾 + 仓 → 解包逐条与仓内字节一致（§7-M3-5）----
	openBlob := func(hash string) (io.ReadCloser, error) {
		return os.Open(e.engine.Store.Path(hash))
	}
	var oszBuf bytes.Buffer
	if err := container.WriteOSZ(&oszBuf, gimmickSet, openBlob); err != nil {
		t.Fatalf("WriteOSZ: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(oszBuf.Bytes()), int64(oszBuf.Len()))
	if err != nil {
		t.Fatalf("zip 打开: %v", err)
	}
	entries := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("zip 条目 %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close() //nolint:errcheck
		if err != nil {
			t.Fatalf("zip 条目 %s 读取: %v", f.Name, err)
		}
		entries[f.Name] = b
	}
	if len(entries) != len(gimmickSet.Files) {
		t.Fatalf(".osz 条目数 = %d（%v），期望 %d", len(entries), entries, len(gimmickSet.Files))
	}
	for _, nf := range gimmickSet.Files {
		b, ok := entries[nf.Filename]
		if !ok {
			t.Fatalf(".osz 缺条目 %s（清单：%v）", nf.Filename, entries)
		}
		if !bytes.Equal(b, seed.Files[nf.Hash]) {
			t.Errorf(".osz 条目 %s 字节与仓不符", nf.Filename)
		}
	}
	// gimmick section 头随 .osu 完整带出（容器形态不裁剪 delta 专有数据）。
	gimmickOsu, ok := entries["Gimmick.osu"]
	if !ok || !bytes.Contains(gimmickOsu, []byte("[BeatmapSectionGimmicks]")) {
		t.Errorf(".osz 的 Gimmick.osu 未带 gimmick section 头（entries=%v）", entries)
	}

	// ---- 重投影：再收集目标 → M3 三类 presence 计数收口 ----
	e.collectInto(refDst)
	// 键位 presence 4：全局 ActionInt=100 行未落地（D20 越域拒）。
	for kind, want := range map[hub.EntityKind]int{
		hub.KindKeyBinding: 4, hub.KindRulesetSetting: 2, hub.KindModPreset: 2,
	} {
		assertHubCount(t, e, `SELECT COUNT(*) FROM presence WHERE fork_id=? AND kind=?`,
			want, "lazer 重投影 presence "+string(kind), "lazer", kind)
	}
}
