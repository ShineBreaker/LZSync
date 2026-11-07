package itests

// stable 端跨端闭环验收（stable-legacy §4 对齐锚点）：fixture 造官方
// lazer 库（一集一收藏）→ 真实收集入 hub → 部署到 stable 目录（链接式
// Songs/ + collection.db 原子重写）→ 重新收集 stable → 断言两端
// presence 同集哈希（跨端同键）、收藏同键、Songs/ 文件为指向仓的链接。
// lazer 侧建库依赖 librealm（缺失时显式 SKIP，§8.1）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lzsync/internal/deploy"
	"lzsync/internal/fixture"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
)

func TestStableCrossEndRoundTrip(t *testing.T) {
	mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	// ---- 夹具：lazer 库（一集两难度 + 一收藏）----
	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(9001, "Stable Artist", "Stable Roundtrip", "mapperS",
		[]fixture.DiffMaterial{
			{Name: "Easy", OsuBytes: []byte("stable roundtrip easy osu")},
			{Name: "Hard", OsuBytes: []byte("stable roundtrip hard osu")},
		}, []byte("stable roundtrip audio"), files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	refLazer := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{set},
		Files:       files,
	})
	rep := e.collectInto(refLazer)
	if rep.Entities[hub.KindBeatmapSet] != 1 {
		t.Fatalf("lazer 收集集数 = %d（夹具异常）", rep.Entities[hub.KindBeatmapSet])
	}

	// ---- 投放 stable：链接式目录 + 收藏（决定值载荷直给）----
	stableRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stableRoot, "Songs"), 0o755); err != nil {
		t.Fatal(err)
	}
	// stable 投放需链接引擎（realm 端 runner 无 Files——按需自建，与
	// fullchain 同款就地装配先例）。
	dr := deploy.NewRunner(deploy.Deps{
		Hub: e.hub, Files: e.engine, Backups: e.backups, Guard: hermeticGuard(),
	})
	report, err := dr.Deploy(ctx, deploy.Plan{
		TargetFork: "stable",
		Target:     forks.LibraryRef{ForkID: "stable", DataDir: stableRoot},
		Items: []deploy.PlanItem{
			{Kind: hub.KindBeatmapSet, ContentKey: set.Hash, Action: deploy.ActionCreate},
			{Kind: hub.KindCollection, ContentKey: "favs", Action: deploy.ActionCreate,
				PayloadJSON: `{"Name":"favs","BeatmapMD5Hashes":["` + set.Beatmaps[0].MD5Hash + `"]}`},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Deploy(stable): %v", err)
	}
	if len(report.Applied) != 2 {
		t.Fatalf("applied = %+v skipped = %+v", report.Applied, report.Skipped)
	}

	// ---- 重新收集 stable：跨端同键断言 ----
	refStable := forks.LibraryRef{ForkID: "stable", DataDir: stableRoot}
	repS := e.collectInto(refStable)
	// 集在 lazer 侧首见（B2：stable 只进 presence）；收藏 favs 首见来源
	// 恰是 stable（lazer 夹具无收藏），实体插入合法。
	if repS.Entities[hub.KindBeatmapSet] != 0 {
		t.Fatalf("stable 收集集实体 = %d（应只进 presence——B2 首见即定）", repS.Entities[hub.KindBeatmapSet])
	}
	if repS.Entities[hub.KindCollection] != 1 {
		t.Fatalf("stable 收藏实体 = %d（首见应入库）", repS.Entities[hub.KindCollection])
	}
	// presence 同集哈希：stable 目录复算的集哈希 == lazer 端集哈希（§4 锚点）。
	var n int
	if err := e.hub.DB().QueryRow(
		`SELECT count(*) FROM presence WHERE fork_id='stable' AND kind='beatmap_set' AND content_key=?`,
		set.Hash).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stable presence 同集哈希缺失（跨端同键失败）: n=%d err=%v", n, err)
	}
	if err := e.hub.DB().QueryRow(
		`SELECT count(*) FROM presence WHERE fork_id='stable' AND kind='collection' AND content_key='favs'`,
	).Scan(&n); err != nil || n != 1 {
		t.Fatalf("stable 收藏 presence 缺失: n=%d err=%v", n, err)
	}

	// ---- 游戏侧形态：Songs/ 文件为链接、collection.db 成员可解析 ----
	songsDir := filepath.Join(stableRoot, "Songs")
	entries, err := os.ReadDir(songsDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Songs/ 目录数 = %d err=%v（期望 1）", len(entries), err)
	}
	setDir := filepath.Join(songsDir, entries[0].Name())
	files2, err := os.ReadDir(setDir)
	if err != nil {
		t.Fatal(err)
	}
	linkCount := 0
	for _, fe := range files2 {
		if fe.Type()&os.ModeSymlink != 0 {
			linkCount++
		}
	}
	if linkCount != len(files2) || linkCount < 3 {
		t.Fatalf("集目录链接文件 = %d/%d（期望全部为链接且 ≥3：两 .osu + 音频）", linkCount, len(files2))
	}
	if _, err := os.Stat(filepath.Join(stableRoot, "collection.db")); err != nil {
		t.Fatalf("collection.db 未落地: %v", err)
	}

	// 重投幂等：全部跳过（同内容复核 + 收藏重续）。
	report2, err := dr.Deploy(ctx, deploy.Plan{
		TargetFork: "stable",
		Target:     refStable,
		Items: []deploy.PlanItem{
			{Kind: hub.KindBeatmapSet, ContentKey: set.Hash, Action: deploy.ActionCreate},
			{Kind: hub.KindCollection, ContentKey: "favs", Action: deploy.ActionCreate,
				PayloadJSON: `{"Name":"favs","BeatmapMD5Hashes":["` + set.Beatmaps[0].MD5Hash + `"]}`},
		},
	}, nil)
	if err != nil {
		t.Fatalf("重投: %v", err)
	}
	if len(report2.Applied) != 0 || len(report2.Skipped) < 2 {
		t.Fatalf("重投 applied=%+v skipped=%+v（期望全跳过）", report2.Applied, report2.Skipped)
	}
}
