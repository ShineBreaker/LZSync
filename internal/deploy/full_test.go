package deploy

// 增强形态目标库测试（BeatmapCollection 完整列面：Guid 主键 + Name +
// BeatmapMD5Hashes string 列表 + LastModified timestamp）——fixture 镜像
// 施工期不承载集合列/主键（fixture 包注释的已知边界），此处以 realmc
// 显式 schema 构造完整形态库，覆盖「逐字段断言」的剩余列面，以及占用
// 拒写、批回滚、恢复组 tombstone 流程（§8.2-5/6）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// fullCollectionSchema 增强形态 BeatmapCollection schema；lastModifiedType
// 参数供回滚测试注入列类型错误（正常为 Timestamp）。
func fullCollectionSchema(lastModifiedType realmc.PropType) *realmc.Schema {
	return &realmc.Schema{Classes: []realmc.ClassDef{
		{Name: "BeatmapCollection", PrimaryKey: "ID", Properties: []realmc.PropertyDef{
			{Name: "ID", Type: realmc.PropTypeUUID, IsPrimaryKey: true},
			{Name: "Name", Type: realmc.PropTypeString},
			{Name: "BeatmapMD5Hashes", Type: realmc.PropTypeString, Collection: realmc.CollectionList},
			{Name: "LastModified", Type: lastModifiedType},
		}},
	}}
}

// buildFullLib 建一个增强形态空库并返回 LibraryRef。
func buildFullLib(t *testing.T, lib *realmc.Library, lastModifiedType realmc.PropType) forks.LibraryRef {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "client.realm")
	r, err := lib.OpenWithSchema(path, fullCollectionSchema(lastModifiedType), 52)
	if err != nil {
		t.Fatalf("建增强库: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("关闭增强库: %v", err)
	}
	return forks.LibraryRef{
		ForkID: "lazer", DataDir: dir, RealmPath: path,
		DiskVersion: 52, VersionSource: forks.VersionFromOpen,
	}
}

