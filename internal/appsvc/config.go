package appsvc

// 同步范围持久化（§0-G1）：同步板块开关与每端角色（拉取/写入）落
// Deps.DataDir/config.json。盘面形态是本文件的 syncConfig；对外契约形态
// 是 SyncScope（Forks 按 ForkID 排序）。DataDir 为空 = 纯内存（测试用），
// 不写盘。

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"lzsync/internal/forks"
)

// Section 同步板块（§0 设置页「同步范围」的板块开关维度）。
type Section string

const (
	SectionBeatmaps        Section = "beatmap_set" // 含 beatmap 难度
	SectionCollections     Section = "collection"
	SectionSkins           Section = "skin"
	SectionScores          Section = "score"
	SectionKeyBindings     Section = "key_binding"
	SectionRulesetSettings Section = "ruleset_setting"
	SectionModPresets      Section = "mod_preset"
	SectionGameIni         Section = "game_ini"
)

// AllSections 全部板块（前端渲染与缺省「全开」的声明顺序）。
var AllSections = []Section{
	SectionBeatmaps, SectionCollections, SectionSkins, SectionScores,
	SectionKeyBindings, SectionRulesetSettings, SectionModPresets, SectionGameIni,
}

// ForkRole 单端的同步角色（§0 来源端/目标端）。
type ForkRole struct {
	ForkID    string
	RealmPath string
	Source    bool // 拉取：该端数据进 hub
	Target    bool // 写入：hub 数据投放到该端
}

// SectionToggle 板块开关（前端形态）。
type SectionToggle struct {
	Section Section
	Enabled bool
}

// SyncScope 一轮同步的范围（返回前端的形态，Forks 按 ForkID 排序）。
type SyncScope struct {
	Sections []SectionToggle
	Forks    []ForkRole
}

// syncConfigVersion 盘面格式版本（向前兼容拒绝：更高的版本报错不静默降级）。
const syncConfigVersion = 1

// syncConfig config.json 的落盘形态。
type syncConfig struct {
	Version  int                `json:"version"`
	Forks    []ForkRole         `json:"forks"`
	Sections map[Section]bool   `json:"sections"`
	Ignored  []string           `json:"ignored"` // 用户移除过的 RealmPath，自动发现不再加回
	// BaselineFork 基准端（§3.5 收件箱外环 + 冲突处置默认参照端）：
	// 空 = 未设；设置页可改。默认 lazer（官方端）。
	BaselineFork string `json:"baseline_fork"`
}

// configStore 同步范围配置的持久化载体（Deps.DataDir/config.json）。
// dataDir 为空 = 纯内存（不落盘，供测试）。
type configStore struct {
	mu      sync.Mutex
	dataDir string
	cfg     syncConfig
}

// defaultSyncConfig 缺文件时的默认盘面（全部板块开）。
func defaultSyncConfig() syncConfig {
	sections := make(map[Section]bool, len(AllSections))
	for _, s := range AllSections {
		sections[s] = true
	}
	return syncConfig{
		Version:  syncConfigVersion,
		Forks:    []ForkRole{},
		Sections: sections,
		Ignored:  []string{},
		BaselineFork: "lazer",
	}
}

// knownSection 报告 s 是否为已声明板块。
func knownSection(s Section) bool {
	for _, k := range AllSections {
		if k == s {
			return true
		}
	}
	return false
}

