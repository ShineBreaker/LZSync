package forks

import "testing"

// 数据断言全部锚定调研摘录：fork-comparison §1.1（目录/文件名/版本矩阵）、
// fork-delta §1–§3、fork-ez2lazer §1–§2、fork-g0v0 §1–§3。

func TestAllRegistersFourForks(t *testing.T) {
	all := All()
	if len(all) != 4 {
		t.Fatalf("All() 返回 %d 份 Profile，期望 4", len(all))
	}
	seenID := map[string]bool{}
	seenDir := map[string]bool{}
	for _, p := range all {
		if seenID[p.ID] {
			t.Errorf("Profile ID 重复: %s", p.ID)
		}
		if seenDir[p.DataDirName] {
			t.Errorf("DataDirName 重复: %s", p.DataDirName)
		}
		seenID[p.ID] = true
		seenDir[p.DataDirName] = true
		if p.DecodeVersion == nil {
			t.Errorf("%s: DecodeVersion 为 nil", p.ID)
		}
		if p.ServerRealm == "" {
			t.Errorf("%s: ServerRealm 为空", p.ID)
		}
		if len(p.IniKeys.Locked) == 0 {
			t.Errorf("%s: IniKeys.Locked 为空（S10 红灯键缺失）", p.ID)
		}
	}
	for _, id := range []string{"lazer", "delta", "ez2lazer", "g0v0"} {
		if !seenID[id] {
			t.Errorf("缺少四端之一的 Profile: %s", id)
		}
	}
	// 拷贝语义：改写 All() 结果不得影响注册表。
	all[0].DataDirName = "tampered"
	if again := All(); again[0].DataDirName == "tampered" {
		t.Error("All() 返回了注册表本体，改写外泄")
	}
}

func TestProfileLookup(t *testing.T) {
	for _, id := range []string{"lazer", "delta", "ez2lazer", "g0v0"} {
		if _, ok := Profile(id); !ok {
			t.Errorf("Profile(%q) 不存在", id)
		}
	}
	if _, ok := Profile("nope"); ok {
		t.Error(`Profile("nope") 不应存在`)
	}
}

func TestProfileStaticData(t *testing.T) {
	tests := []struct {
		id            string
		dataDir       string
		realmPat      string
		processes     []string
		serverRealm   string
		diskVersion   uint64
		extTableCount int
		extColCount   int
		ownRulesets   []string
		ownIniKeys    []string
	}{
		{
			id: "lazer", dataDir: "osu", realmPat: "client.realm",
			processes: []string{"osu!", "osu"}, serverRealm: "ppy", diskVersion: 52,
			extTableCount: 0, extColCount: 0,
		},
		{
			id: "delta", dataDir: "deltalazer", realmPat: "client.realm",
			// 进程名未记录（fork-comparison U13），名单留空由用户扩展（§6.4）。
			processes: nil, serverRealm: "ppy", diskVersion: 51,
			extTableCount: 0, extColCount: 0,
			ownIniKeys: []string{
				"MenuCursorFromSkin", "MenuCursorFromSkinConfirmed", "MenuCursorFromSkinSize",
				"MenuCursorTrail", "MenuCursorScaleX", "MenuCursorScaleY",
			},
		},
		{
			id: "ez2lazer", dataDir: "osu-Ez2Lazer", realmPat: "client_<diskVersion>.realm",
			processes: []string{"Ez2Lazer"}, serverRealm: "ppy", diskVersion: 52011,
			extTableCount: 7, extColCount: 10,
			ownRulesets: []string{"bms"},
		},
		{
			id: "g0v0", dataDir: "g0v0", realmPat: "client.realm",
			processes: []string{"g0v0!"}, serverRealm: "g0v0.top", diskVersion: 52,
			extTableCount: 0, extColCount: 0,
			ownIniKeys: []string{
				"UseRulesetSkins", "RulesetSkins", "UseV2ResultsScreen",
				"ToriiLegacyFooterUseSkin", "ToriiLegacySongSelectFooter", "ToriiStableResults",
				"ToriiLegacyFont", "UnslantedSongSelectUI", "CustomApiUrl",
				"DisableAutomaticUpdates", "HitsoundVolume",
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			p, ok := Profile(tc.id)
			if !ok {
				t.Fatalf("Profile(%q) 缺失", tc.id)
			}
			if p.DataDirName != tc.dataDir {
				t.Errorf("DataDirName = %q, 期望 %q", p.DataDirName, tc.dataDir)
			}
			if p.RealmFilePat != tc.realmPat {
				t.Errorf("RealmFilePat = %q, 期望 %q", p.RealmFilePat, tc.realmPat)
			}
			if len(p.ProcessNames) != len(tc.processes) {
				t.Errorf("ProcessNames = %v, 期望 %v", p.ProcessNames, tc.processes)
			} else {
				for i, want := range tc.processes {
					if p.ProcessNames[i] != want {
						t.Errorf("ProcessNames[%d] = %q, 期望 %q", i, p.ProcessNames[i], want)
					}
				}
			}
			if p.ServerRealm != tc.serverRealm {
				t.Errorf("ServerRealm = %q, 期望 %q", p.ServerRealm, tc.serverRealm)
			}
			if p.DiskVersion != tc.diskVersion {
				t.Errorf("DiskVersion = %d, 期望 %d", p.DiskVersion, tc.diskVersion)
			}
			if len(p.ExtTables) != tc.extTableCount {
				t.Errorf("ExtTables 数 = %d, 期望 %d（%v）", len(p.ExtTables), tc.extTableCount, p.ExtTables)
			}
			if got := extColumnCount(p); got != tc.extColCount {
				t.Errorf("ExtColumns 总列数 = %d, 期望 %d", got, tc.extColCount)
			}
			if !equalStrings(p.OwnRulesets, tc.ownRulesets) {
				t.Errorf("OwnRulesets = %v, 期望 %v", p.OwnRulesets, tc.ownRulesets)
			}
			if !equalStrings(p.IniKeys.Own, tc.ownIniKeys) {
				t.Errorf("IniKeys.Own = %v, 期望 %v", p.IniKeys.Own, tc.ownIniKeys)
			}
		})
	}
}

