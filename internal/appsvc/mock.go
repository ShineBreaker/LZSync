package appsvc

// Mock 后端（M-4：ezrealmsync-design 的 --ui-test 同款结构）：四服务全
// 内存态，不触碰任何文件系统与 native 库，供无 native 库环境开发与冒烟。
// 数据刻意覆盖 UI 的全部呈现分支：四端卡片（含 Ez 版本解码与「未知
// （运行中）」降级）、五类差异徽标（含恢复组与弱键疑似）、守卫拦截
//（g0v0 模拟运行中）、批进度与取消。

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/guard"
	"lzsync/internal/hub"
)

// mockSleep 步进间隔：足够肉眼观察进度条，又不拖慢冒烟。
const mockSleep = 40 * time.Millisecond

type mockBackend struct {
	lib   *mockLibrary
	cmp   *mockCompare
	sync  *mockSync
	exp   *mockExport
	mode  string
	reasn string

	mu       sync.Mutex
	scope    *SyncScope      // 内存态同步范围（M-4 冒烟，不落盘）
	kept     map[string]bool // KeepDifference 已「接受差异」的 kind\x00key（ListConflicts 过滤）
	baseline string          // 内存态基准端（演示）
}

func newMockBackend(mode, reason string, prog ProgressSink) *Backend {
	if prog == nil {
		prog = func(ProgressEvent) {}
	}
	cmp := &mockCompare{createdAt: time.Now()}
	b := &mockBackend{
		lib:  &mockLibrary{},
		cmp:  cmp,
		exp:  &mockExport{},
		mode: mode, reasn: reason,
	}
	cmp.b = b
	sync := &mockSync{prog: prog, b: b, runs: map[string]*mockRun{}}
	sync.cmp = cmp
	b.sync = sync
	return &Backend{
		Library:  b.lib,
		Compare:  cmp,
		Sync:     sync,
		Export:   b.exp,
		Settings: b,
		Mode:     mode,
		Reason:   reason,
	}
}

// --- 容器导出（§7-M3-5 路线④ Mock：只演示分支，不触碰文件系统与进程） ---

type mockExport struct{}

func (m *mockExport) ExportContainers(forkID string, kind hub.EntityKind, contentKeys []string) (ExportResult, error) {
	if forkID == "" {
		return ExportResult{}, errors.New("appsvc: 导出需要目标 fork")
	}
	ext := ""
	switch kind {
	case hub.KindBeatmapSet:
		ext = ".osz"
	case hub.KindSkin:
		ext = ".osk"
	case hub.KindScore:
		ext = ".osr"
	default:
		return ExportResult{}, fmt.Errorf("appsvc: %s 无容器形态（仅谱面集/皮肤/成绩可导出，§7-M3-5）", kind)
	}
	if len(contentKeys) == 0 {
		return ExportResult{}, errors.New("appsvc: 未指定要导出的内容键")
	}
	paths := make([]string, 0, len(contentKeys))
	for i, k := range contentKeys {
		paths = append(paths, fmt.Sprintf("/mock/export/lzsync-export-mock/%02d-%s%s", i+1, shortKey(k), ext))
	}
	return ExportResult{Paths: paths, Note: "Mock 后端仅演示：未生成真实容器、未拉起客户端"}, nil
}

// --- 库管理 ---

type mockLibrary struct {
	mu   sync.Mutex
	refs map[string]forks.LibraryRef // 注册态（可被 Remove）
}

// mockRefs 固定四端样本：Ez 演示版本编码解码，g0v0 演示「运行中 →
// 版本未知」降级链与守卫拦截。
var mockRefs = []forks.LibraryRef{
	{ForkID: "lazer", DataDir: "/mock/.local/share/osu", RealmPath: "/mock/.local/share/osu/client.realm",
		DiskVersion: 52, VersionSource: forks.VersionFromOpen, UpstreamVersion: 52},
	{ForkID: "delta", DataDir: "/mock/.local/share/deltalazer", RealmPath: "/mock/.local/share/deltalazer/client.realm",
		DiskVersion: 51, VersionSource: forks.VersionFromSnapshot, UpstreamVersion: 51},
	{ForkID: "ez2lazer", DataDir: "/mock/.local/share/osu-Ez2Lazer", RealmPath: "/mock/.local/share/osu-Ez2Lazer/client_52011.realm",
		DiskVersion: 52011, VersionSource: forks.VersionFromFilename, UpstreamVersion: 52, ForkRev: 11},
	{ForkID: "g0v0", DataDir: "/mock/.local/share/g0v0", RealmPath: "/mock/.local/share/g0v0/client.realm",
		DiskVersion: 0, VersionSource: forks.VersionUnknownRunning},
}

func (m *mockLibrary) reg() map[string]forks.LibraryRef {
	if m.refs == nil {
		m.refs = map[string]forks.LibraryRef{}
		for _, r := range mockRefs {
			m.refs[r.ForkID] = r
		}
	}
	return m.refs
}

func (m *mockLibrary) Scan() ([]forks.LibraryRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]forks.LibraryRef, 0, len(mockRefs))
	for _, r := range mockRefs {
		m.reg()[r.ForkID] = r
		out = append(out, r)
	}
	return out, nil
}

func (m *mockLibrary) Register(path string) (forks.LibraryRef, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range mockRefs {
		if r.RealmPath == path {
			m.reg()[r.ForkID] = r
			return r, nil
		}
	}
	// 未命中样本：演示识别失败分支（ForkID 空串 + 原始版本，§6.1 指认）。
	return forks.LibraryRef{
		DataDir: "/mock/.local/share/unknown-fork", RealmPath: path,
		DiskVersion: 9999, VersionSource: forks.VersionFromOpen, UpstreamVersion: 9999,
	}, nil
}