// loadSyncConfig 读取/初始化同步范围配置。缺文件 = 默认全板块开；解析失败
// 返回错误（走 mock-fallback 呈现原因，不静默重置）。dataDir 为空时返回
// 纯内存 store（不写盘）。
func loadSyncConfig(dataDir string) (*configStore, error) {
	c := &configStore{dataDir: dataDir, cfg: defaultSyncConfig()}
	if dataDir == "" {
		return c, nil
	}
	b, err := os.ReadFile(c.path())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("appsvc: 读同步范围配置 %s: %w", c.path(), err)
	}
	var cfg syncConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("appsvc: 解析同步范围配置 %s: %w", c.path(), err)
	}
	if cfg.Version > syncConfigVersion {
		return nil, fmt.Errorf("appsvc: 同步范围配置版本 %d 高于本程序支持的 %d", cfg.Version, syncConfigVersion)
	}
	for s := range cfg.Sections {
		if !knownSection(s) {
			return nil, fmt.Errorf("appsvc: 同步范围配置含未知板块 %q", s)
		}
	}
	// 缺失键归一为默认开；nil 字段归位空集合。
	if cfg.Sections == nil {
		cfg.Sections = map[Section]bool{}
	}
	for _, s := range AllSections {
		if _, ok := cfg.Sections[s]; !ok {
			cfg.Sections[s] = true
		}
	}
	if cfg.Forks == nil {
		cfg.Forks = []ForkRole{}
	}
	if cfg.Ignored == nil {
		cfg.Ignored = []string{}
	}
	if cfg.BaselineFork == "" {
		cfg.BaselineFork = "lazer" // 默认官方端
	}
	c.cfg = cfg
	return c, nil
}

func (c *configStore) path() string {
	return filepath.Join(c.dataDir, "config.json")
}

// save 原子落盘（同目录临时文件 → rename）。纯内存态为 no-op。
func (c *configStore) save() error {
	if c.dataDir == "" {
		return nil
	}
	b, err := json.MarshalIndent(c.cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("appsvc: 序列化同步范围配置: %w", err)
	}
	tmp, err := os.CreateTemp(c.dataDir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("appsvc: 建配置临时文件: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("appsvc: 写配置临时文件: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("appsvc: 关闭配置临时文件: %w", err)
	}
	if err := os.Rename(tmpName, c.path()); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("appsvc: 配置原子落盘: %w", err)
	}
	return nil
}

// scope 返回 SyncScope（Forks 按 ForkID 排序、Sections 按 AllSections 序）。
func (c *configStore) scope() SyncScope {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := SyncScope{
		Sections: make([]SectionToggle, 0, len(AllSections)),
		Forks:    make([]ForkRole, 0, len(c.cfg.Forks)),
	}
	for _, s := range AllSections {
		out.Sections = append(out.Sections, SectionToggle{Section: s, Enabled: c.cfg.Sections[s]})
	}
	out.Forks = append(out.Forks, c.cfg.Forks...)
	sort.Slice(out.Forks, func(i, j int) bool { return out.Forks[i].ForkID < out.Forks[j].ForkID })
	return out
}

// sectionEnabled 报告板块开关（未登记 = 默认开，与缺文件默认一致）。
func (c *configStore) sectionEnabled(s Section) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cfg.Sections[s]
	return !ok || v
}

// role 取单端角色。
func (c *configStore) role(forkID string) (ForkRole, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fr := range c.cfg.Forks {
		if fr.ForkID == forkID {
			return fr, true
		}
	}
	return ForkRole{}, false
}

// sourceForkIDs 全部 Source=true 端的 ForkID（§0-B5 歧义检查的来源集合
// ——含因运行中/收集失败被跳过的端，其 presence 是最后已知取值）。
func (c *configStore) sourceForkIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.cfg.Forks))
	for _, fr := range c.cfg.Forks {
		if fr.Source {
			out = append(out, fr.ForkID)
		}
	}
	sort.Strings(out)
	return out
}

// targetForkIDs 全部 Target=true 端的 ForkID（全局交集端清单——含未在
// 运行时注册表中的条目，交集判定按其 Profile 静态数据即可进行，§0-B3）。
func (c *configStore) targetForkIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.cfg.Forks))
	for _, fr := range c.cfg.Forks {
		if fr.Target {
			out = append(out, fr.ForkID)
		}
	}
	sort.Strings(out)
	return out
}