// TestDeployFullFeaturedCollection 完整列面的投放往返：
// create 逐字段（Guid/Name/MD5 逐元素/LastModified）→ 重续判「已一致 →
// 跳过」→ 就地更新保目标 Guid（§4.2）。
func TestDeployFullFeaturedCollection(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	ref := buildFullLib(t, lib, realmc.PropTypeTimestamp)
	h := openMemHub(t)

	fav1 := hub.Collection{
		ID: "11111111-1111-1111-1111-111111111111", Name: "满配夹",
		BeatmapMD5Hashes: []string{"md5-a", "md5-b", "md5-c"}, LastModified: t0,
	}
	fav2 := hub.Collection{
		ID: "22222222-2222-2222-2222-222222222222", Name: "第二夹",
		BeatmapMD5Hashes: []string{"md5-z"}, LastModified: t0,
	}
	applySnap(t, h, hub.Snapshot{ForkID: "src", CollectedAt: t0,
		Collections: []hub.Collection{fav1, fav2}})

	plan, err := BuildPlan(ctx, h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("计划项 = %d（期望 2）", len(plan.Items))
	}
	runner := NewRunner(Deps{Hub: h})
	report, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#1: %v", err)
	}
	if len(report.Applied) != 2 || len(report.Skipped) != 0 {
		t.Fatalf("Deploy#1 报告 = applied %d, skipped %+v", len(report.Applied), report.Skipped)
	}

	// 逐字段断言（含 Guid 主键、MD5 列表逐元素、timestamp）。只读会话用完
	// 即关：Deploy 的占用探测在自身会话之外做 flock，残留的只读会话会让
	// 下一次 Deploy 误判占用（施工期实证）。
	withReadOnly(t, lib, ref.RealmPath, func(r *realmc.Realm) {
		if n := objectCountOf(t, r); n != 2 {
			t.Errorf("行数 = %d（期望 2）", n)
		}
		assertCollectionEqual(t, r, fav1)
		assertCollectionEqual(t, r, fav2)
	})

	// 重续：同计划重跑 → 全部「已存在 → 跳过」，零 applied（§3.3）。
	report2, err := runner.Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy#2: %v", err)
	}
	if len(report2.Applied) != 0 {
		t.Errorf("重跑 Applied = %d（期望 0）", len(report2.Applied))
	}
	skipTotal := 0
	for _, st := range report2.Skipped {
		skipTotal += st.Count
	}
	if skipTotal != 2 {
		t.Errorf("重跑 skip 计数 = %d（期望 2）: %+v", skipTotal, report2.Skipped)
	}

	// 就地更新保目标 Guid：① 模拟「投放后重新收集目标 fork」（投放不写
	// presence——hub 语义：presence 只由收集维护）；② forkC 收集同键收藏
	//（不同 Guid + 不同成员）→ canonical 整行覆盖（HB3）→ 对账出冲突 →
	// update 计划 → 目标 Guid 不变。
	withReadOnly(t, lib, ref.RealmPath, func(r *realmc.Realm) {
		collected := []hub.Collection{readCollectionOr(t, r, "满配夹"), readCollectionOr(t, r, "第二夹")}
		applySnap(t, h, hub.Snapshot{ForkID: ref.ForkID, CollectedAt: t0.Add(time.Hour + time.Minute),
			Collections: collected})
	})
	t1 := t0.Add(2 * time.Hour)
	fav1v2 := hub.Collection{
		ID: "33333333-3333-3333-3333-333333333333", Name: "满配夹",
		BeatmapMD5Hashes: []string{"md5-a", "md5-b", "md5-c", "md5-d"}, LastModified: t1,
	}
	applySnap(t, h, hub.Snapshot{ForkID: "fork-c", CollectedAt: t1, Collections: []hub.Collection{fav1v2}})

	plan3, err := BuildPlan(ctx, h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan#3: %v", err)
	}
	if len(plan3.Items) != 1 || plan3.Items[0].Action != ActionUpdate {
		t.Fatalf("计划#3 = %+v（期望仅「满配夹」update）", plan3.Items)
	}
	report3, err := runner.Deploy(ctx, plan3, nil)
	if err != nil {
		t.Fatalf("Deploy#3: %v", err)
	}
	if len(report3.Applied) != 1 || report3.Applied[0].Action != ActionUpdate {
		t.Errorf("Deploy#3 applied = %+v", report3.Applied)
	}

	// 读回：成员更新为目标 canonical、Guid 保持首次落地的值、LM = 新值。
	withReadOnly(t, lib, ref.RealmPath, func(r *realmc.Realm) {
		got, ok := readCollection(t, r, "满配夹")
		if !ok {
			t.Fatalf("读回缺「满配夹」")
		}
		if got.ID != fav1.ID {
			t.Errorf("就地更新后 Guid = %s，期望保持目标值 %s（§4.2 保目标 Guid）", got.ID, fav1.ID)
		}
		if !equalUnordered(got.BeatmapMD5Hashes, fav1v2.BeatmapMD5Hashes) {
			t.Errorf("MD5 成员 = %v，期望 %v", got.BeatmapMD5Hashes, fav1v2.BeatmapMD5Hashes)
		}
		if !got.LastModified.Equal(t1) {
			t.Errorf("LastModified = %v，期望 %v", got.LastModified, t1)
		}
	})
}

