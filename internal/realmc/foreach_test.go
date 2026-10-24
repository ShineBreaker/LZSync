package realmc

// ForEach 单测（design §3.2 收集步骤②的全表枚举原语）：mini schema 建库
// 播多行 → 动态只读重开 → ForEach 枚举计数与内容、零行 class、错误中止
// 三个分支。主 agent 授权的跨包最小增补（见 BuildResult.notes）。

import (
	"errors"
	"sort"
	"testing"
	"unsafe"
)

func TestForEachEnumeratesAllRows(t *testing.T) {
	m := openMiniWrite(t)
	colKey := m.classKeyOf("BeatmapCollection")
	for i := byte(1); i <= 3; i++ {
		obj, err := m.r.CreateObjectWithGuidPK(colKey, mkGuid(i))
		if err != nil {
			t.Fatalf("建收藏夹行 %d 失败: %v", i, err)
		}
		if err := m.r.SetString(obj, m.propKeyOf("BeatmapCollection", "Name"), string(rune('a'+i))); err != nil {
			t.Fatalf("写 Name 失败: %v", err)
		}
		m.r.ReleaseObject(obj)
	}
	m.commitAndClose()

	ro, err := m.lib.OpenDynamicReadOnly(m.path)
	if err != nil {
		t.Fatalf("动态只读重开失败: %v", err)
	}
	defer ro.Close()
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("begin_read 失败: %v", err)
	}
	// 只读会话读事务不做显式收尾（commit/rollback 均报 2005），Close 即结束

	var names []string
	if err := ro.ForEach(colKey, func(obj unsafe.Pointer) error {
		name, err := ro.GetString(obj, m.propKeyOf("BeatmapCollection", "Name"))
		if err != nil {
			return err
		}
		names = append(names, name)
		return nil
	}); err != nil {
		t.Fatalf("ForEach: %v", err)
	}
	sort.Strings(names)
	want := []string{"b", "c", "d"}
	if len(names) != len(want) {
		t.Fatalf("ForEach 枚举 %d 行 = %v，期望 %v", len(names), names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q，期望 %q", i, names[i], want[i])
		}
	}
}

func TestForEachEmptyClassIsNoOp(t *testing.T) {
	m := openMiniWrite(t)
	m.commitAndClose()

	ro, err := m.lib.OpenDynamicReadOnly(m.path)
	if err != nil {
		t.Fatalf("动态只读重开失败: %v", err)
	}
	defer ro.Close()
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("begin_read 失败: %v", err)
	}
	// 只读会话读事务不做显式收尾（commit/rollback 均报 2005），Close 即结束

	calls := 0
	if err := ro.ForEach(m.classKeyOf("BeatmapCollection"), func(unsafe.Pointer) error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("零行 class 的 ForEach: %v", err)
	}
	if calls != 0 {
		t.Errorf("零行 class 回调了 %d 次，期望 0", calls)
	}
}

func TestForEachStopsOnError(t *testing.T) {
	m := openMiniWrite(t)
	colKey := m.classKeyOf("BeatmapCollection")
	for i := byte(1); i <= 3; i++ {
		obj, err := m.r.CreateObjectWithGuidPK(colKey, mkGuid(i))
		if err != nil {
			t.Fatalf("建收藏夹行 %d 失败: %v", i, err)
		}
		m.r.ReleaseObject(obj)
	}
	m.commitAndClose()

	ro, err := m.lib.OpenDynamicReadOnly(m.path)
	if err != nil {
		t.Fatalf("动态只读重开失败: %v", err)
	}
	defer ro.Close()
	if err := ro.BeginRead(); err != nil {
		t.Fatalf("begin_read 失败: %v", err)
	}
	// 只读会话读事务不做显式收尾（commit/rollback 均报 2005），Close 即结束

	sentinel := errors.New("stop")
	calls := 0
	err = ro.ForEach(colKey, func(unsafe.Pointer) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ForEach 错误 = %v，期望原样上抛 %v", err, sentinel)
	}
	if calls != 1 {
		t.Errorf("中止后回调次数 = %d，期望 1（首个错误即停）", calls)
	}
}
