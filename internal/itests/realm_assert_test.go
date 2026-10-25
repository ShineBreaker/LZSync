package itests

// M3 全链断言的小助手（现场读回面与 hub 只读直查面）：与既有 mutate/
// withRO/rowCount（harness_test.go / versionreplace_test.go）同风格收拢，
// 供 Ez/g0v0/组合三个全链用例共用。

import (
	"testing"
	"unsafe"

	"lzsync/internal/realmc"
)

// findRowSoft 只读会话内按 string 列取行句柄；缺失返回 (nil, nil)（调用方
// 自行区分「行不存在」与错误，负责释放非 nil 句柄）。
func findRowSoft(r *realmc.Realm, cls, prop, val string) (unsafe.Pointer, error) {
	ck, err := r.FindClass(cls)
	if err != nil {
		return nil, err
	}
	pk, err := r.FindProperty(ck, prop)
	if err != nil {
		return nil, err
	}
	return r.FindFirstByString(ck, pk, val)
}

// findRowByString findRowSoft 的断言形态（缺失即失败；调用方负责释放）。
func findRowByString(t *testing.T, r *realmc.Realm, cls, prop, val string) unsafe.Pointer {
	t.Helper()
	obj, err := findRowSoft(r, cls, prop, val)
	if err != nil || obj == nil {
		t.Fatalf("找行 %s.%s=%s: %v（行缺失）", cls, prop, val, err)
	}
	return obj
}

// propKey 只读会话内取列 key（缺失即失败）。
func propKey(t *testing.T, r *realmc.Realm, cls, name string) int64 {
	t.Helper()
	ck, err := r.FindClass(cls)
	if err != nil {
		t.Fatalf("FindClass(%s): %v", cls, err)
	}
	k, err := r.FindProperty(ck, name)
	if err != nil {
		t.Fatalf("FindProperty(%s.%s): %v", cls, name, err)
	}
	return k
}

func assertRealmInt64(t *testing.T, r *realmc.Realm, obj unsafe.Pointer, cls, name string, want int64, label string) {
	t.Helper()
	v, err := r.GetInt64(obj, propKey(t, r, cls, name))
	if err != nil || v != want {
		t.Errorf("%s：%s.%s = (%d, %v)，期望 %d", label, cls, name, v, err, want)
	}
}

func assertRealmFloat64(t *testing.T, r *realmc.Realm, obj unsafe.Pointer, cls, name string, want float64, label string) {
	t.Helper()
	v, err := r.GetFloat64(obj, propKey(t, r, cls, name))
	if err != nil || v != want {
		t.Errorf("%s：%s.%s = (%v, %v)，期望 %v", label, cls, name, v, err, want)
	}
}

func assertRealmBool(t *testing.T, r *realmc.Realm, obj unsafe.Pointer, cls, name string, want bool, label string) {
	t.Helper()
	v, err := r.GetBool(obj, propKey(t, r, cls, name))
	if err != nil || v != want {
		t.Errorf("%s：%s.%s = (%v, %v)，期望 %v", label, cls, name, v, err, want)
	}
}

func assertRealmStr(t *testing.T, r *realmc.Realm, obj unsafe.Pointer, cls, name, want, label string) {
	t.Helper()
	v, err := r.GetString(obj, propKey(t, r, cls, name))
	if err != nil || v != want {
		t.Errorf("%s：%s.%s = (%q, %v)，期望 %q", label, cls, name, v, err, want)
	}
}

// assertHubCount hub 只读直查的 COUNT 断言（args 参数化透传）。
func assertHubCount(t *testing.T, e *env, query string, want int, label string, args ...any) {
	t.Helper()
	var got int
	if err := e.hub.DB().QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("hub 查 %s: %v", label, err)
	}
	if got != want {
		t.Errorf("hub %s = %d，期望 %d", label, got, want)
	}
}

// assertHubKeys hub 单列键集断言（query 返回一列字符串；want 为期望全集）。
func assertHubKeys(t *testing.T, e *env, query string, want map[string]bool, label string) {
	t.Helper()
	rows, err := e.hub.DB().Query(query)
	if err != nil {
		t.Fatalf("hub 查 %s: %v", label, err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("hub 读 %s 行: %v", label, err)
		}
		got[k] = true
	}
	if len(got) != len(want) {
		t.Errorf("hub %s = %d 键 %v，期望 %d 键", label, len(got), got, len(want))
	}
	for k := range want {
		if !got[k] {
			t.Errorf("hub %s 缺键 %s", label, k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("hub %s 多出键 %s", label, k)
		}
	}
}
