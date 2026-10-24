package inisettings

// 白名单策略验收：共有集口径（119 键）、红灯硬锁（S10 断言「不可被
// 勾选」的数据面）、per-fork Own 排除（fork-comparison D12/D14/D15）。

import (
	"testing"

	"lzsync/internal/forks"
)

func TestSharedKeysCount(t *testing.T) {
	// 官方 OsuSetting 122 键 − delta 缺 3 键（D12）= 119。
	if got := len(SharedKeys()); got != 119 {
		t.Fatalf("共有键数 = %d，期望 119", got)
	}
	for _, missing := range []string{"SongSelectCollectionFilter", "HitsoundVolume", "PMFriendsOnly"} {
		if IsShared(missing) {
			t.Errorf("delta 缺键 %q 不应在共有集（D12）", missing)
		}
	}
	for _, k := range []string{"MenuCursorFromSkin", "CustomApiUrl", "EzSomething"} {
		if IsShared(k) {
			t.Errorf("fork 专有/未知键 %q 不应在共有集", k)
		}
	}
}

func TestRedLightLockedAllCombinations(t *testing.T) {
	// S10 红灯键在任何收集/同步入口都不可被勾选：四端全部 16 组合断言。
	profiles := forks.All()
	if len(profiles) != 4 {
		t.Fatalf("fork 注册表 = %d 端，期望 4", len(profiles))
	}
	for _, src := range profiles {
		if got := CollectableKeys(src); got["Token"] || got["Username"] || got["ReleaseStream"] {
			t.Errorf("CollectableKeys(%s) 含红灯键（S10）", src.ID)
		}
		for _, dst := range profiles {
			got := SyncableKeys(src, dst)
			for _, locked := range []string{"Token", "Username", "ReleaseStream"} {
				if got[locked] {
					t.Errorf("SyncableKeys(%s→%s) 含红灯键 %q（S10 硬锁失效）", src.ID, dst.ID, locked)
				}
			}
		}
		if !IsRedLight(src, "Token") || !IsRedLight(src, "Username") || !IsRedLight(src, "ReleaseStream") {
			t.Errorf("IsRedLight(%s) 未覆盖四端一致红灯三键", src.ID)
		}
	}
}

func TestSyncableKeysExcludesTargetOwn(t *testing.T) {
	// delta 专有 6 键（fork-delta §3.1）不进任何同步面；典型白名单键在。
	delta, ok := forks.Profile("delta")
	if !ok {
		t.Fatal("delta Profile 缺失")
	}
	lazer, _ := forks.Profile("lazer")
	sync := SyncableKeys(lazer, delta)
	for _, own := range delta.IniKeys.Own {
		if sync[own] {
			t.Errorf("delta 专有键 %q 不应进入同步面", own)
		}
	}
	for _, want := range []string{"Ruleset", "ShowStoryboard", "ShowFpsDisplay", "SavePassword", "Skin"} {
		if !sync[want] {
			t.Errorf("白名单键 %q 应在 lazer→delta 同步面", want)
		}
	}
}

func TestSyncableKeysUnknownForkDegrades(t *testing.T) {
	// 未注册 fork 的零值 Profile：排除自然为空，共有集照常（投放侧
	// SyncFile 的执行期复核仍是防线）。
	unknown := forks.ForkProfile{ID: "custom"}
	got := CollectableKeys(unknown)
	if len(got) != len(SharedKeys()) {
		t.Fatalf("零值 Profile 收集面 = %d 键，期望 = 共有集 %d", len(got), len(SharedKeys()))
	}
	if IsRedLight(unknown, "Token") {
		t.Error("零值 Profile 无红灯数据，IsRedLight 应为 false（引擎不内置名单）")
	}
}
