package realmc

// 动态读写面单测（design §3.1 / §8.2）：全部用 TestPoCCreateWithSchema 同款
// 「显式 schema 建小库」方式自包含构造，不依赖任何外部 fixture。mini schema
// 按 docs/research/lazer-baseline.md §2 的 lazer 持久列子集镜像（含 list /
// embedded / 对象链接的真实形态：Beatmaps 前向对象列表、Files embedded 列表、
// BeatmapMD5Hashes string 列表、Pauses int 列表、可空链接列）。

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func miniSchema() *Schema {
	return &Schema{Classes: []ClassDef{
		{Name: "BeatmapSet", PrimaryKey: "ID", Properties: []PropertyDef{
			{Name: "ID", Type: PropTypeUUID, IsPrimaryKey: true},
			{Name: "OnlineID", Type: PropTypeInt},
			{Name: "DateAdded", Type: PropTypeTimestamp},
			{Name: "Hash", Type: PropTypeString},
			{Name: "DeletePending", Type: PropTypeBool},
			{Name: "Protected", Type: PropTypeBool},
			{Name: "Beatmaps", Type: PropTypeObject, Collection: CollectionList, LinkTarget: "Beatmap"},
			{Name: "Files", Type: PropTypeObject, Collection: CollectionList, LinkTarget: "RealmNamedFileUsage"},
		}},
		{Name: "Beatmap", PrimaryKey: "ID", Properties: []PropertyDef{
			{Name: "ID", Type: PropTypeUUID, IsPrimaryKey: true},
			{Name: "DifficultyName", Type: PropTypeString},
			{Name: "Hash", Type: PropTypeString},
			{Name: "StarRating", Type: PropTypeDouble},
			{Name: "Hidden", Type: PropTypeBool},
			{Name: "BeatmapSet", Type: PropTypeObject, LinkTarget: "BeatmapSet", IsNullable: true},
			{Name: "Difficulty", Type: PropTypeObject, LinkTarget: "BeatmapDifficulty"},
			{Name: "UserSettings", Type: PropTypeObject, LinkTarget: "BeatmapUserSettings"},
		}},
		{Name: "BeatmapDifficulty", Embedded: true, Properties: []PropertyDef{
			{Name: "CircleSize", Type: PropTypeFloat},
			{Name: "SliderMultiplier", Type: PropTypeDouble},
		}},
		{Name: "BeatmapUserSettings", Embedded: true, Properties: []PropertyDef{
			{Name: "Offset", Type: PropTypeDouble, IsNullable: true},
		}},
		{Name: "RealmNamedFileUsage", Embedded: true, Properties: []PropertyDef{
			{Name: "File", Type: PropTypeObject, LinkTarget: "File"},
			{Name: "Filename", Type: PropTypeString},
		}},
		{Name: "File", PrimaryKey: "Hash", Properties: []PropertyDef{
			{Name: "Hash", Type: PropTypeString, IsPrimaryKey: true},
			{Name: "Size", Type: PropTypeInt},
		}},
		{Name: "RealmUser", Embedded: true, Properties: []PropertyDef{
			{Name: "Username", Type: PropTypeString},
			{Name: "OnlineID", Type: PropTypeInt},
		}},
		{Name: "Score", PrimaryKey: "ID", Properties: []PropertyDef{
			{Name: "ID", Type: PropTypeUUID, IsPrimaryKey: true},
			{Name: "TotalScore", Type: PropTypeInt},
			{Name: "Accuracy", Type: PropTypeDouble},
			{Name: "PP", Type: PropTypeDouble, IsNullable: true},
			{Name: "Date", Type: PropTypeTimestamp},
			{Name: "BeatmapHash", Type: PropTypeString},
			{Name: "BeatmapInfo", Type: PropTypeObject, LinkTarget: "Beatmap", IsNullable: true},
			{Name: "User", Type: PropTypeObject, LinkTarget: "RealmUser"},
			{Name: "Pauses", Type: PropTypeInt, Collection: CollectionList},
			{Name: "Files", Type: PropTypeObject, Collection: CollectionList, LinkTarget: "RealmNamedFileUsage"},
			{Name: "DeletePending", Type: PropTypeBool},
			{Name: "Payload", Type: PropTypeBinary},
		}},
		{Name: "BeatmapCollection", PrimaryKey: "ID", Properties: []PropertyDef{
			{Name: "ID", Type: PropTypeUUID, IsPrimaryKey: true},
			{Name: "Name", Type: PropTypeString},
			{Name: "BeatmapMD5Hashes", Type: PropTypeString, Collection: CollectionList},
			{Name: "LastModified", Type: PropTypeTimestamp},
		}},
	}}
}

// miniDB 是测试小库的会话脚手架：建库、开写事务，并预算好 class/property key。
type miniDB struct {
	t    *testing.T
	lib  *Library
	path string
	r    *Realm
	key  map[string]uint32
	prop map[string]int64
}

const miniSchemaVersion = 7

func openMiniWrite(t *testing.T) *miniDB {
	t.Helper()
	lib := mustLoad(t)
	path := filepath.Join(t.TempDir(), "mini.realm")
	r, err := lib.OpenWithSchema(path, miniSchema(), miniSchemaVersion)
	if err != nil {
		t.Fatalf("显式 schema 建库失败: %v", err)
	}
	if err := r.BeginWrite(); err != nil {
		t.Fatalf("begin_write 失败: %v", err)
	}
	m := &miniDB{t: t, lib: lib, path: path, r: r, key: map[string]uint32{}, prop: map[string]int64{}}
	classes, err := r.Classes()
	if err != nil {
		t.Fatalf("枚举 class 失败: %v", err)
	}
	for _, c := range classes {
		m.key[c.Name] = c.Key
		props, err := r.Properties(c.Key)
		if err != nil {
			t.Fatalf("枚举 %s 属性失败: %v", c.Name, err)
		}
		for _, p := range props {
			m.prop[c.Name+"."+p.Name] = p.Key
		}
	}
	return m
}