// withReadOnly 开只读会话执行 fn 后即关（避免会话残留影响后续 Deploy 的
// 占用探测）。
func withReadOnly(t *testing.T, lib *realmc.Library, path string, fn func(*realmc.Realm)) {
	t.Helper()
	r, err := lib.OpenDynamicReadOnly(path)
	if err != nil {
		t.Fatalf("只读打开 %s: %v", path, err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	fn(r)
}

func assertCollectionEqual(t *testing.T, r *realmc.Realm, want hub.Collection) {
	t.Helper()
	got, ok := readCollection(t, r, want.Name)
	if !ok {
		t.Errorf("读回缺 %q", want.Name)
		return
	}
	if got.ID != want.ID {
		t.Errorf("%s Guid = %s，期望 %s", want.Name, got.ID, want.ID)
	}
	if !equalUnordered(got.BeatmapMD5Hashes, want.BeatmapMD5Hashes) {
		t.Errorf("%s MD5 = %v，期望 %v", want.Name, got.BeatmapMD5Hashes, want.BeatmapMD5Hashes)
	}
	if !got.LastModified.Equal(want.LastModified) {
		t.Errorf("%s LastModified = %v，期望 %v", want.Name, got.LastModified, want.LastModified)
	}
}

// readCollectionOr 读回指定收藏，缺失即失败。
func readCollectionOr(t *testing.T, r *realmc.Realm, name string) hub.Collection {
	t.Helper()
	c, ok := readCollection(t, r, name)
	if !ok {
		t.Fatalf("读回缺 %q", name)
	}
	return c
}

func equalUnordered(a, b []string) bool {	sa := append([]string(nil), a...)
	sb := append([]string(nil), b...)
	sort.Strings(sa)
	sort.Strings(sb)
	if len(sa) != len(sb) {
		return false
	}
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// TestDeployBatchRollback 批内写入异常 → 整批回滚（S1）：LastModified 列
// 声明为 Int（畸形目标库）→ SetTimestamp 类型错误 → 批失败，目标库零变更。
func TestDeployBatchRollback(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	ref := buildFullLib(t, lib, realmc.PropTypeInt) // 畸形：LastModified 应为 Timestamp
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: "src", CollectedAt: t0, Collections: []hub.Collection{
		{ID: "44444444-4444-4444-4444-444444444444", Name: "夹一", BeatmapMD5Hashes: []string{"m1"}, LastModified: t0},
		{ID: "55555555-5555-5555-5555-555555555555", Name: "夹二", BeatmapMD5Hashes: []string{"m2"}, LastModified: t0},
	}})
	plan, err := BuildPlan(ctx, h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	runner := NewRunner(Deps{Hub: h})
	report, err := runner.Deploy(ctx, plan, nil)
	if err == nil {
		t.Fatal("畸形列库投放应失败")
	}
	if !strings.Contains(err.Error(), "TIMESTAMP") && !strings.Contains(err.Error(), "LastModified") {
		t.Errorf("失败原因不含列类型线索: %v", err)
	}
	if len(report.FailedBatches) != 1 || len(report.FailedBatches[0].Items) != 2 {
		t.Fatalf("FailedBatches = %+v（期望 1 批 2 项）", report.FailedBatches)
	}
	if len(report.NotRun) != 0 {
		t.Errorf("单批计划 NotRun 应为空: %+v", report.NotRun)
	}
	// 整批回滚：目标库零行。
	r, err := lib.OpenDynamicReadOnly(ref.RealmPath)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	if n := objectCountOf(t, r); n != 0 {
		t.Errorf("回滚后行数 = %d（期望 0）", n)
	}
	_, ok := readCollection(t, r, "夹一")
	if ok {
		t.Error("回滚后不应存在「夹一」")
	}
}

// TestDeployTombstoneRestore 恢复组流程（HB2/§5.4 删除不复活）：
// 默认计划排除 tombstone 命中项；显式勾选 → 投放成功 → tombstone 同步清除。
func TestDeployTombstoneRestore(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	ref := buildFullLib(t, lib, realmc.PropTypeTimestamp)
	h := openMemHub(t)

	revive := hub.Collection{
		ID: "66666666-6666-6666-6666-666666666666", Name: "复活夹",
		BeatmapMD5Hashes: []string{"md5-r"}, LastModified: t0,
	}
	applySnap(t, h, hub.Snapshot{ForkID: "src", CollectedAt: t0, Collections: []hub.Collection{revive}})
	// 目标 fork 曾有「复活夹」后删除（两次快照差集 → tombstone）。
	applySnap(t, h, hub.Snapshot{ForkID: ref.ForkID, CollectedAt: t0, Collections: []hub.Collection{revive}})
	applySnap(t, h, hub.Snapshot{ForkID: ref.ForkID, CollectedAt: t0.Add(time.Hour), Collections: nil})
	if got := tombstoneCount(t, h, ref.ForkID, "复活夹"); got != 1 {
		t.Fatalf("布置失败：tombstone 计数 = %d（期望 1）", got)
	}

	// 默认计划：恢复组排除。
	planDefault, err := BuildPlan(ctx, h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan(默认): %v", err)
	}
	if len(planDefault.Items) != 0 {
		t.Fatalf("默认计划 = %+v（期望空：唯一项命中 tombstone）", planDefault.Items)
	}

	// 显式勾选 → 投放 → tombstone 清除 + 目标库复活。
	plan, err := BuildPlan(ctx, h, ref, BuildOptions{RestoreKeys: []string{"复活夹"}})
	if err != nil {
		t.Fatalf("BuildPlan(勾选): %v", err)
	}
	report, err := NewRunner(Deps{Hub: h}).Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if len(report.Applied) != 1 {
		t.Fatalf("Applied = %+v（期望 1 项 create）", report.Applied)
	}
	if got := tombstoneCount(t, h, ref.ForkID, "复活夹"); got != 0 {
		t.Errorf("投放成功后 tombstone 仍存在（%d 行），期望同步清除", got)
	}
	r, err := lib.OpenDynamicReadOnly(ref.RealmPath)
	if err != nil {
		t.Fatalf("重开: %v", err)
	}
	defer r.Close()
	if err := r.BeginRead(); err != nil {
		t.Fatalf("BeginRead: %v", err)
	}
	assertCollectionEqual(t, r, revive)
}

func tombstoneCount(t *testing.T, h *hub.Store, forkID, key string) int {
	t.Helper()
	var n int
	if err := h.DB().QueryRow(
		`SELECT COUNT(*) FROM tombstone WHERE fork_id=? AND kind=? AND content_key=?`,
		forkID, hub.KindCollection, key).Scan(&n); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("查 tombstone: %v", err)
	}
	return n
}

// TestDeployOccupiedRejected 占用拒写（S5）：跨进程持有目标库写会话时，
// 投放在非阻塞占用探测处被拒（ErrTargetOccupied），目标库零变更、零备份。
func TestDeployOccupiedRejected(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	ref := buildFullLib(t, lib, realmc.PropTypeTimestamp)
	h := openMemHub(t)
	applySnap(t, h, hub.Snapshot{ForkID: "src", CollectedAt: t0, Collections: []hub.Collection{
		{ID: "77777777-7777-7777-7777-777777777777", Name: "占用夹", BeatmapMD5Hashes: []string{"m0"}, LastModified: t0},
	}})

	// 子进程持有写会话（realm 会话存在即占 .lock flock——本包施工探针实证）。
	cmd := exec.Command(os.Args[0], "-test.run=^TestHoldRealmHelper$", "-test.v", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "LZSYNC_HOLD_REALM="+ref.RealmPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("管道: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动持锁子进程: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			cmd.Process.Kill() //nolint:errcheck
			cmd.Wait()         //nolint:errcheck
		}
	})
	if !waitOutput(t, stdout, "READY", 15*time.Second) {
		t.Fatal("持锁子进程未就绪")
	}

	plan, err := BuildPlan(ctx, h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	report, err := NewRunner(Deps{Hub: h}).Deploy(ctx, plan, nil)
	if !errors.Is(err, ErrTargetOccupied) {
		t.Fatalf("Deploy 错误 = %v，期望 ErrTargetOccupied", err)
	}
	if len(report.Applied) != 0 || report.BackupPath != "" {
		t.Errorf("占用被拒后不应有落地/备份: %+v", report)
	}
	// 子进程退出后库可正常投放（探测不残留）。kill 与内核回收其 fd 之间
	// 存在时序窗口，短轮询等待锁释放（施工期实证：完整 open→close/kill
	// 路径的锁都会释放，仅非同步即时）。
	cmd.Process.Kill() //nolint:errcheck
	cmd.Wait()         //nolint:errcheck
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := probeOccupied(ref.RealmPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kill 持锁子进程后 5 秒探测仍失败（锁未释放）")
		}
		time.Sleep(100 * time.Millisecond)
	}
	plan2, err := BuildPlan(ctx, h, ref, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan#2: %v", err)
	}
	if _, err := NewRunner(Deps{Hub: h}).Deploy(ctx, plan2, nil); err != nil {
		t.Fatalf("锁释放后投放应成功: %v", err)
	}
}

