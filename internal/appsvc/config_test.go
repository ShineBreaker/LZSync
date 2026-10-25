package appsvc

// 同步范围配置（§0-G1）单测：configStore 直接测试（无 native 依赖）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"lzsync/internal/forks"
)

func TestConfigMissingFileDefaults(t *testing.T) {
	dir := t.TempDir()
	c, err := loadSyncConfig(dir)
	if err != nil {
		t.Fatalf("缺文件载入 = %v，期望 nil", err)
	}
	scope := c.scope()
	if len(scope.Sections) != len(AllSections) {
		t.Fatalf("默认板块数 = %d，期望 %d", len(scope.Sections), len(AllSections))
	}
	for _, tg := range scope.Sections {
		if !tg.Enabled {
			t.Errorf("板块 %s 默认应为开", tg.Section)
		}
	}
	if len(scope.Forks) != 0 {
		t.Errorf("默认端清单 = %d，期望 0", len(scope.Forks))
	}
}

func TestConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	c, err := loadSyncConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	scope := c.scope()
	scope.Forks = append(scope.Forks, ForkRole{ForkID: "lazer", RealmPath: "/x/client.realm", Source: true, Target: false})
	for i := range scope.Sections {
		if scope.Sections[i].Section == SectionScores {
			scope.Sections[i].Enabled = false
		}
	}
	if err := c.applyScope(scope, nil); err != nil {
		t.Fatalf("applyScope: %v", err)
	}
	// 重新载入读回。
	c2, err := loadSyncConfig(dir)
	if err != nil {
		t.Fatalf("重载: %v", err)
	}
	s2 := c2.scope()
	for _, tg := range s2.Sections {
		if tg.Section == SectionScores && tg.Enabled {
			t.Error("成绩板块应为关（持久化）")
		}
	}
	if len(s2.Forks) != 1 || s2.Forks[0].ForkID != "lazer" || !s2.Forks[0].Source || s2.Forks[0].Target {
		t.Fatalf("重载 Forks = %+v", s2.Forks)
	}
}

func TestConfigCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSyncConfig(dir); err == nil {
		t.Fatal("损坏配置应报错（不得静默重置）")
	}
	// 未知板块同样报错。
	b, _ := json.Marshal(syncConfig{Version: 1, Sections: map[Section]bool{"bogus": true}})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSyncConfig(dir); err == nil {
		t.Fatal("未知板块应报错")
	}
}

func TestConfigDiscoverAppendIgnoreAndRegister(t *testing.T) {
	dir := t.TempDir()
	c, err := loadSyncConfig(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ref := forks.LibraryRef{ForkID: "delta", RealmPath: "/d/client.realm"}
	ok, err := c.discoverFork(ref)
	if err != nil || !ok {
		t.Fatalf("discoverFork = (%v,%v)，期望 (true,nil)", ok, err)
	}
	role, _ := c.role("delta")
	if !role.Source || !role.Target {
		t.Fatalf("发现追加默认双角色 = %+v", role)
	}
	// 重扫幂等：不重复追加、不重置角色。
	c.mu.Lock()
	for i := range c.cfg.Forks {
		if c.cfg.Forks[i].ForkID == "delta" {
			c.cfg.Forks[i].Target = false
		}
	}
	c.mu.Unlock()
	ok, _ = c.discoverFork(ref)
	if !ok {
		t.Fatal("已配置端应仍进注册表")
	}
	role, _ = c.role("delta")
	if role.Target {
		t.Fatal("已配置端的用户角色不应被 discover 重置")
	}

	// Remove → ignored；重新 discover 不加回。
	if err := c.removeFork("delta", "/d/client.realm"); err != nil {
		t.Fatalf("removeFork: %v", err)
	}
	if _, ok := c.role("delta"); ok {
		t.Fatal("移除后角色应不存在")
	}
	ok, _ = c.discoverFork(ref)
	if ok {
		t.Fatal("ignored 路径的发现不应再加回")
	}
	if _, ok := c.role("delta"); ok {
		t.Fatal("ignored 路径不应重进配置")
	}
	// Register 同路径：摘除 ignored 并恢复默认双角色。
	if err := c.registerFork(ref); err != nil {
		t.Fatalf("registerFork: %v", err)
	}
	role, ok2 := c.role("delta")
	if !ok2 || !role.Source || !role.Target {
		t.Fatalf("Register 后角色 = (%+v,%v)，期望默认双角色", role, ok2)
	}
	if c.isIgnored("/d/client.realm") {
		t.Fatal("Register 后路径不应留在 ignored")
	}
	// 盘面持久：重载读回 ignored 清空与条目。
	c3, err := loadSyncConfig(dir)
	if err != nil {
		t.Fatalf("重载: %v", err)
	}
	if _, ok := c3.role("delta"); !ok {
		t.Fatal("重载后 delta 应在配置中")
	}
}
