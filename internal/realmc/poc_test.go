package realmc

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"unsafe"
)

// mustLoad 加载 realm-core；库文件缺失（分发环境未带 sidecar，P-2 场景）
// 显式 SKIP，不得静默绿（design §8.1 补充门）。
func mustLoad(t *testing.T) *Library {
	t.Helper()
	lib, err := Load()
	if err != nil {
		t.Skipf("realm-core 库不可用，跳过 PoC: %v", err)
	}
	return lib
}

// TestPoCABILayout 编译期镜像结构体的 C ABI 尺寸与字段偏移断言。
// purego 不校验结构体布局（func.go 文档：padding 是调用者责任），布局错位
// 会让 C 端读到垃圾指针（realm_schema_new 实测教训）或按值传参写坏栈——
// 这道断言把布局偏差挡在最早的位置。
func TestPoCABILayout(t *testing.T) {
	if got := unsafe.Sizeof(realmValue{}); got != 24 {
		t.Errorf("sizeof(realm_value_t) = %d, 期望 24（realm.h:201-218）", got)
	}
	var v realmValue
	if o := unsafe.Offsetof(v.u); o != 0 {
		t.Errorf("realm_value_t.values 偏移 = %d, 期望 0", o)
	}
	if o := unsafe.Offsetof(v.typ); o != 16 {
		t.Errorf("realm_value_t.type 偏移 = %d, 期望 16", o)
	}

	if got := unsafe.Sizeof(realmClassInfo{}); got != 40 {
		t.Errorf("sizeof(realm_class_info_t) = %d, 期望 40（realm.h:296-303）", got)
	}
	var ci realmClassInfo
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"name", unsafe.Offsetof(ci.name), 0},
		{"primary_key", unsafe.Offsetof(ci.primaryKey), 8},
		{"num_properties", unsafe.Offsetof(ci.numProperties), 16},
		{"num_computed_properties", unsafe.Offsetof(ci.numComputedProperties), 24},
		{"key", unsafe.Offsetof(ci.key), 32},
		{"flags", unsafe.Offsetof(ci.flags), 36},
	} {
		if tc.got != tc.want {
			t.Errorf("realm_class_info_t.%s 偏移 = %d, 期望 %d", tc.name, tc.got, tc.want)
		}
	}

	if got := unsafe.Sizeof(realmPropertyInfo{}); got != 56 {
		t.Errorf("sizeof(realm_property_info_t) = %d, 期望 56（realm.h:284-294）", got)
	}
	var pi realmPropertyInfo
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"name", unsafe.Offsetof(pi.name), 0},
		{"public_name", unsafe.Offsetof(pi.publicName), 8},
		{"type", unsafe.Offsetof(pi.typ), 16},
		{"collection_type", unsafe.Offsetof(pi.collectionType), 20},
		{"link_target", unsafe.Offsetof(pi.linkTarget), 24},
		{"link_origin_property_name", unsafe.Offsetof(pi.linkOriginPropName), 32},
		{"key", unsafe.Offsetof(pi.key), 40},
		{"flags", unsafe.Offsetof(pi.flags), 48},
	} {
		if tc.got != tc.want {
			t.Errorf("realm_property_info_t.%s 偏移 = %d, 期望 %d", tc.name, tc.got, tc.want)
		}
	}

	if got := unsafe.Sizeof(realmError{}); got != 32 {
		t.Errorf("sizeof(realm_error_t) = %d, 期望 32（realm.h:235-243）", got)
	}
	var e realmError
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"error", unsafe.Offsetof(e.errNo), 0},
		{"categories", unsafe.Offsetof(e.categories), 4},
		{"message", unsafe.Offsetof(e.message), 8},
		{"user_code_error", unsafe.Offsetof(e.userCodeErr), 16},
		{"path", unsafe.Offsetof(e.path), 24},
	} {
		if tc.got != tc.want {
			t.Errorf("realm_error_t.%s 偏移 = %d, 期望 %d", tc.name, tc.got, tc.want)
		}
	}
}

func readFileHead(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开 %s: %v", path, err)
	}
	defer f.Close()
	head := make([]byte, 64)
	if _, err := io.ReadFull(f, head); err != nil {
		t.Fatalf("读 %s 前 64 字节: %v", path, err)
	}
	return head
}