func (m *mockLibrary) AssignFork(path, forkID string) (forks.LibraryRef, error) {
	p, ok := forks.Profile(forkID)
	if !ok {
		return forks.LibraryRef{}, fmt.Errorf("appsvc: 未知 fork %q（可选：[lazer delta ez2lazer g0v0]）", forkID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ref := forks.LibraryRef{
		ForkID: forkID, DataDir: "/mock/.local/share/" + p.DataDirName, RealmPath: path,
		DiskVersion: 9999, VersionSource: forks.VersionFromOpen,
	}
	ref.UpstreamVersion, ref.ForkRev = p.DecodeVersion(ref.DiskVersion)
	m.reg()[forkID] = ref
	return ref, nil
}

func (m *mockLibrary) List() []LibraryCard {
	m.mu.Lock()
	refs := make([]forks.LibraryRef, 0, len(m.reg()))
	for _, r := range m.reg() {
		refs = append(refs, r)
	}
	m.mu.Unlock()
	cards := make([]LibraryCard, 0, len(refs))
	for i, ref := range refs {
		card := LibraryCard{
			ForkID: ref.ForkID, DataDir: ref.DataDir, RealmPath: ref.RealmPath,
			DiskVersion: ref.DiskVersion, VersionSource: ref.VersionSource,
			UpstreamVersion: ref.UpstreamVersion, ForkRev: ref.ForkRev,
			VersionLabel: versionLabel(ref), Registered: true,
			FilesDir: ref.DataDir + "/files", RunningNames: []string{},
		}
		card.RealmSize = int64(1_048_576 + i*262_144)
		card.FilesSize = int64(3_221_225_472 + int64(i)*512_000_000)
		card.FilesCount = 4200 + i*1337
		if ref.ForkID == "g0v0" {
			card.ProcessRunning = true // 演示运行状态灯与警示条
			card.RunningNames = []string{"g0v0!"}
		}
		cards = append(cards, card)
	}
	return cards
}

func (m *mockLibrary) Remove(forkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reg()[forkID]; !ok {
		return fmt.Errorf("appsvc: 未注册的 fork %q", forkID)
	}
	delete(m.reg(), forkID)
	return nil
}

// --- 对比预览 ---

type mockCompare struct {
	mu        sync.Mutex
	plan      deploy.Plan
	createdAt time.Time
	b         *mockBackend // 同步范围（Target 角色）与 kept 集（装配后回填）
}

// mockPreviewRows hub → fork 投放预演样本（对 target=delta）：覆盖五类
// 徽标、八类 kind（谱面集/难度/收藏/皮肤/成绩 + M3 设置类三类；难度不产
// 独立对账行——难度生命周期挂集整集管理，§4.2，Mock 不伪造该数据面）
// 与默认勾选规则。
func mockPreviewRows() []DiffRow {
	return []DiffRow{
		{Kind: hub.KindCollection, ContentKey: "新谱面收藏", Category: CategoryCreate,
			Summary: "收藏夹「新谱面收藏」", DefaultChecked: true},
		{Kind: hub.KindCollection, ContentKey: "常用练习", Category: CategoryConflict,
			Summary: "收藏夹「常用练习」", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "BeatmapMD5Hashes",
				HubValue: "md5-a,md5-b,md5-c", ForkValue: "md5-a,md5-b"}}},
		{Kind: hub.KindCollection, ContentKey: "旧收藏", Category: CategoryRestore,
			Summary: "收藏夹「旧收藏」", DefaultChecked: false,
			DeletedAt: time.Now().Add(-72 * time.Hour).UnixMilli()},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-new-set-0001", Category: CategoryCreate,
			Summary: "Cranky - Terpsichore（Reol）", DefaultChecked: true},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-old-ver-0002", Category: CategoryVersionUpdate,
			Summary: "ginkiha - EOS（kamome sano）", IdentityKey: "online:1656406",
			CurrentKey: "hash-eos-new-0002", Evidence: EvidenceStrong, DefaultChecked: true},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-weak-0003", Category: CategoryVersionUpdate,
			Summary: "Unknown Artist - Demo（mapper）", IdentityKey: "meta:UA|Demo|mapper",
			CurrentKey: "hash-demo-new-0003", Evidence: EvidenceWeak, DefaultChecked: false,
			SkipReason: "元数据弱键「疑似」版本更新，需用户拍板（§5.4 三选一）"},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-conflict-0004", Category: CategoryConflict,
			Summary: "IA - Suna no Wakusei（nhato）", DefaultChecked: true,
			ConflictFields: []FieldDiff{
				{Field: "StatusInt", HubValue: "1", ForkValue: "0"},
				{Field: "DateAdded", HubValue: "1769000000000", ForkValue: "1768000000000"},
			}},
		{Kind: hub.KindSkin, ContentKey: "hash-skin-new-0005", Category: CategoryCreate,
			Summary: "皮肤「Argon（modified）」（peppy）", DefaultChecked: true},
		{Kind: hub.KindSkin, ContentKey: "hash-skin-conflict-0006", Category: CategoryConflict,
			Summary: "皮肤「Custom HD」（作者甲）", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "Name", HubValue: "Custom HD", ForkValue: "Custom HD（旧名）"}}},
		{Kind: hub.KindSkin, ContentKey: "hash-skin-deleted-0007", Category: CategoryRestore,
			Summary: "皮肤「Retired Skin」（作者乙）", DefaultChecked: false,
			DeletedAt: time.Now().Add(-120 * time.Hour).UnixMilli()},
		{Kind: hub.KindScore, ContentKey: "bh-new|sc-0008", Category: CategoryCreate,
			Summary: "成绩 osu · 987654 分 · 2026-02-01", DefaultChecked: true},
		{Kind: hub.KindScore, ContentKey: "bh-old|sc-0009", Category: CategoryConflict,
			Summary: "成绩 taiko · 123456 分 · 2026-01-15", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "TotalScore", HubValue: "123456", ForkValue: "123450"}}},
		{Kind: hub.KindScore, ContentKey: "bh-gone|sc-0010", Category: CategoryRestore,
			Summary: "成绩 fruits · 4242 分 · 2025-12-30", DefaultChecked: false,
			DeletedAt: time.Now().Add(-24 * time.Hour).UnixMilli()},
		// M3 设置类三类（§6.2 八类 Tab 数据面；摘要形态对齐 deploy 键级摘要）。
		{Kind: hub.KindKeyBinding, ContentKey: "kb-global-editor-grid", Category: CategoryCreate,
			Summary: "键位 全局 · EditorToggleGridSpace = Ctrl+G", DefaultChecked: true},
		{Kind: hub.KindRulesetSetting, ContentKey: "rs-osu-hud", Category: CategoryConflict,
			Summary: "规则集设置 osu · hud_position", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "Value", HubValue: `"bottomleft"`, ForkValue: `"topright"`}}},
		{Kind: hub.KindModPreset, ContentKey: "mp-osu-score", Category: CategoryCreate,
			Summary: "模组预设 osu · Score Preset", DefaultChecked: true},
	}
}

