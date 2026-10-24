package guard

// 单测：进程守卫（Linux 实测；darwin/windows 枚举分支仅交叉编译验证）、
// 时间戳备份（同名不覆盖、还原前安全备份、原子替换）、mtime 戳。

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGuardClearWhenNotListed(t *testing.T) {
	g := NewGuard([]string{"definitely-not-running-process-xyz"})
	if got := g.Running(); len(got) != 0 {
		t.Fatalf("Running() = %v，期望空（名单进程未运行）", got)
	}
	if err := g.EnsureClear(); err != nil {
		t.Fatalf("EnsureClear() = %v，期望 nil", err)
	}
}

func TestGuardDetectsOwnProcess(t *testing.T) {
	// go test 进程自身的可执行名（Linux：comm 与 argv[0] 基名一致），
	// 是「名单内进程正在运行」的最小可复现夹具。
	self := filepath.Base(os.Args[0])
	g := NewGuard([]string{self})
	if got := g.Running(); len(got) != 1 || got[0] != self {
		t.Fatalf("Running() = %v，期望 [%s]", got, self)
	}
	err := g.EnsureClear()
	if !errors.Is(err, ErrGameRunning) {
		t.Fatalf("EnsureClear() = %v，期望 ErrGameRunning", err)
	}
	if !strings.Contains(err.Error(), self) {
		t.Errorf("错误消息 %q 应携带运行中进程名 %s", err.Error(), self)
	}
}

func TestGuardEmptyListNeverBlocks(t *testing.T) {
	g := NewGuard(nil)
	if got := g.Running(); got != nil {
		t.Fatalf("Running() = %v，期望 nil（空名单恒放行）", got)
	}
}

func TestBackupsBackupFileAndNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenBackups(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("OpenBackups: %v", err)
	}
	src := filepath.Join(dir, "client.realm")
	if err := os.WriteFile(src, []byte("payload-v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 固定时钟：第二次备份必然生成同名文件 → 必须报错且原件不动。
	defer func() { nowFunc = time.Now }()
	nowFunc = func() time.Time { return time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC) }

	p1, err := b.BackupFile(src)
	if err != nil {
		t.Fatalf("BackupFile: %v", err)
	}
	if filepath.Base(p1) != "client.realm.20261024T100000.000Z" {
		t.Errorf("备份名 = %q，期望时间戳形态 client.realm.20261024T100000.000Z", filepath.Base(p1))
	}
	got, err := os.ReadFile(p1)
	if err != nil || string(got) != "payload-v1" {
		t.Fatalf("备份内容 = %q, %v，期望 payload-v1", got, err)
	}

	// 同名不覆盖（S2）。
	if _, err := b.BackupFile(src); err == nil {
		t.Fatal("同名备份第二次调用应报错（绝不覆盖）")
	} else if !strings.Contains(err.Error(), "绝不覆盖") {
		t.Errorf("错误消息 %q 应说明绝不覆盖语义", err)
	}
	// 原备份内容未被触碰。
	got, _ = os.ReadFile(p1)
	if string(got) != "payload-v1" {
		t.Errorf("首份备份被改写: %q", got)
	}

	// 时钟推进 1ms → 新名，成功。
	nowFunc = func() time.Time { return time.Date(2026, 10, 24, 10, 0, 0, int(time.Millisecond), time.UTC) }
	if _, err := b.BackupFile(src); err != nil {
		t.Fatalf("毫秒推进后的备份应成功: %v", err)
	}
}

func TestBackupsRestoreMakesSafetyBackupAndReplaces(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenBackups(filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatalf("OpenBackups: %v", err)
	}
	src := filepath.Join(dir, "client.realm")
	if err := os.WriteFile(src, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	nowFunc = func() time.Time { return time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC) }
	bp, err := b.BackupFile(src)
	if err != nil {
		t.Fatal(err)
	}

	// 目标漂移后还原：还原前应自动为漂移后状态留安全备份（§5.2）。
	if err := os.WriteFile(src, []byte("drifted"), 0o644); err != nil {
		t.Fatal(err)
	}
	nowFunc = func() time.Time { return time.Date(2026, 10, 24, 11, 0, 0, 0, time.UTC) }
	if err := b.Restore(bp, src); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got, err := os.ReadFile(src)
	if err != nil || string(got) != "before" {
		t.Fatalf("还原后内容 = %q, %v，期望 before", got, err)
	}
	// 安全备份存在且为漂移后内容。
	safety := filepath.Join(dir, "backups", "client.realm.20261024T110000.000Z")
	sg, err := os.ReadFile(safety)
	if err != nil || string(sg) != "drifted" {
		t.Fatalf("还原前安全备份 = %q, %v，期望 drifted", sg, err)
	}
}

func TestBackupsRestoreMissingTarget(t *testing.T) {
	dir := t.TempDir()
	b, _ := OpenBackups(filepath.Join(dir, "backups"))
	src := filepath.Join(dir, "client.realm")
	os.WriteFile(src, []byte("x"), 0o644)
	bp, err := b.BackupFile(src)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere.realm")
	if err := b.Restore(bp, target); err != nil {
		t.Fatalf("还原到不存在目标应直接落位: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "x" {
		t.Errorf("还原内容 = %q，期望 x", got)
	}
}

func TestTouchAfterWrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "client.realm")
	os.WriteFile(p, []byte("x"), 0o644)
	past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return want }
	if err := TouchAfterWrite(p); err != nil {
		t.Fatalf("TouchAfterWrite: %v", err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(want) {
		t.Errorf("mtime = %v，期望 %v", st.ModTime(), want)
	}
}
