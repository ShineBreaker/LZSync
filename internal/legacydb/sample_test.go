package legacydb

import (
	"crypto/md5" // mimosa-ignore -- osu! 谱面格式规定 MD5 内容哈希（与 osu!.db 列值逐字节一致），非安全用途，已裁定误报
	"os"
	"path/filepath"
	"testing"
)

// stableSampleRoot 是真机 stable 安装目录（本机实测样本），可用
// LZSYNC_STABLE_ROOT 覆盖；不可用时跳过（CI 无样本环境）。
func stableSampleRoot() string {
	if v := os.Getenv("LZSYNC_STABLE_ROOT"); v != "" {
		return v
	}
	return "/home/brokenshine/Games/osu!"
}

// TestParseRealOsuDB 对真机 osu!.db 全量解析：头部字段、逐条目录存在性、
// 谱面 MD5 与 .osu 文件逐字节一致（跨端对齐锚点，stable-legacy §4）、
// 解析终点与文件尾精确重合（Trailing=0）。
func TestParseRealOsuDB(t *testing.T) {
	root := stableSampleRoot()
	data, err := os.ReadFile(filepath.Join(root, "osu!.db"))
	if err != nil {
		t.Skipf("真机样本不可用: %v", err)
	}
	db, err := ParseBeatmapDB(data)
	if err != nil {
		t.Fatalf("解析真机 osu!.db: %v", err)
	}
	if db.Version < 20200000 {
		t.Fatalf("版本 %d 异常", db.Version)
	}
	if len(db.Beatmaps) == 0 {
		t.Fatal("0 条难度")
	}
	if db.Trailing != 0 {
		t.Fatalf("Trailing = %d（20260924 已知格式应精确收官）", db.Trailing)
	}
	dirMiss, md5Checked, md5Bad := 0, 0, 0
	for _, e := range db.Beatmaps {
		osu := filepath.Join(root, "Songs", e.FolderName, e.OsuFilename)
		content, err := os.ReadFile(osu)
		if err != nil {
			dirMiss++
			continue
		}
		md5Checked++
		if sum := md5.Sum(content); sum != [16]byte(decodeHex16(e.MD5Hash)) {
			md5Bad++
		}
	}
	if dirMiss != 0 {
		t.Fatalf("%d 条难度指向不存在的 Songs/ 文件", dirMiss)
	}
	if md5Checked == 0 {
		t.Fatal("无一条难度完成 MD5 校验")
	}
	if md5Bad != 0 {
		t.Fatalf("%d/%d 条难度 MD5 与 .osu 文件不一致", md5Bad, md5Checked)
	}
	t.Logf("version=%d difficulties=%d md5Verified=%d", db.Version, len(db.Beatmaps), md5Checked)
}

// TestParseRealCollectionDB 对真机 collection.db 解析（空收藏亦为合法态）。
func TestParseRealCollectionDB(t *testing.T) {
	root := stableSampleRoot()
	data, err := os.ReadFile(filepath.Join(root, "collection.db"))
	if err != nil {
		t.Skipf("真机样本不可用: %v", err)
	}
	db, err := ParseCollectionDB(data)
	if err != nil {
		t.Fatalf("解析真机 collection.db: %v", err)
	}
	if db.Version < 20200000 {
		t.Fatalf("版本 %d 异常", db.Version)
	}
	t.Logf("version=%d collections=%d", db.Version, len(db.Collections))
}

// decodeHex16 把 32 字符 hex 串解成 16 字节（与 md5.Sum 比较用；非十六
// 进制返回全 ff 触发不等比较，不报错——格式异常属于数据问题，由计数呈现）。
func decodeHex16(s string) [16]byte {
	var out [16]byte
	if len(s) != 32 {
		for i := range out {
			out[i] = 0xff
		}
		return out
	}
	for i := 0; i < 16; i++ {
		hi, ok1 := hexVal(s[i*2])
		lo, ok2 := hexVal(s[i*2+1])
		if !ok1 || !ok2 {
			out[i] = 0xff
			continue
		}
		out[i] = hi<<4 | lo
	}
	return out
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