// TestPoCReadOnly 是 design §2 PoC 第一道验收线：只读链路。
// purego 打开本机真实库的 TempDir 副本（真实库绝不直接打开——只读打开
// 也会在库旁创建 .lock/.management 伴生文件）→ 枚举全部 class → 读一行
// BeatmapSetInfo 属性 → 安全关闭 → 断言副本文件头 64 字节不变。
func TestPoCReadOnly(t *testing.T) {
	src := os.Getenv("LZSYNC_TEST_REALM")
	if src == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("UserHomeDir: %v", err)
		}
		src = filepath.Join(home, ".local", "share", "osu", "client.realm")
	}
	if _, err := os.Stat(src); err != nil {
		t.Skipf("真实库不存在（%s），跳过只读链路: %v", src, err)
	}

	lib := mustLoad(t)
	t.Logf("librealm 版本: %s", lib.Version())

	// 拷贝到 TempDir 副本，只读打开的伴生文件落在副本旁，真实库零接触。
	dir := t.TempDir()
	clone := filepath.Join(dir, "client.realm")
	// mimosa-ignore #nosec G305 -- 测试辅助：把本机真实库拷进 t.TempDir 副本再开库，路径由测试自身构造，无外部输入（已裁定误报）
	if err := copyFile(src, clone); err != nil {
		t.Fatalf("拷贝真实库到 %s: %v", clone, err)
	}
	headBefore := readFileHead(t, clone)

	r, err := lib.OpenDynamicReadOnly(clone)
	if err != nil {
		t.Fatalf("动态只读打开副本失败: %v", err)
	}
	classes, err := func() ([]ClassInfo, error) {
		if err := r.BeginRead(); err != nil {
			return nil, err
		}
		defer r.Close()
		version := r.SchemaVersion()
		t.Logf("磁盘 schema 版本: %d", version)
		if version == 0 {
			t.Errorf("磁盘 schema 版本为 0，动态打开应读回真实版本")
		}
		cs, err := r.Classes()
		if err != nil {
			return nil, err
		}
		t.Logf("class 数量: %d", len(cs))
		for i, c := range cs {
			t.Logf("class[%d] = %q (pk=%q)", i, c.Name, c.PrimaryKey)
		}

		// 磁盘 object_type 是 MapTo 名（BeatmapSet/Beatmap/Score），
		// 与 EZRealmSync KnownProperties 的键名同款（ezrealmsync-design.md §2.3）。
		setKey, err := r.FindClass("BeatmapSet")
		if err != nil {
			return cs, err
		}
		n, err := r.ObjectCount(setKey)
		if err != nil {
			return cs, err
		}
		t.Logf("BeatmapSet 行数: %d", n)
		if n == 0 {
			return cs, fmt.Errorf("真实库 BeatmapSet 无行")
		}
		obj, err := r.FirstObject(setKey)
		if err != nil {
			return cs, err
		}
		defer r.ReleaseObject(obj)
		// 属性按存在性寻址（EZRealmSync 的 HasProperty 纪律）：枚举实际列，
		// 目标列缺失则失败——真实库必有 OnlineID/Hash。
		propByKey := map[string]int64{}
		props, err := r.Properties(setKey)
		if err != nil {
			return cs, err
		}
		for _, p := range props {
			t.Logf("BeatmapSet.%s (type=%d)", p.Name, p.Type)
			propByKey[p.Name] = p.Key
		}
		if idKey, ok := propByKey["OnlineID"]; ok {
			id, err := r.GetInt64(obj, idKey)
			if err != nil {
				return cs, err
			}
			t.Logf("首行 BeatmapSet.OnlineID = %d", id)
		} else {
			return cs, fmt.Errorf("BeatmapSet 缺 OnlineID 列")
		}
		if hashKey, ok := propByKey["Hash"]; ok {
			hash, err := r.GetString(obj, hashKey)
			if err != nil {
				return cs, err
			}
			t.Logf("首行 BeatmapSet.Hash = %q", hash)
			if hash == "" {
				t.Errorf("BeatmapSet.Hash 为空串")
			}
		} else {
			return cs, fmt.Errorf("BeatmapSet 缺 Hash 列")
		}
		return cs, nil
	}()
	if err != nil {
		t.Fatalf("只读链路失败: %v", err)
	}

	// class 枚举断言
	if len(classes) == 0 {
		t.Fatal("class 枚举结果为空")
	}
	names := make(map[string]bool, len(classes))
	for _, c := range classes {
		names[c.Name] = true
	}
	for _, want := range []string{"BeatmapSet", "Beatmap", "Score", "BeatmapCollection", "Skin"} {
		if !names[want] {
			t.Errorf("class 枚举缺少 %s（共 %d 个 class）", want, len(classes))
		}
	}

	// 文件头不变断言（副本）
	headAfter := readFileHead(t, clone)
	if !bytes.Equal(headBefore, headAfter) {
		t.Errorf("只读打开后副本文件头 64 字节发生变化:\n前: %x\n后: %x", headBefore, headAfter)
	}
	// 真实库未被触碰
	if _, err := os.Stat(src + ".lzsync-probe"); err == nil {
		t.Error("真实库旁出现了探针文件，疑似直接打开真实库")
	}
}

