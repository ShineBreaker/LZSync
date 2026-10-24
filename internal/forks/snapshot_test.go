package forks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lzsync/internal/realmc"
)

func sampleSnap(forkID string, v uint64, path string, at time.Time) *StructureSnapshot {
	return &StructureSnapshot{
		ForkID:      forkID,
		DiskVersion: v,
		SourcePath:  path,
		TakenAt:     at,
		Classes: []ClassSnapshot{{
			Name: "Score", PrimaryKey: "ID",
			Properties: []PropertySnapshot{
				{Name: "ID", Type: realmc.PropTypeString, PrimaryKey: true},
				{Name: "TotalScore", Type: realmc.PropTypeInt},
				{Name: "JudgeMode", Type: realmc.PropTypeInt},
			},
		}},
	}
}

func TestSnapshotStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSnapshotStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if store.Dir != dir {
		t.Errorf("Dir = %s, 期望 %s", store.Dir, dir)
	}
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	snap := sampleSnap("ez2lazer", 52011, "/data/osu-Ez2Lazer/client_52011.realm", at)

	saved, err := store.SaveIfAbsent(snap)
	if err != nil || !saved {
		t.Fatalf("首次 SaveIfAbsent = (%v, %v), 期望 (true, nil)", saved, err)
	}
	// 落盘文件名 {fork}-{diskVersion}.json（§3.4 snapshots/ 布局）。
	if _, err := os.Stat(filepath.Join(dir, "ez2lazer-52011.json")); err != nil {
		t.Errorf("档案文件名不合约定: %v", err)
	}

	got, err := store.Load("ez2lazer", 52011)
	if err != nil || got == nil {
		t.Fatalf("Load = (%v, %v)", got, err)
	}
	if got.ForkID != snap.ForkID || got.DiskVersion != snap.DiskVersion ||
		got.SourcePath != snap.SourcePath || !got.TakenAt.Equal(at) || len(got.Classes) != 1 {
		t.Errorf("往返不一致: %+v", got)
	}
	if len(got.Classes[0].Properties) != 3 || !got.Classes[0].Properties[0].PrimaryKey {
		t.Errorf("属性档案不符: %+v", got.Classes[0].Properties)
	}
}

func TestSnapshotSaveIfAbsentKeepsFirst(t *testing.T) {
	store, err := OpenSnapshotStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := sampleSnap("lazer", 52, "/a", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := store.SaveIfAbsent(first); err != nil {
		t.Fatal(err)
	}
	// 同 (fork, diskVersion) 第二份：不覆盖，首份保留（同版本只留首份）。
	second := sampleSnap("lazer", 52, "/b", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC))
	saved, err := store.SaveIfAbsent(second)
	if err != nil || saved {
		t.Fatalf("二次 SaveIfAbsent = (%v, %v), 期望 (false, nil)", saved, err)
	}
	got, err := store.Load("lazer", 52)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.SourcePath != "/a" {
		t.Errorf("首份被覆盖: SourcePath = %s, 期望 /a", got.SourcePath)
	}
}

func TestSnapshotLoadMissingAndCorrupt(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSnapshotStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 不存在 → (nil, nil)。
	got, err := store.Load("g0v0", 52)
	if err != nil || got != nil {
		t.Errorf("缺失档案 Load = (%v, %v), 期望 (nil, nil)", got, err)
	}
	// 坏文件按不存在处理（ezrealmsync-design §4.1）。
	if err := os.WriteFile(filepath.Join(dir, "delta-51.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load("delta", 51)
	if err != nil || got != nil {
		t.Errorf("坏档案 Load = (%v, %v), 期望 (nil, nil)", got, err)
	}
}

func TestFindBySourcePath(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenSnapshotStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	p := filepath.Join(root, "client.realm")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	abs, _ := filepath.Abs(p)

	if _, err := store.SaveIfAbsent(sampleSnap("g0v0", 52, abs, time.Now())); err != nil {
		t.Fatal(err)
	}
	// 干扰项：其他路径 / 其他 fork 的档案。
	if _, err := store.SaveIfAbsent(sampleSnap("lazer", 52, "/other/client.realm", time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveIfAbsent(sampleSnap("ez2lazer", 51011, "/ez/client_51011.realm", time.Now())); err != nil {
		t.Fatal(err)
	}

	got := store.findBySourcePath(p)
	if got == nil || got.ForkID != "g0v0" || got.DiskVersion != 52 {
		t.Fatalf("findBySourcePath = %+v, 期望 g0v0/52", got)
	}
	if s := store.findBySourcePath(filepath.Join(root, "nope.realm")); s != nil {
		t.Errorf("未知路径应返回 nil, got %+v", s)
	}
}

// TestCaptureRealRealm 采集链路（native；缺失时显式 SKIP）。
func TestCaptureRealRealm(t *testing.T) {
	lib := mustLoadNative(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "client_52011.realm")
	r, err := lib.OpenWithSchema(p, &realmc.Schema{Classes: []realmc.ClassDef{{
		Name: "BeatmapSet", PrimaryKey: "ID",
		Properties: []realmc.PropertyDef{
			{Name: "ID", Type: realmc.PropTypeString, IsPrimaryKey: true},
			{Name: "Hash", Type: realmc.PropTypeString},
		},
	}}}, 52011)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	snap, err := Capture(lib, "ez2lazer", p)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if snap.ForkID != "ez2lazer" || snap.DiskVersion != 52011 {
		t.Errorf("snap 头 = %+v", snap)
	}
	if want, _ := filepath.Abs(p); snap.SourcePath != want {
		t.Errorf("SourcePath = %s, 期望 %s", snap.SourcePath, want)
	}
	if len(snap.Classes) != 1 || snap.Classes[0].Name != "BeatmapSet" || snap.Classes[0].PrimaryKey != "ID" {
		t.Fatalf("Classes = %+v", snap.Classes)
	}
	for _, pr := range snap.Classes[0].Properties {
		if pr.Name == "ID" && !pr.PrimaryKey {
			t.Errorf("ID 列应标记为主键: %+v", pr)
		}
	}

	// 采集 → 落盘 → 读回一致（collect「顺手采集」链路）。
	store, err := OpenSnapshotStore(filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveIfAbsent(snap); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("ez2lazer", 52011)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if len(got.Classes) != len(snap.Classes) || len(got.Classes[0].Properties) != 2 {
		t.Errorf("档案往返不符: %+v", got)
	}
}