// TestHoldRealmHelper 占用测试的持锁子进程（LZSYNC_HOLD_REALM 指定库路径；
// 常规测试轮无该环境变量，直接 Skip）。
func TestHoldRealmHelper(t *testing.T) {
	path := os.Getenv("LZSYNC_HOLD_REALM")
	if path == "" {
		t.Skip("helper：仅由占用测试以子进程方式拉起")
	}
	lib, err := realmc.Load()
	if err != nil {
		fmt.Fprint(os.Stderr, "LOADFAIL")
		os.Exit(2)
	}
	r, err := lib.OpenDynamicReadWrite(path)
	if err != nil {
		fmt.Fprint(os.Stderr, "OPENFAIL")
		os.Exit(3)
	}
	defer r.Close()
	if err := r.BeginWrite(); err != nil {
		fmt.Fprint(os.Stderr, "WRITEFAIL")
		os.Exit(4)
	}
	fmt.Print("READY")
	time.Sleep(60 * time.Second)
	r.Rollback() //nolint:errcheck // 子进程收尾
}

// waitOutput 从管道读直到包含标记或超时。
func waitOutput(t *testing.T, r io.Reader, marker string, timeout time.Duration) bool {	t.Helper()
	done := make(chan bool, 1)
	go func() {
		buf := make([]byte, 4096)
		var acc strings.Builder
		for {
			n, err := r.Read(buf)
			if n > 0 {
				acc.Write(buf[:n])
				if strings.Contains(acc.String(), marker) {
					done <- true
					return
				}
			}
			if err != nil {
				done <- false
				return
			}
		}
	}()
	select {
	case ok := <-done:
		return ok
	case <-time.After(timeout):
		return false
	}
}