func (m *miniDB) classKeyOf(name string) uint32 {
	m.t.Helper()
	k, ok := m.key[name]
	if !ok {
		m.t.Fatalf("mini schema 缺 class %s", name)
	}
	return k
}

func (m *miniDB) propKeyOf(class, name string) int64 {
	m.t.Helper()
	k, ok := m.prop[class+"."+name]
	if !ok {
		m.t.Fatalf("mini schema 缺属性 %s.%s", class, name)
	}
	return k
}

func (m *miniDB) commitAndClose() {
	m.t.Helper()
	if err := m.r.Commit(); err != nil {
		m.t.Fatalf("commit 失败: %v", err)
	}
	if err := m.r.Close(); err != nil {
		m.t.Fatalf("close 失败: %v", err)
	}
}

func (m *miniDB) rollbackAndClose() {
	m.t.Helper()
	if err := m.r.Rollback(); err != nil {
		m.t.Fatalf("rollback 失败: %v", err)
	}
	if err := m.r.Close(); err != nil {
		m.t.Fatalf("close 失败: %v", err)
	}
}

func mkGuid(n byte) [16]byte {
	var g [16]byte
	for i := range g {
		g[i] = n
	}
	return g
}

// TestABIQueryArgLayout 是 PoC ABI 纪律（design §2 三解法之三）对新结构体
// realmQueryArg 的延伸：Sizeof/Offsetof 与 realm.h:219-223 逐项对齐。
func TestABIQueryArgLayout(t *testing.T) {
	if got := unsafe.Sizeof(realmQueryArg{}); got != 24 {
		t.Errorf("sizeof(realm_query_arg_t) = %d, 期望 24（realm.h:219-223）", got)
	}
	var qa realmQueryArg
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"nb_args", unsafe.Offsetof(qa.nbArgs), 0},
		{"is_list", unsafe.Offsetof(qa.isList), 8},
		{"arg", unsafe.Offsetof(qa.arg), 16},
	} {
		if tc.got != tc.want {
			t.Errorf("realm_query_arg_t.%s 偏移 = %d, 期望 %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestDynamicReadWriteSession 验证动态读写会话（S3/S5 语义）：动态 RW 打开
// 既有库（SchemaModeAutomatic + 不传 schema，contract.go 实证取值）→ 写入 →
// 磁盘 schema 版本不变；动态只读重开读回新值；不存在的路径直接报错。
func TestDynamicReadWriteSession(t *testing.T) {
	m := openMiniWrite(t)
	guid := mkGuid(1)
	col, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapCollection"), guid)
	if err != nil {
		t.Fatalf("建收藏夹行失败: %v", err)
	}
	defer m.r.ReleaseObject(col)
	if err := m.r.SetString(col, m.propKeyOf("BeatmapCollection", "Name"), "v1"); err != nil {
		t.Fatalf("写 Name 失败: %v", err)
	}
	dbPath := m.path
	m.commitAndClose()

	// 动态读写重开：SchemaMode 实证点。
	rw, err := m.lib.OpenDynamicReadWrite(dbPath)
	if err != nil {
		t.Fatalf("动态读写打开失败: %v", err)
	}
	if got := rw.SchemaVersion(); got != miniSchemaVersion {
		t.Fatalf("动态读写打开后磁盘 schema 版本 = %d, 期望 %d（S3 钉死）", got, miniSchemaVersion)
	}
	if err := rw.BeginWrite(); err != nil {
		t.Fatalf("动态会话 begin_write 失败: %v", err)
	}
	colKey := m.classKeyOf("BeatmapCollection")
	obj, err := rw.FirstObject(colKey)
	if err != nil {
		t.Fatalf("动态会话取首行失败: %v", err)
	}
	if err := rw.SetString(obj, m.propKeyOf("BeatmapCollection", "Name"), "v2"); err != nil {
		t.Fatalf("动态会话写 Name 失败: %v", err)
	}
	rw.ReleaseObject(obj)
	if err := rw.Commit(); err != nil {
		t.Fatalf("动态会话 commit 失败: %v", err)
	}
	if got := rw.SchemaVersion(); got != miniSchemaVersion {
		t.Fatalf("写入提交后磁盘 schema 版本 = %d, 期望 %d（动态会话绝不迁移）", got, miniSchemaVersion)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("动态读写会话关闭失败: %v", err)
	}

	// 动态只读重开读回。
	ro, err := m.lib.OpenDynamicReadOnly(dbPath)
	if err != nil {
		t.Fatalf("动态只读重开失败: %v", err)
	}
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("begin_read 失败: %v", err)
	}
	obj2, err := ro.FirstObject(colKey)
	if err != nil {
		t.Fatalf("重开取首行失败: %v", err)
	}
	got, err := ro.GetString(obj2, m.propKeyOf("BeatmapCollection", "Name"))
	ro.ReleaseObject(obj2)
	if err != nil {
		t.Fatalf("重开读 Name 失败: %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("只读会话关闭失败: %v", err)
	}
	if got != "v2" {
		t.Errorf("动态读写写入读回 = %q, 期望 %q", got, "v2")
	}

	// 不存在的路径：动态打开不建库，直接报错。
	if _, err := m.lib.OpenDynamicReadWrite(filepath.Join(t.TempDir(), "absent.realm")); err == nil {
		t.Error("对不存在的路径动态读写打开应报错（动态打开不携带 schema，不得造空库）")
	}
}

// TestScalarRoundTrip 覆盖全部属性类型的动态读写：
// bool/int64/float32/float64/string/timestamp/guid/binary，外加
// IsNull/SetNull 的可空纪律与 S4 软删列写入。
func TestScalarRoundTrip(t *testing.T) {
	m := openMiniWrite(t)
	defer m.rollbackAndClose()

	// BeatmapSet：guid 主键 + int64 + timestamp + string + bool（软删列）。
	setGuid := mkGuid(7)
	set, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapSet"), setGuid)
	if err != nil {
		t.Fatalf("Guid 主键建行失败: %v", err)
	}
	defer m.r.ReleaseObject(set)
	pk := m.propKeyOf
	if err := m.r.SetInt64(set, pk("BeatmapSet", "OnlineID"), 123456); err != nil {
		t.Fatalf("写 OnlineID: %v", err)
	}
	ts := time.Unix(1774286400, 987654321) // 2026-03 前后的确定时刻
	if err := m.r.SetTimestamp(set, pk("BeatmapSet", "DateAdded"), ts); err != nil {
		t.Fatalf("写 DateAdded: %v", err)
	}
	if err := m.r.SetString(set, pk("BeatmapSet", "Hash"), "set-hash-1"); err != nil {
		t.Fatalf("写 Hash: %v", err)
	}
	if err := m.r.SetBool(set, pk("BeatmapSet", "DeletePending"), true); err != nil { // S4 软删写入
		t.Fatalf("写 DeletePending: %v", err)
	}
	if err := m.r.SetBool(set, pk("BeatmapSet", "Protected"), false); err != nil {
		t.Fatalf("写 Protected: %v", err)
	}

	if got, err := m.r.GetGuid(set, pk("BeatmapSet", "ID")); err != nil || got != setGuid {
		t.Errorf("读回主键 Guid = %v, %v", got, err)
	}
	if got, err := m.r.GetInt64(set, pk("BeatmapSet", "OnlineID")); err != nil || got != 123456 {
		t.Errorf("读回 OnlineID = %d, %v", got, err)
	}
	gotTs, err := m.r.GetTimestamp(set, pk("BeatmapSet", "DateAdded"))
	if err != nil || gotTs.Unix() != ts.Unix() || gotTs.Nanosecond() != ts.Nanosecond() {
		t.Errorf("读回 DateAdded = %v, %v（期望 %v）", gotTs, err, ts)
	}
	if got, err := m.r.GetString(set, pk("BeatmapSet", "Hash")); err != nil || got != "set-hash-1" {
		t.Errorf("读回 Hash = %q, %v", got, err)
	}
	if got, err := m.r.GetBool(set, pk("BeatmapSet", "DeletePending")); err != nil || got != true {
		t.Errorf("读回 DeletePending = %v, %v（S4 软删语义）", got, err)
	}
	if got, err := m.r.GetBool(set, pk("BeatmapSet", "Protected")); err != nil || got != false {
		t.Errorf("读回 Protected = %v, %v", got, err)
	}

	// Score：double、可空 double、binary。
	score, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Score"), mkGuid(9))
	if err != nil {
		t.Fatalf("建 Score 行失败: %v", err)
	}
	defer m.r.ReleaseObject(score)
	if err := m.r.SetFloat64(score, pk("Score", "Accuracy"), 0.987654321); err != nil {
		t.Fatalf("写 Accuracy: %v", err)
	}
	if err := m.r.SetFloat64(score, pk("Score", "PP"), 337.5); err != nil {
		t.Fatalf("写 PP: %v", err)
	}
	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	if err := m.r.SetBinary(score, pk("Score", "Payload"), payload); err != nil {
		t.Fatalf("写 Payload: %v", err)
	}
	if err := m.r.SetBinary(score, pk("Score", "Payload"), nil); err != nil { // 空二进制合法
		t.Fatalf("写空 Payload: %v", err)
	}
	if err := m.r.SetBinary(score, pk("Score", "Payload"), payload); err != nil {
		t.Fatalf("回写 Payload: %v", err)
	}

	if got, err := m.r.GetFloat64(score, pk("Score", "Accuracy")); err != nil || got != 0.987654321 {
		t.Errorf("读回 Accuracy = %v, %v", got, err)
	}
	if null, err := m.r.IsNull(score, pk("Score", "PP")); err != nil || null {
		t.Errorf("PP 写值后 IsNull = %v, %v", null, err)
	}
	// 解除可空列：SetNull → IsNull 真，typed 读严格报错（可空纪律）。
	if err := m.r.SetNull(score, pk("Score", "PP")); err != nil {
		t.Fatalf("SetNull(PP): %v", err)
	}
	if null, err := m.r.IsNull(score, pk("Score", "PP")); err != nil || !null {
		t.Errorf("SetNull 后 IsNull = %v, %v", null, err)
	}
	if _, err := m.r.GetFloat64(score, pk("Score", "PP")); err == nil {
		t.Error("NULL 列的 GetFloat64 应报错（typed getter 不静默折算零值）")
	}
	gotBin, err := m.r.GetBinary(score, pk("Score", "Payload"))
	if err != nil || !bytes.Equal(gotBin, payload) {
		t.Errorf("读回 Payload = %x, %v（期望 %x）", gotBin, err, payload)
	}

	// float32（BeatmapDifficulty.CircleSize 经 embedded 对象写入）。
	bm, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Beatmap"), mkGuid(3))
	if err != nil {
		t.Fatalf("建 Beatmap 行失败: %v", err)
	}
	defer m.r.ReleaseObject(bm)
	diff, err := m.r.SetEmbedded(bm, pk("Beatmap", "Difficulty"))
	if err != nil {
		t.Fatalf("挂接 embedded Difficulty 失败: %v", err)
	}
	defer m.r.ReleaseObject(diff)
	if err := m.r.SetFloat32(diff, pk("BeatmapDifficulty", "CircleSize"), 4.25); err != nil {
		t.Fatalf("写 CircleSize: %v", err)
	}
	if err := m.r.SetFloat64(diff, pk("BeatmapDifficulty", "SliderMultiplier"), 1.4); err != nil {
		t.Fatalf("写 SliderMultiplier: %v", err)
	}
	if got, err := m.r.GetFloat32(diff, pk("BeatmapDifficulty", "CircleSize")); err != nil || got != 4.25 {
		t.Errorf("读回 CircleSize = %v, %v", got, err)
	}
	if got, err := m.r.GetFloat64(diff, pk("BeatmapDifficulty", "SliderMultiplier")); err != nil || got != 1.4 {
		t.Errorf("读回 SliderMultiplier = %v, %v", got, err)
	}
}

// TestObjectLifecycle 覆盖对象创建/删除与主键语义：Guid/String 主键建行、
// 重复主键报错（last_error 解码）、GetOrCreate 幂等、物理删除。
func TestObjectLifecycle(t *testing.T) {
	m := openMiniWrite(t)
	defer m.rollbackAndClose()

	colKey := m.classKeyOf("BeatmapCollection")
	g1, g2, g3 := mkGuid(1), mkGuid(2), mkGuid(3)

	col1, err := m.r.CreateObjectWithGuidPK(colKey, g1)
	if err != nil {
		t.Fatalf("Guid 主键建行失败: %v", err)
	}
	m.r.ReleaseObject(col1)

	// 重复主键：报错且错误信息来自 realm_get_last_error 解码。
	if _, err := m.r.CreateObjectWithGuidPK(colKey, g1); err == nil {
		t.Fatal("重复 Guid 主键建行应报错")
	} else if !strings.Contains(err.Error(), "errno=") {
		t.Errorf("重复主键错误应含 last_error 解码信息，实际: %v", err)
	}

	// GetOrCreate：已存在 → 不新建；不存在 → 新建。
	obj, created, err := m.r.GetOrCreateObjectWithGuidPK(colKey, g1)
	if err != nil || created {
		t.Errorf("GetOrCreate 既有键 = (%v, %v)，期望 created=false", obj != nil, created)
	} else {
		m.r.ReleaseObject(obj)
	}
	obj, created, err = m.r.GetOrCreateObjectWithGuidPK(colKey, g2)
	if err != nil || !created {
		t.Errorf("GetOrCreate 新键 = (%v, %v)，期望 created=true", obj != nil, created)
	} else {
		m.r.ReleaseObject(obj)
	}

	// string 主键（RealmFile.Hash 的 Find ?? Create 语义）。
	fileKey := m.classKeyOf("File")
	f1, created, err := m.r.GetOrCreateObjectWithStringPK(fileKey, "hash-a")
	if err != nil || !created {
		t.Fatalf("GetOrCreate(StringPK) 新键 = (%v, %v)", f1 != nil, err)
	}
	m.r.ReleaseObject(f1)
	f2, created, err := m.r.GetOrCreateObjectWithStringPK(fileKey, "hash-a")
	if err != nil || created {
		t.Errorf("GetOrCreate(StringPK) 既有键 = (%v, %v)，期望 created=false", f2 != nil, created)
	} else {
		m.r.ReleaseObject(f2)
	}
	if _, err := m.r.CreateObjectWithStringPK(fileKey, "hash-a"); err == nil {
		t.Error("重复 string 主键建行应报错")
	}

	// 物理删除：仅收藏夹等无软删语义的表使用（S4）。
	col3, err := m.r.CreateObjectWithGuidPK(colKey, g3)
	if err != nil {
		t.Fatalf("建待删行失败: %v", err)
	}
	if err := m.r.DeleteObject(col3); err != nil {
		t.Fatalf("物理删除失败: %v", err)
	}
	m.r.ReleaseObject(col3)
	if n, err := m.r.ObjectCount(colKey); err != nil || n != 2 {
		t.Errorf("删除后 BeatmapCollection 行数 = %d, %v，期望 2", n, err)
	}
}

// TestEmbeddedObjects 覆盖 embedded 对象（BeatmapDifficulty /
// BeatmapUserSettings / RealmUser）：SetEmbedded 创建、GetLinkedObject 读回、
// 内部字段读写、可空列语义。
func TestEmbeddedObjects(t *testing.T) {
	m := openMiniWrite(t)
	defer m.rollbackAndClose()
	pk := m.propKeyOf

	bm, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Beatmap"), mkGuid(5))
	if err != nil {
		t.Fatalf("建 Beatmap 行失败: %v", err)
	}
	defer m.r.ReleaseObject(bm)

	// 单值 embedded：创建并读回（Score.User 同路径）。
	user, err := m.r.SetEmbedded(bm, pk("Beatmap", "UserSettings"))
	if err != nil {
		t.Fatalf("挂接 UserSettings 失败: %v", err)
	}
	defer m.r.ReleaseObject(user)
	if err := m.r.SetFloat64(user, pk("BeatmapUserSettings", "Offset"), -15.5); err != nil {
		t.Fatalf("写 Offset: %v", err)
	}
	readBack, err := m.r.GetLinkedObject(bm, pk("Beatmap", "UserSettings"))
	if err != nil {
		t.Fatalf("GetLinkedObject(UserSettings) 失败: %v", err)
	}
	defer m.r.ReleaseObject(readBack)
	if got, err := m.r.GetFloat64(readBack, pk("BeatmapUserSettings", "Offset")); err != nil || got != -15.5 {
		t.Errorf("embedded 读回 Offset = %v, %v", got, err)
	}
	// embedded 内的可空列语义与顶层行一致。
	if err := m.r.SetNull(readBack, pk("BeatmapUserSettings", "Offset")); err != nil {
		t.Fatalf("SetNull(Offset): %v", err)
	}
	if null, err := m.r.IsNull(readBack, pk("BeatmapUserSettings", "Offset")); err != nil || !null {
		t.Errorf("embedded 可空列 Offset IsNull = %v, %v", null, err)
	}
	// 同一属性二次 SetEmbedded：实证为替换语义——旧 embedded 对象失效
	// 销毁（与 realm_list_set_embedded 一致），新对象为全新状态（可空列
	// 默认 NULL，写入器须显式赋值）。
	repl, err := m.r.SetEmbedded(bm, pk("Beatmap", "UserSettings"))
	if err != nil {
		t.Fatalf("二次 SetEmbedded 应为替换语义: %v", err)
	}
	defer m.r.ReleaseObject(repl)
	if null, err := m.r.IsNull(repl, pk("BeatmapUserSettings", "Offset")); err != nil || !null {
		t.Errorf("替换后的 embedded 应为全新对象（可空列 Offset 为 NULL），实际 IsNull=%v, %v", null, err)
	}
	if err := m.r.SetNull(readBack, pk("BeatmapUserSettings", "Offset")); err == nil {
		t.Error("被替换的旧 embedded 句柄再操作应报错（errno=2010 invalidated）")
	}

	// Score.User（RealmUser）：embedded 挂在另一个父 class 上同样成立。
	score, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Score"), mkGuid(6))
	if err != nil {
		t.Fatalf("建 Score 行失败: %v", err)
	}
	defer m.r.ReleaseObject(score)
	ru, err := m.r.SetEmbedded(score, pk("Score", "User"))
	if err != nil {
		t.Fatalf("挂接 Score.User 失败: %v", err)
	}
	defer m.r.ReleaseObject(ru)
	if err := m.r.SetString(ru, pk("RealmUser", "Username"), "player1"); err != nil {
		t.Fatalf("写 Username: %v", err)
	}
	if err := m.r.SetInt64(ru, pk("RealmUser", "OnlineID"), 100001); err != nil {
		t.Fatalf("写 OnlineID: %v", err)
	}
	ruBack, err := m.r.GetLinkedObject(score, pk("Score", "User"))
	if err != nil {
		t.Fatalf("GetLinkedObject(Score.User) 失败: %v", err)
	}
	defer m.r.ReleaseObject(ruBack)
	if got, err := m.r.GetString(ruBack, pk("RealmUser", "Username")); err != nil || got != "player1" {
		t.Errorf("读回 Username = %q, %v", got, err)
	}
	if got, err := m.r.GetInt64(ruBack, pk("RealmUser", "OnlineID")); err != nil || got != 100001 {
		t.Errorf("读回 OnlineID = %d, %v", got, err)
	}
}

// TestObjectLinks 覆盖对象关系链接（BeatmapInfo→Set、Score→Beatmap）：
// SetLinkedObject 写、GetLinkedObject 读、SetNull 解绑（§5.4 版本更新时
// 成绩解绑的关系置 null 语义）与对象列表 add/get。
func TestObjectLinks(t *testing.T) {
	m := openMiniWrite(t)
	defer m.rollbackAndClose()
	pk := m.propKeyOf

	setGuid := mkGuid(11)
	bmGuid := mkGuid(12)
	set, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapSet"), setGuid)
	if err != nil {
		t.Fatalf("建 Set 行失败: %v", err)
	}
	defer m.r.ReleaseObject(set)
	bm, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Beatmap"), bmGuid)
	if err != nil {
		t.Fatalf("建 Beatmap 行失败: %v", err)
	}
	defer m.r.ReleaseObject(bm)

	// BeatmapInfo→Set。
	if err := m.r.SetLinkedObject(bm, pk("Beatmap", "BeatmapSet"), set); err != nil {
		t.Fatalf("SetLinkedObject(Beatmap.BeatmapSet) 失败: %v", err)
	}
	linked, err := m.r.GetLinkedObject(bm, pk("Beatmap", "BeatmapSet"))
	if err != nil {
		t.Fatalf("GetLinkedObject 失败: %v", err)
	}
	if got, err := m.r.GetGuid(linked, pk("BeatmapSet", "ID")); err != nil || got != setGuid {
		t.Errorf("链接目标读回 Guid 不符: %v, %v", got, err)
	}
	m.r.ReleaseObject(linked)

	// 对象列表（BeatmapSet.Beatmaps 前向 list）。
	bmaps, err := m.r.GetList(set, pk("BeatmapSet", "Beatmaps"))
	if err != nil {
		t.Fatalf("GetList(Beatmaps) 失败: %v", err)
	}
	defer bmaps.Close()
	if err := bmaps.AddLinkedObject(bm); err != nil {
		t.Fatalf("Beatmaps.AddLinkedObject 失败: %v", err)
	}
	if n, err := bmaps.Size(); err != nil || n != 1 {
		t.Errorf("Beatmaps Size = %d, %v，期望 1", n, err)
	}
	elem, err := bmaps.GetLinkedObject(0)
	if err != nil {
		t.Fatalf("Beatmaps.GetLinkedObject(0) 失败: %v", err)
	}
	if got, err := m.r.GetGuid(elem, pk("Beatmap", "ID")); err != nil || got != bmGuid {
		t.Errorf("Beatmaps[0] 读回 Guid 不符: %v, %v", got, err)
	}
	m.r.ReleaseObject(elem)

	// Score→Beatmap：链接后解绑（关系置 null、句柄读回 (nil, nil)）。
	score, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Score"), mkGuid(13))
	if err != nil {
		t.Fatalf("建 Score 行失败: %v", err)
	}
	defer m.r.ReleaseObject(score)
	if err := m.r.SetLinkedObject(score, pk("Score", "BeatmapInfo"), bm); err != nil {
		t.Fatalf("SetLinkedObject(Score.BeatmapInfo) 失败: %v", err)
	}
	if err := m.r.SetNull(score, pk("Score", "BeatmapInfo")); err != nil {
		t.Fatalf("解绑 Score.BeatmapInfo 失败: %v", err)
	}
	if got, err := m.r.GetLinkedObject(score, pk("Score", "BeatmapInfo")); err != nil || got != nil {
		t.Errorf("解绑后 GetLinkedObject = (%v, %v)，期望 (nil, nil)", got, err)
	}

	// 前向 list（BeatmapSet.Beatmaps）与子侧链接列（Beatmap.BeatmapSet）是
	// 两条独立持久列（lazer 同款形态），摘除一侧不影响另一侧的存量。
	if n, err := bmaps.Size(); err != nil || n != 1 {
		t.Errorf("解绑子侧链接后 Beatmaps Size = %d, %v，期望 1（独立列不受影响）", n, err)
	}
}

// TestListOperations 覆盖四类 list 列（Beatmaps 已在 TestObjectLinks 验证）：
// BeatmapMD5Hashes（string）、Pauses（int）、Files（embedded + 指向 File 行）。
func TestListOperations(t *testing.T) {
	m := openMiniWrite(t)
	defer m.rollbackAndClose()
	pk := m.propKeyOf

	// string 列表：add/get/set/erase/clear 全路径。
	col, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapCollection"), mkGuid(21))
	if err != nil {
		t.Fatalf("建收藏夹失败: %v", err)
	}
	defer m.r.ReleaseObject(col)
	hashes, err := m.r.GetList(col, pk("BeatmapCollection", "BeatmapMD5Hashes"))
	if err != nil {
		t.Fatalf("GetList(BeatmapMD5Hashes) 失败: %v", err)
	}
	defer hashes.Close()
	for _, h := range []string{"md5-a", "md5-b", "md5-c"} {
		if err := hashes.AddString(h); err != nil {
			t.Fatalf("AddString(%q): %v", h, err)
		}
	}
	if n, err := hashes.Size(); err != nil || n != 3 {
		t.Errorf("列表 Size = %d, %v，期望 3", n, err)
	}
	if got, err := hashes.GetString(1); err != nil || got != "md5-b" {
		t.Errorf("GetString(1) = %q, %v", got, err)
	}
	if err := hashes.SetString(1, "md5-B"); err != nil {
		t.Fatalf("SetString(1): %v", err)
	}
	if got, err := hashes.GetString(1); err != nil || got != "md5-B" {
		t.Errorf("SetString 后 GetString(1) = %q, %v", got, err)
	}
	if err := hashes.Erase(0); err != nil {
		t.Fatalf("Erase(0): %v", err)
	}
	if n, err := hashes.Size(); err != nil || n != 2 {
		t.Errorf("Erase 后 Size = %d, %v，期望 2", n, err)
	}
	if err := hashes.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n, err := hashes.Size(); err != nil || n != 0 {
		t.Errorf("Clear 后 Size = %d, %v，期望 0", n, err)
	}

	// int 列表（Pauses）。
	score, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("Score"), mkGuid(22))
	if err != nil {
		t.Fatalf("建 Score 失败: %v", err)
	}
	defer m.r.ReleaseObject(score)
	pauses, err := m.r.GetList(score, pk("Score", "Pauses"))
	if err != nil {
		t.Fatalf("GetList(Pauses) 失败: %v", err)
	}
	defer pauses.Close()
	if err := pauses.AddInt64(1200); err != nil {
		t.Fatalf("AddInt64: %v", err)
	}
	if err := pauses.AddInt64(3400); err != nil {
		t.Fatalf("AddInt64: %v", err)
	}
	if err := pauses.SetInt64(0, 999); err != nil {
		t.Fatalf("SetInt64: %v", err)
	}
	if got, err := pauses.GetInt64(0); err != nil || got != 999 {
		t.Errorf("GetInt64(0) = %d, %v", got, err)
	}
	if got, err := pauses.GetInt64(1); err != nil || got != 3400 {
		t.Errorf("GetInt64(1) = %d, %v", got, err)
	}

	// embedded 列表（Files）：AddEmbedded 创建 RealmNamedFileUsage，内部
	// File 链接指向 string 主键的 RealmFile 行。
	fileRow, created, err := m.r.GetOrCreateObjectWithStringPK(m.classKeyOf("File"), "file-hash-1")
	if err != nil || !created {
		t.Fatalf("建 File 行 = (%v, %v)", fileRow != nil, err)
	}
	defer m.r.ReleaseObject(fileRow)
	if err := m.r.SetInt64(fileRow, pk("File", "Size"), 4096); err != nil {
		t.Fatalf("写 File.Size: %v", err)
	}

	files, err := m.r.GetList(score, pk("Score", "Files"))
	if err != nil {
		t.Fatalf("GetList(Score.Files) 失败: %v", err)
	}
	defer files.Close()
	usage, err := files.AddEmbedded()
	if err != nil {
		t.Fatalf("Files.AddEmbedded 失败: %v", err)
	}
	defer m.r.ReleaseObject(usage)
	if err := m.r.SetLinkedObject(usage, pk("RealmNamedFileUsage", "File"), fileRow); err != nil {
		t.Fatalf("embedded 内链接 File 失败: %v", err)
	}
	if err := m.r.SetString(usage, pk("RealmNamedFileUsage", "Filename"), "replay.osr"); err != nil {
		t.Fatalf("写 Filename: %v", err)
	}
	usageBack, err := files.GetLinkedObject(0)
	if err != nil {
		t.Fatalf("Files.GetLinkedObject(0) 失败: %v", err)
	}
	if got, err := m.r.GetString(usageBack, pk("RealmNamedFileUsage", "Filename")); err != nil || got != "replay.osr" {
		t.Errorf("读回 Filename = %q, %v", got, err)
	}
	fTarget, err := m.r.GetLinkedObject(usageBack, pk("RealmNamedFileUsage", "File"))
	if err != nil {
		t.Fatalf("embedded→File 链接读回失败: %v", err)
	}
	if got, err := m.r.GetString(fTarget, pk("File", "Hash")); err != nil || got != "file-hash-1" {
		t.Errorf("读回 File.Hash = %q, %v", got, err)
	}
	if got, err := m.r.GetInt64(fTarget, pk("File", "Size")); err != nil || got != 4096 {
		t.Errorf("读回 File.Size = %d, %v", got, err)
	}
	m.r.ReleaseObject(fTarget)

	// 同一 embedded class 挂第二个父（BeatmapSet.Files）+ SetEmbedded 替换。
	set, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapSet"), mkGuid(23))
	if err != nil {
		t.Fatalf("建 Set 失败: %v", err)
	}
	defer m.r.ReleaseObject(set)
	setFiles, err := m.r.GetList(set, pk("BeatmapSet", "Files"))
	if err != nil {
		t.Fatalf("GetList(BeatmapSet.Files) 失败: %v", err)
	}
	defer setFiles.Close()
	u1, err := setFiles.AddEmbedded()
	if err != nil {
		t.Fatalf("Set.Files AddEmbedded 失败: %v", err)
	}
	defer m.r.ReleaseObject(u1)
	if err := m.r.SetString(u1, pk("RealmNamedFileUsage", "Filename"), "audio.mp3"); err != nil {
		t.Fatalf("写 Filename: %v", err)
	}
	u2, err := setFiles.SetEmbedded(0) // 替换：旧 embedded 销毁
	if err != nil {
		t.Fatalf("Set.Files SetEmbedded(0) 失败: %v", err)
	}
	defer m.r.ReleaseObject(u2)
	if got, err := m.r.GetString(u2, pk("RealmNamedFileUsage", "Filename")); err != nil || got != "" {
		t.Errorf("替换后的 embedded 应为全新对象（Filename 空串），实际 %q, %v", got, err)
	}
}

// TestFindFirstByString 验证按条件查目标库难度（§5.2-3 的原语）：
// RQL 主路径 + 全表扫描兜底路径（白盒直测）+ 无匹配 (nil, nil)。
func TestFindFirstByString(t *testing.T) {
	m := openMiniWrite(t)
	pk := m.propKeyOf
	bmKey := m.classKeyOf("Beatmap")

	seed := []struct {
		guid [16]byte
		hash string
		name string
	}{
		{mkGuid(31), "bm-hash-1", "Easy"},
		{mkGuid(32), "bm-hash-2", "Normal"},
		{mkGuid(33), "bm-hash-3", "Hard"},
	}
	for _, s := range seed {
		bm, err := m.r.CreateObjectWithGuidPK(bmKey, s.guid)
		if err != nil {
			t.Fatalf("建难度行失败: %v", err)
		}
		if err := m.r.SetString(bm, pk("Beatmap", "Hash"), s.hash); err != nil {
			t.Fatalf("写 Hash: %v", err)
		}
		if err := m.r.SetString(bm, pk("Beatmap", "DifficultyName"), s.name); err != nil {
			t.Fatalf("写 DifficultyName: %v", err)
		}
		m.r.ReleaseObject(bm)
	}

	hashProp := pk("Beatmap", "Hash")
	nameProp := pk("Beatmap", "DifficultyName")

	// RQL 主路径：命中。
	obj, err := m.r.FindFirstByString(bmKey, hashProp, "bm-hash-2")
	if err != nil {
		t.Fatalf("FindFirstByString 失败: %v", err)
	}
	if obj == nil {
		t.Fatal("FindFirstByString 应命中 bm-hash-2")
	}
	if got, err := m.r.GetString(obj, nameProp); err != nil || got != "Normal" {
		t.Errorf("命中行 DifficultyName = %q, %v，期望 Normal", got, err)
	}
	m.r.ReleaseObject(obj)

	// RQL 主路径：无匹配。
	obj, err = m.r.FindFirstByString(bmKey, hashProp, "bm-hash-none")
	if err != nil {
		t.Fatalf("FindFirstByString(无匹配) 失败: %v", err)
	}
	if obj != nil {
		t.Error("无匹配应返回 (nil, nil)")
		m.r.ReleaseObject(obj)
	}

	// 兜底路径白盒直测（RQL 不可用时的全表扫描）。
	obj, err = m.r.findFirstByStringScan(bmKey, hashProp, "bm-hash-3")
	if err != nil {
		t.Fatalf("findFirstByStringScan 失败: %v", err)
	}
	if obj == nil {
		t.Fatal("扫描路径应命中 bm-hash-3")
	}
	if got, err := m.r.GetString(obj, nameProp); err != nil || got != "Hard" {
		t.Errorf("扫描路径命中行 DifficultyName = %q, %v，期望 Hard", got, err)
	}
	m.r.ReleaseObject(obj)

	obj, err = m.r.findFirstByStringScan(bmKey, hashProp, "absent")
	if err != nil {
		t.Fatalf("findFirstByStringScan(无匹配) 失败: %v", err)
	}
	if obj != nil {
		t.Error("扫描路径无匹配应返回 (nil, nil)")
		m.r.ReleaseObject(obj)
	}

	// File 表 string 主键查询（成绩挂接外的另一常见查询面）。
	f, _, err := m.r.GetOrCreateObjectWithStringPK(m.classKeyOf("File"), "file-x")
	if err != nil {
		t.Fatalf("建 File 失败: %v", err)
	}
	m.r.ReleaseObject(f)
	fobj, err := m.r.FindFirstByString(m.classKeyOf("File"), pk("File", "Hash"), "file-x")
	if err != nil {
		t.Fatalf("FindFirstByString(File.Hash) 失败: %v", err)
	}
	if fobj == nil {
		t.Fatal("File.Hash 查询应命中")
	}
	m.r.ReleaseObject(fobj)

	m.commitAndClose()
}

// TestSchemaSnapshotDrift 验证结构档案采集与 DriftGuard 支撑：
// 写前/写后结构零漂移（动态会话不迁移，S3）、篡改可检出、JSON 导出可回读。
func TestSchemaSnapshotDrift(t *testing.T) {
	m := openMiniWrite(t)

	before, err := m.r.CaptureSchemaSnapshot()
	if err != nil {
		t.Fatalf("写前采集结构失败: %v", err)
	}
	if before.SchemaVersion != miniSchemaVersion {
		t.Errorf("采集的 schema 版本 = %d, 期望 %d", before.SchemaVersion, miniSchemaVersion)
	}
	// mini schema 完整性：9 个 class，embedded 标记正确。
	if len(before.Classes) != 9 {
		t.Errorf("采集 class 数 = %d, 期望 9", len(before.Classes))
	}
	for _, c := range before.Classes {
		wantEmbedded := c.Name == "BeatmapDifficulty" || c.Name == "BeatmapUserSettings" ||
			c.Name == "RealmNamedFileUsage" || c.Name == "RealmUser"
		if got := c.Flags&classFlagEmbedded != 0; got != wantEmbedded {
			t.Errorf("class %s embedded 标记 = %v, 期望 %v", c.Name, got, wantEmbedded)
		}
	}

	// 写入数据（不碰结构）。
	col, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapCollection"), mkGuid(41))
	if err != nil {
		t.Fatalf("建行失败: %v", err)
	}
	m.r.ReleaseObject(col)
	m.commitAndClose()

	ro, err := m.lib.OpenDynamicReadOnly(m.path)
	if err != nil {
		t.Fatalf("动态只读重开失败: %v", err)
	}
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("begin_read 失败: %v", err)
	}
	after, err := ro.CaptureSchemaSnapshot()
	if err != nil {
		t.Fatalf("写后采集结构失败: %v", err)
	}
	if err := ro.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if !before.Equal(after) {
		bj, _ := before.JSON()
		aj, _ := after.JSON()
		t.Errorf("写前/写后结构档案应零漂移（DriftGuard 语义），实际检出差异\n写前:\n%s\n写后:\n%s", bj, aj)
	}

	// 篡改任一结构字段必须被 Equal 检出。
	tampered := *after
	tampered.Classes[0].Properties[0].Name += "-X"
	if before.Equal(&tampered) {
		t.Error("篡改属性名后 Equal 仍为真，漂移检出失效")
	}

	// JSON 导出（结构档案落盘形态）可反序列化回等价结构。
	raw, err := before.JSON()
	if err != nil {
		t.Fatalf("JSON 导出失败: %v", err)
	}
	if !bytes.Contains(raw, []byte("BeatmapSet")) {
		t.Error("JSON 导出应含 class 名")
	}
	var back SchemaSnapshot
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("JSON 反序列化失败: %v", err)
	}
	if !reflect.DeepEqual(before, &back) {
		t.Error("JSON 往返后结构档案不等价")
	}
}