// TestEzStructureData Ez 的 7 张专有表与 10 个追加列逐名核对
//（fork-ez2lazer §2.4/§2.5/§7A 字段级摘录）。
func TestEzStructureData(t *testing.T) {
	p, _ := Profile("ez2lazer")
	wantTables := []string{
		"EzBeatmapSkillValue", "EzPlayerSkillValue", "EzDanEstimate",
		"EzPlayerSkillHistoryPoint", "EzBeatmapChartSkillInfo",
		"EzPlayerDanSkillsetValue", "EzBeatmapChartDan",
	}
	if !equalStrings(p.ExtTables, wantTables) {
		t.Errorf("ExtTables = %v, 期望 %v", p.ExtTables, wantTables)
	}
	wantCols := map[string][]string{
		"Beatmap":    {"XxyStarRating", "PerformancePoints", "HasVideo", "HasStoryboard"},
		"Score":      {"JudgeMode", "HealthMode"},
		"BeatmapSet": {"ExternalContentRoot", "HostingKind"},
		"Ruleset":    {"LastAppliedXxySrVersion", "LastAppliedManiaSkillVersion"},
	}
	if len(p.ExtColumns) != len(wantCols) {
		t.Fatalf("ExtColumns 表数 = %d, 期望 %d", len(p.ExtColumns), len(wantCols))
	}
	for table, cols := range wantCols {
		if !equalStrings(p.ExtColumns[table], cols) {
			t.Errorf("ExtColumns[%s] = %v, 期望 %v", table, p.ExtColumns[table], cols)
		}
	}
}

// TestDecodeVersion Ez 版本编解码（52011 = 52×1000+11）与三端恒等解码，
// 表驱动覆盖四端识别的版本面（fork-ez2lazer §2.1）。
func TestDecodeVersion(t *testing.T) {
	ez, _ := Profile("ez2lazer")
	lazer, _ := Profile("lazer")
	tests := []struct {
		name     string
		decode   func(uint64) (int, int)
		in       uint64
		upstream int
		forkRev  int
	}{
		{"ez/52011", ez.DecodeVersion, 52011, 52, 11},
		{"ez/51011", ez.DecodeVersion, 51011, 51, 11},
		{"ez/52019", ez.DecodeVersion, 52019, 52, 19},
		{"ez/1000边界", ez.DecodeVersion, 1000, 1, 0},
		{"ez/999纯上游", ez.DecodeVersion, 999, 999, 0},
		{"ez/52纯上游", ez.DecodeVersion, 52, 52, 0},
		{"lazer/52", lazer.DecodeVersion, 52, 52, 0},
		{"lazer/51", lazer.DecodeVersion, 51, 51, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up, rev := tc.decode(tc.in)
			if up != tc.upstream || rev != tc.forkRev {
				t.Errorf("decode(%d) = (%d, %d), 期望 (%d, %d)", tc.in, up, rev, tc.upstream, tc.forkRev)
			}
		})
	}
	if got := EncodeEzVersion(52, 11); got != 52011 {
		t.Errorf("EncodeEzVersion(52, 11) = %d, 期望 52011", got)
	}
	if up, rev := ez.DecodeVersion(EncodeEzVersion(53, 12)); up != 53 || rev != 12 {
		t.Errorf("编解码往返失败: got (%d, %d), 期望 (53, 12)", up, rev)
	}
}

func extColumnCount(p ForkProfile) int {
	n := 0
	for _, cols := range p.ExtColumns {
		n += len(cols)
	}
	return n
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