// applyScope 应用一份 SyncScope：更新列出的板块开关与端角色（未列出的
// 保持原值），并落盘。调用方负责前置校验（未知板块/未注册端）。
func (c *configStore) applyScope(scope SyncScope, realmPathOf func(forkID string) string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, tg := range scope.Sections {
		c.cfg.Sections[tg.Section] = tg.Enabled
	}
	for _, fr := range scope.Forks {
		found := false
		for i := range c.cfg.Forks {
			if c.cfg.Forks[i].ForkID == fr.ForkID {
				c.cfg.Forks[i].Source = fr.Source
				c.cfg.Forks[i].Target = fr.Target
				if c.cfg.Forks[i].RealmPath == "" {
					c.cfg.Forks[i].RealmPath = fr.RealmPath
				}
				found = true
				break
			}
		}
		if !found {
			entry := fr
			if entry.RealmPath == "" && realmPathOf != nil {
				entry.RealmPath = realmPathOf(fr.ForkID)
			}
			c.cfg.Forks = append(c.cfg.Forks, entry)
		}
	}
	return c.save()
}

// registerFork upsert 一个端（Register/AssignFork 共用）：新条目默认
// Source/Target 皆开，已有条目保角色只更新 RealmPath；RealmPath 从
// ignored 移除。
func (c *configStore) registerFork(ref forks.LibraryRef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.cfg.Forks {
		if c.cfg.Forks[i].ForkID == ref.ForkID {
			c.cfg.Forks[i].RealmPath = ref.RealmPath
			return c.removeIgnoredLocked(ref.RealmPath)
		}
	}
	c.cfg.Forks = append(c.cfg.Forks, ForkRole{
		ForkID: ref.ForkID, RealmPath: ref.RealmPath, Source: true, Target: true,
	})
	return c.removeIgnoredLocked(ref.RealmPath)
}

// removeIgnoredLocked 从 ignored 摘除路径（registerFork/applyScope 的尾随
// 步骤，持锁调用）。
func (c *configStore) removeIgnoredLocked(realmPath string) error {
	out := c.cfg.Ignored[:0]
	for _, p := range c.cfg.Ignored {
		if p != realmPath {
			out = append(out, p)
		}
	}
	c.cfg.Ignored = out
	return c.save()
}

// isIgnored 报告路径是否在 ignored 名单（自动发现不加回的判据）。
func (c *configStore) isIgnored(realmPath string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.cfg.Ignored {
		if p == realmPath {
			return true
		}
	}
	return false
}

// hasFork 报告 forkID 是否已在配置中。
func (c *configStore) hasFork(forkID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, fr := range c.cfg.Forks {
		if fr.ForkID == forkID {
			return true
		}
	}
	return false
}

// discoverFork 自动发现接线：不在配置且不在 ignored 的 ref 追加为
// Source=Target=true 并落盘；已在配置 → 不动（保留用户角色）。
// 返回值 = 是否应进运行时注册表（ignored 路径不进）。
func (c *configStore) discoverFork(ref forks.LibraryRef) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.cfg.Ignored {
		if p == ref.RealmPath {
			return false, nil
		}
	}
	for _, fr := range c.cfg.Forks {
		if fr.ForkID == ref.ForkID {
			return true, nil // 已配置：角色照旧，不重写
		}
	}
	c.cfg.Forks = append(c.cfg.Forks, ForkRole{
		ForkID: ref.ForkID, RealmPath: ref.RealmPath, Source: true, Target: true,
	})
	return true, c.save()
}

// baseline 返回基准端 ForkID（空 = 未设）。
func (c *configStore) baseline() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.BaselineFork
}

// setBaseline 设置基准端（校验：必须已注册端）。
func (c *configStore) setBaseline(forkID string, knownForks map[string]bool) error {
	if forkID != "" && !knownForks[forkID] {
		return fmt.Errorf("appsvc: 基准端 %q 未注册", forkID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg.BaselineFork = forkID
	return c.save()
}

// removeFork 解除端管理：删配置条目并把 RealmPath 记入 ignored（自动发现
// 不再加回）。
func (c *configStore) removeFork(forkID, realmPath string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.cfg.Forks[:0]
	for _, fr := range c.cfg.Forks {
		if fr.ForkID != forkID {
			out = append(out, fr)
		}
	}
	c.cfg.Forks = out
	if realmPath != "" {
		seen := false
		for _, p := range c.cfg.Ignored {
			if p == realmPath {
				seen = true
				break
			}
		}
		if !seen {
			c.cfg.Ignored = append(c.cfg.Ignored, realmPath)
		}
	}
	return c.save()
}