// mockForkDiffRows fork↔fork 样本（lazer → delta）：覆盖五类 kind 与
// target-only（仅 fork↔fork 模式产出）。
func mockForkDiffRows() []DiffRow {
	return []DiffRow{
		{Kind: hub.KindCollection, ContentKey: "新谱面收藏", Category: CategoryCreate,
			Summary: "收藏夹「新谱面收藏」", DefaultChecked: true},
		{Kind: hub.KindCollection, ContentKey: "delta 独有收藏", Category: CategoryTargetOnly,
			Summary: "收藏夹「delta 独有收藏」", DefaultChecked: false},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-old-ver-0002", Category: CategoryVersionUpdate,
			Summary: "ginkiha - EOS（kamome sano）", IdentityKey: "online:1656406",
			CurrentKey: "hash-eos-new-0002", Evidence: EvidenceStrong, DefaultChecked: true},
		{Kind: hub.KindCollection, ContentKey: "常用练习", Category: CategoryConflict,
			Summary: "收藏夹「常用练习」", DefaultChecked: true,
			ConflictFields: []FieldDiff{{Field: "BeatmapMD5Hashes",
				HubValue: "md5-a,md5-b,md5-c", ForkValue: "md5-a,md5-b"}}},
		{Kind: hub.KindSkin, ContentKey: "hash-skin-new-0005", Category: CategoryCreate,
			Summary: "皮肤「Argon（modified）」（peppy）", DefaultChecked: true},
		{Kind: hub.KindScore, ContentKey: "bh-new|sc-0008", Category: CategoryTargetOnly,
			Summary: "成绩 osu · 987654 分 · 2026-02-01", DefaultChecked: false},
	}
}

func (m *mockCompare) PreviewDeploy(target string) ([]DiffRow, error) {
	if target == "" {
		return nil, errors.New("appsvc: 请选择目标 fork")
	}
	return mockPreviewRows(), nil
}

func (m *mockCompare) CompareForks(source, target string) ([]DiffRow, error) {
	if source == "" || target == "" {
		return nil, errors.New("appsvc: 请选择对比双方 fork")
	}
	if source == target {
		return nil, errors.New("appsvc: 对比双方不能是同一个 fork")
	}
	return mockForkDiffRows(), nil
}

// mockDeletedRows 删除组样本（DeletedEntries 与 Library 共用一份）。
func mockDeletedRows() []DiffRow {
	return []DiffRow{
		{Kind: hub.KindCollection, ContentKey: "旧收藏", Category: CategoryRestore,
			Summary: "收藏夹「旧收藏」", DefaultChecked: false,
			DeletedAt: time.Now().Add(-72 * time.Hour).UnixMilli()},
		{Kind: hub.KindBeatmapSet, ContentKey: "hash-deleted-0009", Category: CategoryRestore,
			Summary: "DECO*27 - Ghost Rule（Marathon）", DefaultChecked: false,
			DeletedAt: time.Now().Add(-240 * time.Hour).UnixMilli()},
		{Kind: hub.KindSkin, ContentKey: "hash-skin-deleted-0007", Category: CategoryRestore,
			Summary: "皮肤「Retired Skin」（作者乙）", DefaultChecked: false,
			DeletedAt: time.Now().Add(-120 * time.Hour).UnixMilli()},
		{Kind: hub.KindScore, ContentKey: "bh-gone|sc-0010", Category: CategoryRestore,
			Summary: "成绩 fruits · 4242 分 · 2025-12-30", DefaultChecked: false,
			DeletedAt: time.Now().Add(-24 * time.Hour).UnixMilli()},
	}
}

func (m *mockCompare) DeletedEntries(target string) ([]DiffRow, error) {
	if target == "" {
		return nil, errors.New("appsvc: 请选择目标 fork")
	}
	return mockDeletedRows(), nil
}

