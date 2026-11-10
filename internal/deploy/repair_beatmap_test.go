package deploy

import (
	"context"
	"testing"
	"time"

	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

func TestPlanRepairFillsIDFromSameRealmAndDoesNotInventStatus(t *testing.T) {
	live := beatmapSnap{
		hash: "h2", onlineID: -1, status: 0,
		diffs: []diffSnap{{hash: "d2", md5: "m2", name: "Only", online: -1, status: 0}},
	}
	hubEv := idEvidence{
		setID:      2002,
		diffByHash: map[string]int64{"d2": 20020},
		diffByMD5:  map[string]int64{},
	}
	rep, ok := planOneRepair(live, nil, map[string]beatmapSnap{"h2": live}, hubEv)
	if !ok {
		t.Fatal("应补回谱面 id")
	}
	if rep.setID != 2002 || len(rep.diffs) != 1 || rep.diffs[0].onlineID != 20020 {
		t.Fatalf("id 修复 = %+v", rep)
	}
	if rep.writeSetStatus || rep.diffs[0].writeStatus {
		t.Fatal("不能用其他端的状态给这张图凭空标成 Ranked/Approved")
	}
}

func TestPlanRepairRestoresStatusOnlyFromLocalHistory(t *testing.T) {
	old := beatmapSnap{
		hash: "h1", deletePending: true, onlineID: 2002, status: 2,
		diffs: []diffSnap{{hash: "d1", md5: "m1", name: "Only", online: 20020, status: 2}},
	}
	live := beatmapSnap{
		hash: "h2", onlineID: -1, status: -2,
		diffs: []diffSnap{{hash: "d2", md5: "m2", name: "Only", online: -1, status: -2}},
	}
	by := map[string]beatmapSnap{"h1": old, "h2": live}
	rep, ok := planOneRepair(live, []string{"h1"}, by, idEvidence{
		setID: 9999, diffByHash: map[string]int64{"d2": 99990}, diffByMD5: map[string]int64{},
	})
	if !ok {
		t.Fatal("应从本库软删行恢复")
	}
	if rep.setID != 2002 {
		t.Errorf("集 OnlineID = %d，期望本库旧值 2002，而不是 hub 的 9999", rep.setID)
	}
	if !rep.writeSetStatus || rep.setStatus != 2 {
		t.Errorf("集状态 = %d write=%v，期望恢复 Approved(2)", rep.setStatus, rep.writeSetStatus)
	}
	if len(rep.diffs) != 1 || rep.diffs[0].onlineID != 20020 || !rep.diffs[0].writeStatus || rep.diffs[0].status != 2 {
		t.Fatalf("难度修复 = %+v", rep.diffs)
	}
}

func TestPlanRepairDoesNotReplacePositiveID(t *testing.T) {
	live := beatmapSnap{
		hash: "h", onlineID: 7, status: 1,
		diffs: []diffSnap{{hash: "d", md5: "m", name: "Only", online: 70, status: 1}},
	}
	hubEv := idEvidence{setID: 99, diffByHash: map[string]int64{"d": 990}, diffByMD5: map[string]int64{}}
	if _, ok := planOneRepair(live, nil, map[string]beatmapSnap{"h": live}, hubEv); ok {
		t.Fatal("已有正数谱面 id 且状态可见时不应改写")
	}
}

func TestPlanRepairSkipsConflictingLocalIDs(t *testing.T) {
	a := beatmapSnap{hash: "a", onlineID: 1, status: 1}
	b := beatmapSnap{hash: "b", onlineID: 2, status: 1}
	live := beatmapSnap{
		hash: "h", onlineID: -1, status: 1,
		diffs: []diffSnap{{hash: "d", name: "Only", online: 5, status: 1}},
	}
	by := map[string]beatmapSnap{"a": a, "b": b, "h": live}
	rep, ok := planOneRepair(live, []string{"a", "b"}, by, idEvidence{})
	if ok && rep.setID != 0 {
		t.Fatalf("冲突的本库 id 不应被选中: %+v", rep)
	}
}

func TestRepairBeatmapLeaderboardsEndToEnd(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	files := map[string][]byte{}
	v1, err := fixture.MaterializeSet(2002, "Repair Artist", "Repair Title", "mapper",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("osu file format v14\nv1\n")}}, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := fixture.MaterializeSet(2002, "Repair Artist", "Repair Title", "mapper",
		[]fixture.DiffMaterial{{Name: "Only", OsuBytes: []byte("osu file format v14\nv2\n")}}, nil, files)
	if err != nil {
		t.Fatal(err)
	}
	realmV1 := cloneSet(v1)
	realmV1.DeletePending = true
	realmV1.StatusInt = 2
	realmV1.Beatmaps[0].StatusInt = 2
	realmV2 := cloneSet(v2)
	realmV2.OnlineID = -1
	realmV2.StatusInt = 0
	realmV2.Beatmaps[0].OnlineID = -1
	realmV2.Beatmaps[0].StatusInt = -2
	sc, err := fixture.MaterializeScore(realmV2.Beatmaps[0], "g0v0", []byte("keep this score id"), files)
	if err != nil {
		t.Fatal(err)
	}
	sc.OnlineID = 777001
	ref, err := fixture.Build(ctx, fixture.Spec{
		ForkID: fixture.ForkG0v0, SchemaVersion: fixture.VersionG0v0,
		DataDir: t.TempDir(),
		Seed: fixture.Seed{
			BeatmapSets: []hub.BeatmapSet{realmV1, realmV2},
			Scores:      []fixture.SeedScore{sc},
			Files:       files,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: "lazer", CollectedAt: t0, BeatmapSets: []hub.BeatmapSet{v1}})
	applySnap(t, h, hub.Snapshot{ForkID: "lazer", CollectedAt: t0.Add(time.Hour), BeatmapSets: []hub.BeatmapSet{v2}})
	n, err := NewRunner(Deps{Hub: h}).RepairBeatmapLeaderboards(ctx, ref)
	if err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if n != 1 {
		t.Fatalf("修复行数 = %d，期望 1", n)
	}
	withSetReader(t, lib, ref.RealmPath, func(sr *setReader) {
		got, ok := sr.readSetProjection(realmV2.Hash)
		if !ok {
			t.Fatal("活行丢失")
		}
		if got.OnlineID != v1.OnlineID || got.StatusInt != 2 {
			t.Errorf("集 = id %d status %d，期望 id %d status 2", got.OnlineID, got.StatusInt, v1.OnlineID)
		}
		if len(got.Beatmaps) != 1 || got.Beatmaps[0].OnlineID != v1.Beatmaps[0].OnlineID || got.Beatmaps[0].StatusInt != 2 {
			t.Errorf("难度 = id %d status %d，期望 id %d status 2",
				got.Beatmaps[0].OnlineID, got.Beatmaps[0].StatusInt, v1.Beatmaps[0].OnlineID)
		}
		old, ok := sr.readSetProjection(realmV1.Hash)
		if !ok || !old.DeletePending || old.OnlineID != v1.OnlineID {
			t.Fatalf("软删旧行被改动: ok=%v pending=%v id=%d", ok, old.DeletePending, old.OnlineID)
		}
		obj, err := sr.r.FindFirstByString(sr.class("Score"), sr.prop("Score", "Hash"), sc.Hash)
		if err != nil || obj == nil {
			t.Fatalf("成绩丢失: %v", err)
		}
		defer sr.r.ReleaseObject(obj)
		if gotID := sr.int64Of(obj, "Score", "OnlineID"); gotID != sc.OnlineID {
			t.Errorf("成绩 OnlineID = %d，期望保持 %d", gotID, sc.OnlineID)
		}
	})
	n2, err := NewRunner(Deps{Hub: h}).RepairBeatmapLeaderboards(ctx, ref)
	if err != nil || n2 != 0 {
		t.Fatalf("第二次修复 = %d err=%v，期望 0（已修好就不动）", n2, err)
	}
}

func cloneSet(in hub.BeatmapSet) hub.BeatmapSet {
	out := in
	out.Beatmaps = append([]hub.Beatmap(nil), in.Beatmaps...)
	return out
}
