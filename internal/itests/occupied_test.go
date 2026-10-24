//go:build !windows

package itests

// 占用拦截断言（design §1.1-S5 / §7-M1-7）：目标库 .lock 被排他 flock
// 持有（= 活动的 realm 会话占用，deploy 的探测语义见 lock_unix.go 实证）
// → Deploy 被拒（ErrTargetOccupied）、目标库零变更、零备份；锁释放后
// 同一计划可正常投放。flock 在测试进程内以独立 fd 注入——flock 排他锁
// 关联 open file description，同进程异 fd 亦互斥，与跨进程持有等价。

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"lzsync/internal/deploy"
	"lzsync/internal/hub"
)

func TestDeployRejectedWhenOccupied(t *testing.T) {
	lib := mustLib(t)
	ctx := context.Background()
	e := newEnv(t)

	fav := hub.Collection{
		ID: "77777777-7777-7777-7777-777777777777", Name: "占用夹",
		BeatmapMD5Hashes: []string{"md5-o"}, LastModified: t0,
	}
	srcRef := buildFullLib(t, lib, "lazer")
	seedFullCollection(t, lib, srcRef.RealmPath, fav)
	e.collectInto(srcRef)
	dstRef := buildFullLib(t, lib, "delta")

	// 注入占用：持 .lock 排他 flock（buildFullLib 的建库会话已生成 .lock）。
	lockPath := dstRef.RealmPath + ".lock"
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("前置失败：.lock 未生成（建库会话应已创建）: %v", err)
	}
	lockF, err := os.OpenFile(lockPath, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("打开 .lock: %v", err)
	}
	defer lockF.Close()
	if err := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("持占 flock: %v", err)
	}
	release := func() { syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN) } //nolint:errcheck

	plan, err := deploy.BuildPlan(ctx, e.hub, dstRef, deploy.BuildOptions{})
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	occupiedBytes := sha256OfFile(t, dstRef.RealmPath) // 零变更断言的基准
	report, err := e.deployRunner().Deploy(ctx, plan, nil)
	if !errors.Is(err, deploy.ErrTargetOccupied) {
		t.Fatalf("Deploy 错误 = %v，期望 ErrTargetOccupied", err)
	}
	if len(report.Applied) != 0 || report.BackupPath != "" {
		t.Errorf("占用被拒后不应有落地/备份: %+v", report)
	}
	// 零变更断言用文件字节（占用持有期间 realm_open 会阻塞抢锁——不能
	// 以只读会话重开验证）。
	if got := sha256OfFile(t, dstRef.RealmPath); got != occupiedBytes {
		t.Error("占用被拒后目标库字节被改动")
	}

	// 释放占用 → 同一计划正常投放。
	release()
	report2, err := e.deployRunner().Deploy(ctx, plan, nil)
	if err != nil {
		t.Fatalf("锁释放后投放应成功: %v", err)
	}
	if len(report2.Applied) != 1 {
		t.Errorf("锁释放后 Applied = %+v，期望 1", report2.Applied)
	}
	assertCollectionEqual(t, lib, dstRef.RealmPath, fav)
}