// ListConflicts mock：演示冲突清单（两条固定行，无真实 hub）；「保留差
// 异」处置过的键（kept）不再枚举——处置语义与真实后端一致。
func (m *mockCompare) ListConflicts() ([]ConflictRow, error) {
	bl := m.b.getBaseline()
	rows := []ConflictRow{
		{Kind: string(hub.KindBeatmapSet), ContentKey: "hash-conflict-0004", Label: "IA - Suna no Wakusei（nhato）", Forks: []string{"delta", "lazer"}, Values: 2, CanPickHub: true, BaselineFork: bl},
		{Kind: string(hub.KindRulesetSetting), ContentKey: hub.RulesetSettingContentKey("osu", 0, "hud_position"), Label: "osu · 变体 0 · hud_position", Forks: []string{"delta", "lazer"}, Values: 2, CanPickHub: true, BaselineFork: bl},
		{Kind: "game_ini", ContentKey: "ShowFpsDisplay", Label: "game.ini · ShowFpsDisplay", Forks: []string{"delta", "lazer"}, Values: 2, CanPickHub: false, BaselineFork: bl},
	}
	m.b.mu.Lock()
	kept := m.b.kept
	m.b.mu.Unlock()
	out := rows[:0]
	for _, r := range rows {
		if kept[r.Kind+"\x00"+r.ContentKey] {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// UnitConflict mock：演示冲突明细面板形态（两端取值 + 两端缺席置灰，
// 无真实 hub）。CanPickHub 对 game.ini 为 false（无 canonical 表）。
func (m *mockCompare) UnitConflict(kind hub.EntityKind, contentKey string) (ConflictDetail, error) {
	return ConflictDetail{
		Kind: string(kind), ContentKey: contentKey, CanPickHub: kind != hub.EntityKind(hub.BaselineKindGameIni),
		Canonical: "中枢值（演示）",
		Variants: []ConflictVariant{
			{ForkID: "delta", Has: true, Key: contentKey, Value: "delta 端取值（演示）",
				Fields: []FieldDiff{{Field: "Value", HubValue: "中枢值", ForkValue: "delta 值"}}},
			{ForkID: "ez2lazer", Has: false, Value: "缺席"},
			{ForkID: "g0v0", Has: false, Value: "缺席"},
			{ForkID: "lazer", Has: true, Key: contentKey, Value: "lazer 端取值（演示）",
				Fields: []FieldDiff{{Field: "Value", HubValue: "中枢值", ForkValue: "lazer 值"}}},
		},
	}, nil
}

// CoverImage mock：演示模式返回空串（行退化为纯色块），不编造图片。
// 真实封面只在已有收集过的 hub+filestore 上有意义。
func (m *mockCompare) CoverImage(contentKey string) (string, error) {
	return "", nil
}

// ForkPresence mock 数据（数据浏览 x/y 聚合数据源）。
// 演示：谱面集 0001 全 4 端持有 / 0002 lazer+delta / 0003 仅 lazer /
// 0004 delta+ez2 / 其余 kind 简化覆盖；库内同步全项（mockCanonItems）
// 全部 4 端持有。
func (m *mockCompare) ForkPresence(kind hub.EntityKind) ([]ForkPresenceEntry, error) {
	return mockPresenceEntries(kind), nil
}

func mockPresenceEntries(kind hub.EntityKind) []ForkPresenceEntry {
	now := time.Now().UnixMilli()
	mk := func(fork, key string, hoursAgo int) ForkPresenceEntry {
		return ForkPresenceEntry{ForkID: fork, Kind: kind, ContentKey: key,
			CollectedAt: now - int64(hoursAgo)*3600000}
	}
	var out []ForkPresenceEntry
	switch kind {
	case hub.KindBeatmapSet:
		out = []ForkPresenceEntry{
			mk("lazer", "hash-new-set-0001", 1), // create 键：仅来源端持有
			mk("lazer", "hash-old-ver-0002", 2), mk("delta", "hash-old-ver-0002", 2),
			mk("lazer", "hash-weak-0003", 3),
			mk("delta", "hash-conflict-0004", 1), mk("ez2lazer", "hash-conflict-0004", 1),
			mk("lazer", "hash-deleted-0009", 4),
		}
	case hub.KindCollection:
		out = []ForkPresenceEntry{
			mk("lazer", "新谱面收藏", 1),
			mk("lazer", "常用练习", 1), mk("delta", "常用练习", 2),
			mk("lazer", "旧收藏", 5), mk("delta", "旧收藏", 5),
		}
	case hub.KindSkin:
		out = []ForkPresenceEntry{
			mk("lazer", "hash-skin-new-0005", 1),
			mk("lazer", "hash-skin-conflict-0006", 1), mk("delta", "hash-skin-conflict-0006", 2),
		}
	case hub.KindScore:
		out = []ForkPresenceEntry{
			mk("lazer", "bh-new|sc-0008", 1),
			mk("lazer", "bh-old|sc-0009", 2), mk("delta", "bh-old|sc-0009", 2),
		}
	}
	for _, c := range mockCanonItems(kind) {
		for _, f := range mockRefs {
			out = append(out, mk(f.ForkID, c.ContentKey, 6))
		}
	}
	return out
}

// mockCanonItems 库内已同步条目样本（全部端持有的「满库」行——资料库页
// 不能只列差异，§4.2）。摘要形态与 summarizer 输出一致。
func mockCanonItems(kind hub.EntityKind) []canonItem {
	switch kind {
	case hub.KindBeatmapSet:
		return []canonItem{
			{ContentKey: "hash-canon-0001", IdentityKey: "online:2012345", Summary: "Camellia - Parallel Universe（Sotarks）"},
			{ContentKey: "hash-canon-0002", IdentityKey: "online:774891", Summary: "xi - Freedom Dive（Nathan）"},
			{ContentKey: "hash-canon-0003", IdentityKey: "online:512340", Summary: "DJ Okawari - Flower Dance（mapper）"},
			{ContentKey: "hash-canon-0004", IdentityKey: "meta:MEMODEMO|Aqualius|mapper", Summary: "MEMODEMO - Aqualius（mapper）"},
			{ContentKey: "hash-canon-0005", IdentityKey: "online:1899013", Summary: "S3RL - MTC（Ryu*）"},
			{ContentKey: "hash-canon-0006", IdentityKey: "online:2412996", Summary: "Frums - dropdead（Livi）"},
		}
	case hub.KindCollection:
		return []canonItem{
			{ContentKey: "常驻收藏", Summary: "收藏夹「常驻收藏」"},
			{ContentKey: "怀旧金曲", Summary: "收藏夹「怀旧金曲」"},
		}
	case hub.KindSkin:
		return []canonItem{
			{ContentKey: "hash-skin-canon-1", Summary: "皮肤「YUGEN」（itsamemario）"},
			{ContentKey: "hash-skin-canon-2", Summary: "皮肤「WhiteCat 2.1」（BK）"},
		}
	case hub.KindScore:
		return []canonItem{
			{ContentKey: "bh-c1|sc-c1", Summary: "成绩 osu · 1024000 分 · 2026-03-02"},
			{ContentKey: "bh-c2|sc-c2", Summary: "成绩 mania · 887654 分 · 2026-02-18"},
		}
	}
	return nil
}

// HubCounts mock：canonical ∪ 预演/删除组键的去重计数（与真实 SQL 口径
// 同形——库内条目 = canonical 行 + 预演行键）。
func (m *mockCompare) HubCounts() (map[string]int, error) {
	out := map[string]int{}
	for _, kind := range []hub.EntityKind{hub.KindBeatmapSet, hub.KindCollection, hub.KindSkin, hub.KindScore} {
		keys := map[string]bool{}
		for _, c := range mockCanonItems(kind) {
			keys[c.ContentKey] = true
		}
		for _, r := range mockPreviewRows() {
			if r.Kind == kind {
				keys[r.ContentKey] = true
			}
		}
		for _, r := range mockDeletedRows() {
			if r.Kind == kind {
				keys[r.ContentKey] = true
			}
		}
		out[string(kind)] = len(keys)
	}
	return out, nil
}

// Library mock：与真实后端同一 buildLibrary——注册端逐端喂预演/删除组/
// presence 样本（Target 角色取内存态同步范围）。
func (m *mockCompare) Library(kind hub.EntityKind) ([]LibraryItem, error) {
	if !libraryKinds[kind] {
		return nil, fmt.Errorf("appsvc: 资料库页不支持实体类型 %q", kind)
	}
	scope := m.b.getScope()
	targetOf := map[string]bool{}
	for _, fr := range scope.Forks {
		targetOf[fr.ForkID] = fr.Target
	}
	m.b.lib.mu.Lock()
	refs := m.b.lib.reg()
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	m.b.lib.mu.Unlock()
	sort.Strings(ids)
	presenceOf := map[string]map[string]bool{}
	for _, e := range mockPresenceEntries(kind) {
		m, ok := presenceOf[e.ForkID]
		if !ok {
			m = map[string]bool{}
			presenceOf[e.ForkID] = m
		}
		m[e.ContentKey] = true
	}
	var ins []forkLibInput
	for _, fid := range ids {
		var preview, deleted []DiffRow
		for _, r := range mockPreviewRows() {
			if r.Kind == kind {
				preview = append(preview, r)
			}
		}
		for _, r := range mockDeletedRows() {
			if r.Kind == kind {
				deleted = append(deleted, r)
			}
		}
		pres := presenceOf[fid]
		if pres == nil {
			pres = map[string]bool{}
		}
		ins = append(ins, forkLibInput{
			ForkID: fid, Target: targetOf[fid],
			Preview: preview, Deleted: deleted, Presence: pres,
		})
	}
	return buildLibrary(kind, mockCanonItems(kind), ins), nil
}

// BuildPlan 勾选 → 计划（内存持久；动作面与真实后端同构：M2 三类放行
// ——谱面集 create/update/version-replace、皮肤/成绩 create/update，
// 勾选恢复组 = 显式拍板；走 deploy.NewPlan 校验）。
func (m *mockCompare) BuildPlan(target string, sel []Selection) (deploy.Plan, error) {
	if target == "" {
		return deploy.Plan{}, errors.New("appsvc: 生成计划缺少目标 fork")
	}
	if len(sel) == 0 {
		return deploy.Plan{}, errors.New("appsvc: 未勾选任何条目")
	}
	var targetRef forks.LibraryRef
	for _, r := range mockRefs {
		if r.ForkID == target {
			targetRef = r
		}
	}
	if targetRef.ForkID == "" {
		return deploy.Plan{}, fmt.Errorf("appsvc: 目标 fork %q 未注册", target)
	}
	rows := mockPreviewRows()
	byKey := map[string]DiffRow{}
	for _, r := range rows {
		byKey[string(r.Kind)+"\x00"+r.ContentKey] = r
	}
	var raw []deploy.PlanItem
	seen := map[string]bool{}
	for _, sl := range sel {
		key := string(sl.Kind) + "\x00" + sl.ContentKey
		if seen[key] {
			continue
		}
		seen[key] = true
		row, ok := byKey[key]
		if !ok {
			raw = append(raw, deploy.PlanItem{
				Kind: sl.Kind, ContentKey: sl.ContentKey, Action: deploy.ActionSkip,
				Summary:    string(sl.Kind) + " " + shortKey(sl.ContentKey),
				SkipReason: "预演数据中不存在该键（快照可能已过期，请刷新预览）",
			})
			continue
		}
		item := deploy.PlanItem{Kind: row.Kind, ContentKey: row.ContentKey, Summary: row.Summary}
		if row.Category == CategoryVersionUpdate {
			// 版本更新（谱面集专属，§4.4）：链尾键为写入键，行键即目标旧版本键。
			item.ContentKey, item.OldContentKey = row.CurrentKey, row.ContentKey
			item.Action = deploy.ActionVersionReplace
			if row.Evidence == EvidenceWeak {
				item.NeedsConfirm = true // 勾选即拍板留痕（§5.4）
			}
		} else {
			switch row.Kind {
			case hub.KindCollection, hub.KindSkin, hub.KindScore, hub.KindBeatmapSet,
				hub.KindKeyBinding, hub.KindRulesetSetting, hub.KindModPreset:
				switch row.Category {
				case CategoryCreate, CategoryRestore: // 勾选恢复组 = 用户显式拍板投放并清 tombstone（§5.4）
					item.Action = deploy.ActionCreate
					item.Restore = row.Category == CategoryRestore
				case CategoryConflict:
					item.Action = deploy.ActionUpdate // 默认 hub 覆盖（§5.4）
				default:
					item.Action, item.SkipReason = deploy.ActionSkip,
						string(row.Kind)+" 无版本更新语义（内容键即身份，§4.2）"
				}
			default:
				item.Action, item.SkipReason = deploy.ActionSkip,
					string(row.Kind)+" 投放属未开放写路径"
			}
		}
		raw = append(raw, item)
	}
	plan, err := deploy.NewPlan(target, raw)
	if err != nil {
		return deploy.Plan{}, err
	}
	plan.Target = targetRef
	m.mu.Lock()
	m.plan, m.createdAt = plan, time.Now()
	m.mu.Unlock()
	return plan, nil
}

func (r DiffRow) summary() string {
	if r.Summary != "" {
		return r.Summary
	}
	return string(r.Kind) + " " + shortKey(r.ContentKey)
}

func (m *mockCompare) CurrentPlan() (deploy.Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.plan.TargetFork == "" {
		return deploy.Plan{}, ErrNoPlan
	}
	return m.plan, nil
}

// --- 同步执行 ---

// mockRun 是 mock 的一轮同步占位（prepare 产物的运行时参照）。
type mockRun struct {
	preview RunPreview
}

type mockSync struct {
	cmp    *mockCompare
	b      *mockBackend
	prog   ProgressSink
	mu     sync.Mutex
	cancel context.CancelFunc
	status SyncStatus
	runs   map[string]*mockRun
}

func (m *mockSync) emit(ev ProgressEvent) {
	if m.prog != nil {
		m.prog(ev)
	}
}

// Collect 模拟收集两阶段进度（投影 → 收仓），g0v0 演示守卫拦截。
func (m *mockSync) Collect(forkID string) (collect.Report, error) {
	if err := m.begin("collect", forkID); err != nil {
		return collect.Report{}, err
	}
	defer m.finish()
	if forkID == "g0v0" {
		return collect.Report{}, fmt.Errorf("%w（运行中：[g0v0!]）", guard.ErrGameRunning)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.setCancel(cancel)
	defer cancel()
	steps := []struct {
		stage collect.Stage
		label string
		total int
	}{
		{collect.StageProject, "只读投影", 12},
		{collect.StageApply, "入 hub", 12},
		{collect.StageAdopt, "收仓+回链", 34},
	}
	for _, st := range steps {
		for i := 1; i <= st.total; i++ {
			if ctx.Err() != nil {
				return collect.Report{}, ctx.Err()
			}
			m.emit(ProgressEvent{Service: "collect", Stage: string(st.stage), ForkID: forkID, Current: i, Total: st.total,
				Message: fmt.Sprintf("%s：%d/%d", st.label, i, st.total), At: time.Now()})
			time.Sleep(mockSleep)
		}
	}
	return collect.Report{
		Entities: map[hub.EntityKind]int{hub.KindCollection: 28, hub.KindBeatmapSet: 1314,
			hub.KindBeatmap: 5210, hub.KindSkin: 17, hub.KindScore: 8632},
		Skips:           []hub.SkipStat{{Reason: "Protected 系统谱面集", Count: 4}, {Reason: "Ez 外部托管谱面集", Count: 2}},
		FilesAdopted:    34,
		FilesSkipped:    0,
		AdoptModeCounts: map[filestore.LinkMode]int{filestore.LinkSymlink: 34},
	}, nil
}

// DeployCurrentPlan 模拟投放：守卫 → 备份（一次性）→ 4 批 ×（建链接 →
// 事务写入 → DriftGuard）；取消点在批间（§3.3）。
func (m *mockSync) DeployCurrentPlan() (deploy.Report, error) {
	plan, err := m.cmp.CurrentPlan()
	if err != nil {
		return deploy.Report{}, err
	}
	if err := m.begin("deploy", plan.TargetFork); err != nil {
		return deploy.Report{}, err
	}
	defer m.finish()
	if plan.TargetFork == "g0v0" {
		return deploy.Report{}, fmt.Errorf("%w（运行中：[g0v0!]）", guard.ErrGameRunning)
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.setCancel(cancel)
	defer cancel()

	backup := fmt.Sprintf("/mock/data/backups/client.realm.%s", time.Now().UTC().Format("20060102-150405"))
	m.emit(ProgressEvent{Service: "deploy", Stage: string(deploy.StageGuard), ForkID: plan.TargetFork, At: time.Now(), Message: "守卫复查通过"})
	time.Sleep(mockSleep)
	m.emit(ProgressEvent{Service: "deploy", Stage: string(deploy.StageBackup), ForkID: plan.TargetFork, At: time.Now(), Message: "写前自动备份：" + backup})
	time.Sleep(mockSleep)

	const batches = 4
	applied := []deploy.ItemResult{}
	notRun := []deploy.PlanItem{}
	cancelled := false
	for b := 1; b <= batches; b++ {
		if ctx.Err() != nil {
			cancelled = true
			for _, it := range remaining(plan, applied) {
				notRun = append(notRun, it)
			}
			break
		}
		for _, st := range []deploy.Stage{deploy.StageLink, deploy.StageWrite, deploy.StageVerify} {
			for i := 1; i <= 3; i++ {
				m.emit(ProgressEvent{Service: "deploy", Stage: string(st), ForkID: plan.TargetFork, Current: i, Total: 3,
					Message: fmt.Sprintf("批 %d/%d · %s：%d/3", b, batches, stageLabel(st), i), At: time.Now()})
				time.Sleep(mockSleep)
			}
		}
		for _, it := range plan.Items {
			if it.Action == deploy.ActionCreate || it.Action == deploy.ActionUpdate {
				applied = append(applied, deploy.ItemResult{Kind: it.Kind, ContentKey: it.ContentKey, Action: it.Action})
			}
		}
	}
	if cancelled {
		return deploy.Report{Applied: applied, Skipped: mockSkipStats(plan), NotRun: notRun, BackupPath: backup}, context.Canceled
	}
	// 演示一次执行期改判（§3.3：现场复核与计划不符）。
	// game.ini 白名单键同步的组合面演示（真实面经 deploy.SyncGameIni，
	// appsvc realSync.DeployCurrentPlan 组合）。
	report := deploy.Report{Applied: applied, Skipped: mockSkipStats(plan), BackupPath: backup,
		GameIni: &deploy.GameIniResult{Applied: map[string]string{"ShowFpsDisplay": "true"}}}
	if len(applied) > 0 {
		first := applied[0]
		report.Repointed = []deploy.RepointedItem{{
			Item:   deploy.PlanItem{Kind: first.Kind, ContentKey: first.ContentKey, Action: first.Action},
			Reason: "目标已出现同键行：新建 → 就地更新",
		}}
	}
	return report, nil
}

// mockSkipStats 计划内 skip 项按原因分组（SkipCollector 口径，§3.3）。
func mockSkipStats(plan deploy.Plan) []hub.SkipStat {
	counts := map[string]int{}
	for _, it := range plan.Items {
		if it.Action == deploy.ActionSkip {
			counts[it.SkipReason]++
		}
	}
	reasons := make([]string, 0, len(counts))
	for r := range counts {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	out := make([]hub.SkipStat, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, hub.SkipStat{Reason: r, Count: counts[r]})
	}
	return out
}

// remaining 取计划中未被 applied 覆盖的项（未执行批清单，§6.3）。
func remaining(plan deploy.Plan, applied []deploy.ItemResult) []deploy.PlanItem {
	done := map[string]bool{}
	for _, a := range applied {
		done[string(a.Kind)+"\x00"+a.ContentKey] = true
	}
	var out []deploy.PlanItem
	for _, it := range plan.Items {
		if !done[string(it.Kind)+"\x00"+it.ContentKey] {
			out = append(out, it)
		}
	}
	return out
}

// PrepareRun 模拟一轮同步准备（§0.4 步骤 1-4）：按内存态 scope 逐端
// 守卫（g0v0 模拟运行中跳过）、收集进度事件、逐目标端预览计数与待
// 处理计数；产物存 m.runs 供 CommitRun 消费。
func (m *mockSync) PrepareRun() (RunPreview, error) {
	return m.prepareRun(nil)
}

// PrepareSelected 数据页勾选触发的一轮：预览计数按 sel 条目数模拟。
func (m *mockSync) PrepareSelected(sel []Selection) (RunPreview, error) {
	return m.prepareRun(sel)
}

func (m *mockSync) prepareRun(sel []Selection) (RunPreview, error) {
	if err := m.begin("run", ""); err != nil {
		return RunPreview{}, err
	}
	defer m.finish()
	ctx, cancel := context.WithCancel(context.Background())
	m.setCancel(cancel)
	defer cancel()

	scope := m.b.getScope()
	runID := fmt.Sprintf("run-mock-%d", time.Now().UnixMilli())
	preview := RunPreview{RunID: runID, Targets: []RunTargetPreview{}, Skipped: []RunSkippedFork{}}

	// 模拟逐端收集进度（g0v0 守卫拦截）。
	for _, fr := range scope.Forks {
		if fr.ForkID == "g0v0" {
			preview.Skipped = append(preview.Skipped, RunSkippedFork{
				ForkID: "g0v0", Reason: "游戏运行中：g0v0!"})
			continue
		}
		if !fr.Source && !fr.Target {
			continue
		}
		for _, st := range []collect.Stage{collect.StageProject, collect.StageApply, collect.StageAdopt} {
			m.emit(ProgressEvent{Service: "run", Stage: string(st), ForkID: fr.ForkID, Current: 1, Total: 1,
				Message: fmt.Sprintf("%s · %s", fr.ForkID, stageLabel(st)), At: time.Now()})
			time.Sleep(mockSleep)
			if ctx.Err() != nil {
				return RunPreview{}, ctx.Err()
			}
		}
	}

	// 逐目标端预览：条目数 = 板块开时 create 项计数；PrepareSelected 按
	// 勾选数模拟。
	createCount := 0
	for _, r := range mockPreviewRows() {
		if r.Category == CategoryCreate {
			createCount++
		}
	}
	for _, fr := range scope.Forks {
		if !fr.Target || fr.ForkID == "g0v0" {
			continue
		}
		n := createCount
		if sel != nil {
			n = len(sel)
		}
		preview.Targets = append(preview.Targets, RunTargetPreview{ForkID: fr.ForkID, Items: n})
	}
	// 待处理计数演示（冲突/弱键需确认/恢复/交集不支持各一条）。
	preview.Pending = PendingSummary{Conflicts: 1, NeedsConfirm: 1, Restore: 1, Unsupported: 1}

	m.mu.Lock()
	m.runs = map[string]*mockRun{runID: {preview: preview}}
	m.mu.Unlock()
	return preview, nil
}

// CommitRun 模拟一轮同步提交（§0.4 步骤 6-7）：逐目标端 run 进度
// （守卫 → 备份 → 批写入），g0v0 守卫跳过；报告逐端收集/投放摘要与
// 待处理计数。未知 runID 报「请重新同步」。
func (m *mockSync) CommitRun(runID string) (RunReport, error) {
	m.mu.Lock()
	run, ok := m.runs[runID]
	if ok {
		delete(m.runs, runID)
	}
	m.mu.Unlock()
	if !ok {
		return RunReport{}, errors.New("appsvc: 本轮同步计划已失效或不存在，请重新同步")
	}
	if err := m.begin("run", ""); err != nil {
		return RunReport{}, err
	}
	defer m.finish()
	ctx, cancel := context.WithCancel(context.Background())
	m.setCancel(cancel)
	defer cancel()

	scope := m.b.getScope()
	report := RunReport{Pending: run.preview.Pending, Forks: []ForkRunReport{}}
	skipSet := map[string]string{}
	for _, sk := range run.preview.Skipped {
		skipSet[sk.ForkID] = sk.Reason
	}
	items := map[string]int{}
	for _, tp := range run.preview.Targets {
		items[tp.ForkID] = tp.Items
	}
	for _, fr := range scope.Forks {
		frp := ForkRunReport{ForkID: fr.ForkID, Source: fr.Source, Target: fr.Target}
		if reason, skip := skipSet[fr.ForkID]; skip {
			frp.Skipped, frp.SkipReason = true, reason
			report.Forks = append(report.Forks, frp)
			continue
		}
		frp.Collect = &collect.Report{
			Entities:        map[hub.EntityKind]int{hub.KindCollection: 3},
			Skips:           []hub.SkipStat{},
			AdoptModeCounts: map[filestore.LinkMode]int{},
		}
		if fr.Target {
			for _, st := range []deploy.Stage{deploy.StageGuard, deploy.StageBackup, deploy.StageLink, deploy.StageWrite, deploy.StageVerify} {
				if ctx.Err() != nil {
					report.Forks = append(report.Forks, frp)
					return report, ctx.Err()
				}
				m.emit(ProgressEvent{Service: "run", Stage: string(st), ForkID: fr.ForkID, Current: 1, Total: 1,
					Message: fmt.Sprintf("%s · %s", fr.ForkID, stageLabel(st)), At: time.Now()})
				time.Sleep(mockSleep)
			}
			n := items[fr.ForkID]
			applied := make([]deploy.ItemResult, 0, n)
			for i := 0; i < n; i++ {
				applied = append(applied, deploy.ItemResult{Kind: hub.KindCollection, ContentKey: fmt.Sprintf("mock-item-%02d", i), Action: deploy.ActionCreate})
			}
			frp.Deploy = &deploy.Report{
				Applied:       applied,
				Skipped:       []hub.SkipStat{{Reason: deploy.IntersectionRejectPrefix + "g0v0 不支持 — 演示键（整条跳过）", Count: 1}},
				FailedBatches: []deploy.BatchFailure{},
				NotRun:        []deploy.PlanItem{},
				Repointed:     []deploy.RepointedItem{},
				BackupPath:    "/mock/data/backups/client.realm.run",
			}
		}
		report.Forks = append(report.Forks, frp)
	}
	return report, nil
}

func (m *mockSync) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
}

func (m *mockSync) Running() SyncStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *mockSync) RestoreBackup(forkID, backupPath string) error {
	if forkID == "" || backupPath == "" {
		return errors.New("appsvc: 还原需要 fork 与备份路径")
	}
	if forkID == "g0v0" {
		return fmt.Errorf("%w（运行中：[g0v0!]）", guard.ErrGameRunning)
	}
	return nil
}

// KeepDifference mock：记 kept 键集，ListConflicts 不再枚举（处置后不再
// 提示的语义演示；mock 无 baseline 表，记忆态即语义）。
func (m *mockSync) KeepDifference(sel []Selection) error {
	if len(sel) == 0 {
		return errors.New("appsvc: 未选择任何条目")
	}
	if err := m.begin("keep", ""); err != nil {
		return err
	}
	defer m.finish()
	m.b.mu.Lock()
	defer m.b.mu.Unlock()
	if m.b.kept == nil {
		m.b.kept = map[string]bool{}
	}
	for _, sl := range sel {
		m.b.kept[string(sl.Kind)+"\x00"+sl.ContentKey] = true
	}
	return nil
}

func (m *mockSync) begin(kind, forkID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.Busy {
		return ErrBusy
	}
	m.status = SyncStatus{Busy: true, Kind: kind, ForkID: forkID}
	return nil
}

func (m *mockSync) finish() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = nil
	m.status = SyncStatus{}
}

func (m *mockSync) setCancel(cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = cancel
}

// --- 设置（mockSettings 即 mockBackend 的 SettingsService 实现） ---

func (b *mockBackend) Get() (SettingsInfo, error) {
	return SettingsInfo{
		Mode:         b.mode,
		Reason:       b.reasn,
		DataDir:      "/mock/data",
		HubDBPath:    "/mock/data/hub.db",
		BackupsDir:   "/mock/data/backups",
		BackupsCount: 3,
		GuardNames:   []string{"Ez2Lazer", "g0v0!", "osu", "osu!"},
		LinkSymlink:  true,
		LinkHardlink: true,
		LinkNote:     "Mock 探测结果（真实后端按 §5.3 现场探测）",
		NativeLibOK:  false,
		NativeLibErr: "Mock 后端不加载 native 库（M-4 冒烟模式）",
		BaselineFork: b.getBaseline(),
	}, nil
}

// getScope 当前同步范围（内存态；新注册的端自动补默认双角色条目）。
func (b *mockBackend) getScope() SyncScope {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.scope == nil {
		s := &SyncScope{}
		for _, sec := range AllSections {
			s.Sections = append(s.Sections, SectionToggle{Section: sec, Enabled: true})
		}
		b.scope = s
	}
	// 注册态与角色表对齐：新注册的端补默认双角色。
	b.lib.mu.Lock()
	refs := b.lib.reg()
	for _, r := range refs {
		found := false
		for _, fr := range b.scope.Forks {
			if fr.ForkID == r.ForkID {
				found = true
				break
			}
		}
		if !found {
			b.scope.Forks = append(b.scope.Forks, ForkRole{
				ForkID: r.ForkID, RealmPath: r.RealmPath, Source: true, Target: true})
		}
	}
	b.lib.mu.Unlock()
	out := SyncScope{
		Sections: append([]SectionToggle(nil), b.scope.Sections...),
		Forks:    append([]ForkRole(nil), b.scope.Forks...),
	}
	sort.Slice(out.Forks, func(i, j int) bool { return out.Forks[i].ForkID < out.Forks[j].ForkID })
	return out
}

// getBaseline 内存态基准端（默认 lazer）。
func (b *mockBackend) getBaseline() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.baseline == "" {
		b.baseline = "lazer"
	}
	return b.baseline
}

// GetBaseline 基准端。
func (b *mockBackend) GetBaseline() string {
	return b.getBaseline()
}

// SetBaseline 持久化基准端（mock：内存态）。
func (b *mockBackend) SetBaseline(forkID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.baseline = forkID
	return nil
}

// GetSyncScope 同步范围（§0-G1 mock：内存态）。
func (b *mockBackend) GetSyncScope() (SyncScope, error) {
	return b.getScope(), nil
}

// SetSyncScope 持久化同步范围（mock：内存态）；校验与真实后端同构
// （未知板块 / 未注册端报错）。
func (b *mockBackend) SetSyncScope(scope SyncScope) error {
	for _, tg := range scope.Sections {
		if !knownSection(tg.Section) {
			return fmt.Errorf("appsvc: 未知同步板块 %q", tg.Section)
		}
	}
	b.lib.mu.Lock()
	refs := b.lib.reg()
	b.lib.mu.Unlock()
	for _, fr := range scope.Forks {
		if _, ok := refs[fr.ForkID]; !ok {
			return fmt.Errorf("appsvc: 未注册的 fork %q，无法设置同步角色", fr.ForkID)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.scope == nil {
		s := &SyncScope{}
		for _, sec := range AllSections {
			s.Sections = append(s.Sections, SectionToggle{Section: sec, Enabled: true})
		}
		b.scope = s
	}
	for _, tg := range scope.Sections {
		for i := range b.scope.Sections {
			if b.scope.Sections[i].Section == tg.Section {
				b.scope.Sections[i].Enabled = tg.Enabled
			}
		}
	}
	for _, fr := range scope.Forks {
		found := false
		for i := range b.scope.Forks {
			if b.scope.Forks[i].ForkID == fr.ForkID {
				b.scope.Forks[i].Source, b.scope.Forks[i].Target = fr.Source, fr.Target
				found = true
				break
			}
		}
		if !found {
			b.scope.Forks = append(b.scope.Forks, fr)
		}
	}
	return nil
}
