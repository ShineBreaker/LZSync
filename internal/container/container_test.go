package container

// 容器生成单元测试：hub DTO + 内存字节仓 → .osz/.osk/.osr → 解包断言
// 文件清单与字节一致（§7-M3-5 验收线；全链「构造库→收集→生成」断言见
// internal/itests/container_export_test.go）。拉起链路属人工项不进自动化。

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"testing"

	"lzsync/internal/hub"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// memSource 内存字节仓（hash → 字节）；缺失哈希返回错误，模拟仓缺实体。
func memSource(files map[string][]byte) BlobSource {
	return func(hash string) (io.ReadCloser, error) {
		b, ok := files[hash]
		if !ok {
			return nil, fmt.Errorf("仓内无 %s", hash)
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

func unzip(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("zip 打开: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("zip 条目 %s 打开: %v", f.Name, err)
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

// seedSet 手造自洽谱面集（两难度 + 音频 + 背景），返回 DTO 与字节仓。
func seedSet() (hub.BeatmapSet, map[string][]byte) {
	files := map[string][]byte{}
	osuNormal := []byte("osu file format v14\n\n[Difficulty]\nHPDrainRate:4\n")
	osuAnother := []byte("osu file format v14\n\n[Difficulty]\nHPDrainRate:6\n")
	audio := []byte("audio-bytes-0123456789")
	bg := []byte("jpeg-bytes-9876543210")

	set := hub.BeatmapSet{Hash: "sethash", Artist: "A", Title: "T", AuthorUsername: "mapper"}
	norm := hub.Beatmap{Hash: sha256Hex(osuNormal), DifficultyName: "Normal"}
	ano := hub.Beatmap{Hash: sha256Hex(osuAnother), DifficultyName: "Another"}
	set.Beatmaps = []hub.Beatmap{norm, ano}
	set.Files = []hub.NamedFile{
		{Hash: norm.Hash, Filename: "Normal.osu"},
		{Hash: ano.Hash, Filename: "Another.osu"},
		{Hash: sha256Hex(audio), Filename: "audio.mp3"},
		{Hash: sha256Hex(bg), Filename: "bg.jpg"},
	}
	for _, b := range [][]byte{osuNormal, osuAnother, audio, bg} {
		files[sha256Hex(b)] = b
	}
	return set, files
}

func TestWriteOSZRoundtrip(t *testing.T) {
	set, files := seedSet()
	var buf bytes.Buffer
	if err := WriteOSZ(&buf, set, memSource(files)); err != nil {
		t.Fatalf("WriteOSZ: %v", err)
	}
	got := unzip(t, buf.Bytes())
	// 文件清单 = Files.Filename 全集（难度 .osu 按难度名、音频背景按原名）。
	if len(got) != len(set.Files) {
		t.Fatalf("zip 条目数 = %d，期望 %d（%v）", len(got), len(set.Files), got)
	}
	for _, f := range set.Files {
		b, ok := got[f.Filename]
		if !ok {
			t.Fatalf("zip 缺条目 %s（清单：%v）", f.Filename, got)
		}
		if !bytes.Equal(b, files[f.Hash]) {
			t.Fatalf("条目 %s 字节与仓不符：got %q want %q", f.Filename, b, files[f.Hash])
		}
	}
	for _, name := range []string{"Normal.osu", "Another.osu", "audio.mp3", "bg.jpg"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("难度 .osu/音频/背景不齐：缺 %s", name)
		}
	}
}

func TestWriteOSZRejectsMissingDifficultyFile(t *testing.T) {
	set, files := seedSet()
	delete(files, set.Beatmaps[1].Hash) // 仓缺 Another 的实体
	var buf bytes.Buffer
	err := WriteOSZ(&buf, set, memSource(files))
	if err == nil || !strings.Contains(err.Error(), "Another") {
		t.Fatalf("仓缺难度实体应明确报错，实际 %v", err)
	}
}

func TestWriteOSZRejectsFilesWithoutDifficultyEntry(t *testing.T) {
	set, files := seedSet()
	// Files 列表剔除难度条目（模拟 hub 行残缺）：难度 Hash 无 NamedFile 可寻。
	set.Files = set.Files[2:] // 只剩 audio.mp3 / bg.jpg
	var buf bytes.Buffer
	err := WriteOSZ(&buf, set, memSource(files))
	if err == nil || !strings.Contains(err.Error(), "拒绝生成坏容器") {
		t.Fatalf("Files 缺难度条目应拒绝，实际 %v", err)
	}
}

func TestWriteOSZRejectsTamperedBlob(t *testing.T) {
	set, files := seedSet()
	h := set.Files[2].Hash // audio.mp3
	files[h] = []byte("tampered") // 字节与哈希不符
	var buf bytes.Buffer
	err := WriteOSZ(&buf, set, memSource(files))
	if err == nil || !strings.Contains(err.Error(), "哈希不符") {
		t.Fatalf("仓实体损坏应拒绝，实际 %v", err)
	}
}

func TestWriteOSZRejectsBadLayout(t *testing.T) {
	set, files := seedSet()
	// 同名不同哈希：非法 zip 布局。
	set.Files = append(set.Files, hub.NamedFile{Hash: sha256Hex([]byte("dup")), Filename: "audio.mp3"})
	files[sha256Hex([]byte("dup"))] = []byte("dup")
	if err := WriteOSZ(&bytes.Buffer{}, set, memSource(files)); err == nil || !strings.Contains(err.Error(), "重复指向不同哈希") {
		t.Fatalf("同名不同哈希应拒绝，实际 %v", err)
	}
	// 路径逃逸条目名。
	set2, _ := seedSet()
	set2.Files = append(set2.Files, hub.NamedFile{Hash: sha256Hex([]byte("esc")), Filename: "../evil.txt"})
	if err := WriteOSZ(&bytes.Buffer{}, set2, memSource(files)); err == nil || !strings.Contains(err.Error(), ".. 段") {
		t.Fatalf("路径逃逸名应拒绝，实际 %v", err)
	}
}

func TestWriteOSKRoundtrip(t *testing.T) {
	ini := []byte("[General]\nName:Test Skin\nVersion:2.6\n")
	png := []byte("png-bytes")
	files := map[string][]byte{
		sha256Hex(ini): ini,
		sha256Hex(png): png,
	}
	skin := hub.Skin{Name: "Test Skin", Creator: "creator", Hash: "skinhash", Files: []hub.NamedFile{
		{Hash: sha256Hex(ini), Filename: "skin.ini"},
		{Hash: sha256Hex(png), Filename: "hit.png"},
	}}
	var buf bytes.Buffer
	if err := WriteOSK(&buf, skin, memSource(files)); err != nil {
		t.Fatalf("WriteOSK: %v", err)
	}
	got := unzip(t, buf.Bytes())
	if len(got) != 2 {
		t.Fatalf("zip 条目数 = %d，期望 2", len(got))
	}
	if !bytes.Equal(got["skin.ini"], ini) || !bytes.Equal(got["hit.png"], png) {
		t.Fatalf("皮肤条目字节与仓不符: %v", got)
	}
	if err := WriteOSK(&bytes.Buffer{}, hub.Skin{Name: "空皮肤"}, memSource(files)); err == nil {
		t.Fatalf("无文件皮肤应报错")
	}
}

func TestWriteOSRRoundtrip(t *testing.T) {
	replay := []byte("replay-frame-bytes-0123456789")
	files := map[string][]byte{sha256Hex(replay): replay}
	score := hub.Score{Hash: sha256Hex(replay), BeatmapHash: "bmhash", Files: []hub.NamedFile{
		{Hash: sha256Hex(replay), Filename: "replay.osr"},
	}}
	var buf bytes.Buffer
	if err := WriteOSR(&buf, score, memSource(files)); err != nil {
		t.Fatalf("WriteOSR: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), replay) {
		t.Fatalf(".osr 字节与仓不符：got %q want %q", buf.Bytes(), replay)
	}
	// 大小写后缀亦可寻（NamedFile 的 Filename 无规范大小写约束）。
	score2 := hub.Score{Files: []hub.NamedFile{{Hash: sha256Hex(replay), Filename: "Replay.OSR"}}}
	if err := WriteOSR(&bytes.Buffer{}, score2, memSource(files)); err != nil {
		t.Fatalf("大写 .OSR 后缀应可寻: %v", err)
	}
	if err := WriteOSR(&bytes.Buffer{}, hub.Score{}, memSource(files)); err == nil {
		t.Fatalf("无回放文件应报错")
	}
}

func TestOpenerCommand(t *testing.T) {
	for goos, want := range map[string][]string{
		"linux": {"xdg-open"}, "darwin": {"open"},
		"windows": {"rundll32", "url.dll,FileProtocolHandler"},
	} {
		got := OpenerCommand(goos)
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Fatalf("OpenerCommand(%s) = %v，期望 %v", goos, got, want)
		}
	}
}