// copyFile 测试辅助拷贝：调用者均为本文件内测试（路径为 t.TempDir 或本机固定探测路径），无外部输入。
// mimosa-ignore #nosec G304,G305 -- 见上，测试专用（已裁定误报）
func copyFile(src, dst string) error {
	in, err := os.Open(src) // mimosa-ignore #nosec G304 -- 同上，测试专用
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// TestPoCCreateWithSchema 是 design §2 PoC 第二道验收线：显式 schema 建库链路。
// 用 C API schema 构造面（realm_config_t.schema + realm_schema_t 系列，realm.h:641/1121）
// 在 TempDir 建空库 → 写事务写入一行 → commit → 动态重开（不带 schema，产品路径语义）
// → 读回断言字段值 → 关闭。该能力是 §8.2 测试体系的单点依赖，失败即触发
// 手工种子库 fixture 回落（§8.3-T3）。
func TestPoCCreateWithSchema(t *testing.T) {
	lib := mustLoad(t)

	dir := t.TempDir()
	db := filepath.Join(dir, "poc.realm")

	schema := &Schema{Classes: []ClassDef{{
		Name: "PoCItem",
		Properties: []PropertyDef{
			{Name: "Name", Type: PropTypeString},
			{Name: "Count", Type: PropTypeInt},
		},
	}}}
	const schemaVersion = 1

	// 阶段一：显式 schema 建库 + 写事务写入一行 + commit
	r, err := lib.OpenWithSchema(db, schema, schemaVersion)
	if err != nil {
		t.Fatalf("显式 schema 建库失败: %v", err)
	}
	if err := r.BeginWrite(); err != nil {
		r.Close()
		t.Fatalf("begin_write 失败: %v", err)
	}
	itemKey, err := r.FindClass("PoCItem")
	if err != nil {
		r.Rollback()
		r.Close()
		t.Fatalf("新建库中找不到 PoCItem: %v", err)
	}
	obj, err := r.CreateObject(itemKey)
	if err != nil {
		r.Rollback()
		r.Close()
		t.Fatalf("realm_object_create 失败: %v", err)
	}
	defer r.ReleaseObject(obj)
	nameKey, err := r.FindProperty(itemKey, "Name")
	if err != nil {
		r.Rollback()
		r.Close()
		t.Fatalf("找 Name 属性: %v", err)
	}
	countKey, err := r.FindProperty(itemKey, "Count")
	if err != nil {
		r.Rollback()
		r.Close()
		t.Fatalf("找 Count 属性: %v", err)
	}
	if err := r.SetString(obj, nameKey, "lzsync-poc"); err != nil {
		r.Rollback()
		r.Close()
		t.Fatalf("写 Name 失败: %v", err)
	}
	if err := r.SetInt64(obj, countKey, 42); err != nil {
		r.Rollback()
		r.Close()
		t.Fatalf("写 Count 失败: %v", err)
	}
	if err := r.Commit(); err != nil {
		t.Fatalf("commit 失败: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("建库会话关闭失败: %v", err)
	}

	if fi, err := os.Stat(db); err != nil {
		t.Fatalf("建库后 %s 不存在: %v", db, err)
	} else {
		t.Logf("建库产物 %s 大小 %d 字节", db, fi.Size())
	}

	// 阶段二：动态重开（不带 schema，S3 语义：钉死磁盘版本）读回断言
	r2, err := lib.OpenDynamicReadOnly(db)
	if err != nil {
		t.Fatalf("动态重开建库产物失败: %v", err)
	}
	if err := r2.BeginRead(); err != nil {
		r2.Close()
		t.Fatalf("重开 begin_read 失败: %v", err)
	}
	if got := r2.SchemaVersion(); got != schemaVersion {
		r2.Close()
		t.Fatalf("动态重开读回的磁盘 schema 版本 = %d, 期望 %d", got, schemaVersion)
	}
	key, err := r2.FindClass("PoCItem")
	if err != nil {
		r2.Close()
		t.Fatalf("重开后找不到 PoCItem: %v", err)
	}
	n, err := r2.ObjectCount(key)
	if err != nil {
		r2.Close()
		t.Fatalf("重开统计行数失败: %v", err)
	}
	if n != 1 {
		r2.Close()
		t.Fatalf("重开后 PoCItem 行数 = %d, 期望 1", n)
	}
	obj2, err := r2.FirstObject(key)
	if err != nil {
		r2.Close()
		t.Fatalf("重开读首行失败: %v", err)
	}
	defer r2.ReleaseObject(obj2)
	gotName, err := r2.GetString(obj2, nameKey)
	if err != nil {
		r2.Close()
		t.Fatalf("重开读 Name 失败: %v", err)
	}
	gotCount, err := r2.GetInt64(obj2, countKey)
	if err != nil {
		r2.Close()
		t.Fatalf("重开读 Count 失败: %v", err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("重开会话关闭失败: %v", err)
	}

	if gotName != "lzsync-poc" {
		t.Errorf("读回 Name = %q, 期望 %q", gotName, "lzsync-poc")
	}
	if gotCount != 42 {
		t.Errorf("读回 Count = %d, 期望 42", gotCount)
	}
}
