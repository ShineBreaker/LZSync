// 容器降级路径全链验收（§7-M3-5 路线④）：构造库 → 真实收集（hub payload
// + 收仓）→ .osz/.osk/.osr 生成 → 解包断言文件清单/字节与仓一致。拉起
// 客户端导入属人工项，不进自动化（产品服务面 ExportContainers 的分支
// 覆盖见 internal/appsvc/export_test.go）。

package itests

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"testing"

	"lzsync/internal/container"
	"lzsync/internal/fixture"
	"lzsync/internal/hub"
)

func TestContainerExportFullChain(t *testing.T) {
	mustLib(t)
	e := newEnv(t)
	ctx := context.Background()

	files := map[string][]byte{}
	set, err := fixture.MaterializeSet(6006, "Container Artist", "Container Title", "mapper",
		[]fixture.DiffMaterial{
			{Name: "Normal", OsuBytes: []byte("container osz normal osu bytes")},
			{Name: "Extra", OsuBytes: []byte("container osz extra osu bytes")},
		}, []byte("container audio bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeSet: %v", err)
	}
	// 背景资产：MaterializeSet 只种音频，背景按 Files.Filename 布局补一条
	//（谱面集资产面：难度 .osu + 音频 + 背景，§7-M3-5 验收口径）。
	bg := []byte("container bg jpg bytes")
	bgHash := sha256Hex(string(bg))
	files[bgHash] = bg
	set.Files = append(set.Files, hub.NamedFile{Hash: bgHash, Filename: "bg.jpg"})

	skin, err := fixture.MaterializeSkin("容器皮肤", "cskin", []fixture.SkinFileMaterial{
		{Filename: "skin.ini", Bytes: []byte("[General]\nName:容器皮肤\n")},
		{Filename: "cursor.png", Bytes: []byte("container skin cursor png")},
	}, files)
	if err != nil {
		t.Fatalf("MaterializeSkin: %v", err)
	}
	score, err := fixture.MaterializeScore(set.Beatmaps[0], "lazer 2026.1001.0",
		[]byte("container replay bytes"), files)
	if err != nil {
		t.Fatalf("MaterializeScore: %v", err)
	}
	score.TotalScore = 1000000

	ref := buildLib(t, fixture.ForkLazer, fixture.VersionLazer, fixture.Seed{
		BeatmapSets: []hub.BeatmapSet{set},
		Skins:       []fixture.SeedSkin{skin},
		Scores:      []fixture.SeedScore{score},
		Files:       files,
	})
	rep := e.collectInto(ref)
	for _, kind := range []hub.EntityKind{hub.KindBeatmapSet, hub.KindSkin, hub.KindScore} {
		if rep.Entities[kind] == 0 {
			t.Fatalf("收集 %s 实体数 = 0（夹具构造异常）", kind)
		}
	}

	// 仓内实体打开器（收仓后 Store 即 hub payload 引用的唯一权威副本）。
	openBlob := func(hash string) (io.ReadCloser, error) {
		return os.Open(e.engine.Store.Path(hash))
	}
	unzip := func(raw []byte) map[string][]byte {
		t.Helper()
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("zip 打开: %v", err)
		}
		out := map[string][]byte{}
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
			out[f.Name] = b
		}
		return out
	}
	// assertEntries 断言容器条目与仓内原始字节逐条一致（解包 → 哈希寻址）。
	assertEntries := func(label string, got map[string][]byte, want []hub.NamedFile) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s 条目数 = %d（%v），期望 %d", label, len(got), got, len(want))
		}
		for _, f := range want {
			b, ok := got[f.Filename]
			if !ok {
				t.Fatalf("%s 缺条目 %s（清单：%v）", label, f.Filename, got)
			}
			if !bytes.Equal(b, files[f.Hash]) {
				t.Fatalf("%s 条目 %s 字节与仓不符：got %q want %q", label, f.Filename, b, files[f.Hash])
			}
		}
	}

	// ---- .osz：hub 版本链尾 payload + 仓 → 解包断言（难度 .osu、音频、背景齐）----
	var payload string
	err = e.hub.DB().QueryRow(
		`SELECT payload FROM beatmap_set WHERE content_hash=? AND superseded_by IS NULL`, set.Hash).
		Scan(&payload)
	if err == sql.ErrNoRows {
		t.Fatalf("hub 缺谱面集 %s（收集未入仓）", set.Hash)
	}
	if err != nil {
		t.Fatalf("读链尾 payload: %v", err)
	}
	var gotSet hub.BeatmapSet
	if err := json.Unmarshal([]byte(payload), &gotSet); err != nil {
		t.Fatalf("解码谱面集 payload: %v", err)
	}
	var osz bytes.Buffer
	if err := container.WriteOSZ(&osz, gotSet, openBlob); err != nil {
		t.Fatalf("WriteOSZ: %v", err)
	}
	gotOSZ := unzip(osz.Bytes())
	assertEntries(".osz", gotOSZ, gotSet.Files)
	for _, name := range []string{"Normal.osu", "Extra.osu", "audio.mp3", "bg.jpg"} {
		if _, ok := gotOSZ[name]; !ok {
			t.Fatalf(".osz 清单不齐：缺 %s（%v）", name, gotOSZ)
		}
	}

	// ---- .osk：皮肤公共 Load 面 + 仓 ----
	gotSkin, _, ok, err := e.hub.LoadSkin(ctx, skin.Hash)
	if err != nil || !ok {
		t.Fatalf("LoadSkin(%s) = (ok=%v, %v)", skin.Hash, ok, err)
	}
	var osk bytes.Buffer
	if err := container.WriteOSK(&osk, gotSkin, openBlob); err != nil {
		t.Fatalf("WriteOSK: %v", err)
	}
	assertEntries(".osk", unzip(osk.Bytes()), gotSkin.Files)

	// ---- .osr：成绩公共 Load 面 + 仓（单文件字节一致）----
	gotScore, _, found, err := e.hub.LoadScore(ctx, score.BeatmapHash, score.Hash)
	if err != nil || !found {
		t.Fatalf("LoadScore = (found=%v, %v)", found, err)
	}
	var osr bytes.Buffer
	if err := container.WriteOSR(&osr, gotScore, openBlob); err != nil {
		t.Fatalf("WriteOSR: %v", err)
	}
	replay := files[score.ReplayHashes[0]]
	if !bytes.Equal(osr.Bytes(), replay) {
		t.Fatalf(".osr 字节与仓不符：got %q want %q", osr.Bytes(), replay)
	}
}