// TestTransactionLifecycle 验证事务边界：rollback 丢弃未提交写入、
// commit 持久化、同一会话内事务串行复用（§3.3 批事务的底层原语）。
func TestTransactionLifecycle(t *testing.T) {
	m := openMiniWrite(t)
	col, err := m.r.CreateObjectWithGuidPK(m.classKeyOf("BeatmapCollection"), mkGuid(51))
	if err != nil {
		t.Fatalf("建行失败: %v", err)
	}
	if err := m.r.SetString(col, m.propKeyOf("BeatmapCollection", "Name"), "v1"); err != nil {
		t.Fatalf("写 Name: %v", err)
	}
	m.r.ReleaseObject(col)
	colKey := m.classKeyOf("BeatmapCollection")
	nameProp := m.propKeyOf("BeatmapCollection", "Name")
	m.commitAndClose()

	// 动态读写会话上串行跑 rollback 与 commit 两个事务。
	rw, err := m.lib.OpenDynamicReadWrite(m.path)
	if err != nil {
		t.Fatalf("动态读写打开失败: %v", err)
	}
	defer rw.Close()

	// 事务一：写入后 rollback，值不变。
	if err := rw.BeginWrite(); err != nil {
		t.Fatalf("begin_write: %v", err)
	}
	obj, err := rw.FirstObject(colKey)
	if err != nil {
		t.Fatalf("取首行失败: %v", err)
	}
	if err := rw.SetString(obj, nameProp, "v2-rolled-back"); err != nil {
		t.Fatalf("写 Name: %v", err)
	}
	rw.ReleaseObject(obj)
	if err := rw.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// 事务二：读回确认 + 提交新值。
	if err := rw.BeginRead(); err != nil {
		t.Fatalf("begin_read: %v", err)
	}
	obj, err = rw.FirstObject(colKey)
	if err != nil {
		t.Fatalf("取首行失败: %v", err)
	}
	if got, err := rw.GetString(obj, nameProp); err != nil || got != "v1" {
		t.Errorf("rollback 后 Name = %q, %v，期望 v1", got, err)
	}
	rw.ReleaseObject(obj)

	if err := rw.BeginWrite(); err != nil {
		t.Fatalf("begin_write: %v", err)
	}
	obj, err = rw.FirstObject(colKey)
	if err != nil {
		t.Fatalf("取首行失败: %v", err)
	}
	if err := rw.SetString(obj, nameProp, "v3"); err != nil {
		t.Fatalf("写 Name: %v", err)
	}
	rw.ReleaseObject(obj)
	if err := rw.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 独立会话读回 commit 结果。
	ro, err := m.lib.OpenDynamicReadOnly(m.path)
	if err != nil {
		t.Fatalf("动态只读重开失败: %v", err)
	}
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("begin_read: %v", err)
	}
	obj, err = ro.FirstObject(colKey)
	if err != nil {
		t.Fatalf("取首行失败: %v", err)
	}
	if got, err := ro.GetString(obj, nameProp); err != nil || got != "v3" {
		t.Errorf("commit 后 Name = %q, %v，期望 v3", got, err)
	}
	ro.ReleaseObject(obj)
	if err := ro.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
}
