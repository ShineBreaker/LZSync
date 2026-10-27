package appsvc

// 真实后端实现：库身份走 forks、对账走 hub（ReconcileAgainst/Tombstones
// + skin/score 过渡直查，见 reconcile_skin_score.go），fork↔fork 对比与
// 收集/投放编排分别委托 diff / collect / deploy 层——这些层的契约桩尚未
// 施工时如实上抛 ErrNotImplemented，UI 由 Mock 后端承载（M-4）。层实现
// 落地后本文件零改动即接通真实链路。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"lzsync/internal/collect"
	"lzsync/internal/deploy"
	"lzsync/internal/diff"
	"lzsync/internal/filestore"
	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// reconcileKinds hub.ReconcileAgainst 已放开的对账类型（collection/
// beatmap_set）；skin/score 的对账查询面尚未在 hub/diff 放开，由本层
// reconcileSkins/reconcileScores 直查过渡承载（见 reconcile_skin_score.go），
// M3 设置类三类由 deploy.ReconcileSettings 过渡承载（同一文件）。
var reconcileKinds = []hub.EntityKind{hub.KindCollection, hub.KindBeatmapSet}

// uiKinds 预演/恢复组数据面覆盖的全部实体类型（§6.2 类型 Tab 的数据面：
// 谱面集/难度/收藏/皮肤/成绩/键位/规则集设置/ModPreset 八类）。难度不产
// 独立对账行——难度生命周期挂在可软删的父集上整集管理（§4.2），仅占
// Tab 位与 tombstone 遍历；M3 设置类三类经 deploy.ReconcileSettings
// 进预演面（不可见即不可勾选投放的缺口由此闭合）。
var uiKinds = []hub.EntityKind{
	hub.KindBeatmapSet, hub.KindBeatmap, hub.KindCollection, hub.KindSkin, hub.KindScore,
	hub.KindKeyBinding, hub.KindRulesetSetting, hub.KindModPreset,
}

// allReconcileItems 组装 hub→fork 的全量对账结果：hub 公共对账面（两类）
// + skin/score 过渡直查，按 (Kind, ContentKey) 确定性排序。
func (s *realCompare) allReconcileItems(ctx context.Context, target string) ([]hub.ReconcileItem, error) {
	items, err := s.deps.Hub.ReconcileAgainst(ctx, target, reconcileKinds)
	if err != nil {
		return nil, err
	}
	skins, err := s.reconcileSkins(ctx, target)
	if err != nil {
		return nil, err
	}
	items = append(items, skins...)
	scores, err := s.reconcileScores(ctx, target)
	if err != nil {
		return nil, err
	}
	items = append(items, scores...)
	settings, err := s.reconcileSettings(ctx, target)
	if err != nil {
		return nil, err
	}
	items = append(items, settings...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].ContentKey < items[j].ContentKey
	})
	return items, nil
}

// registry 已注册库（forkID → 解析结果）的运行时内存态；持久来源是
// config.json 的 forks（configStore，§0-G1）——启动时按配置回填、运行期
// 每次发现/注册/移除都同步写盘。
type registry struct {
	mu   sync.Mutex
	byID map[string]forks.LibraryRef
}

func (r *registry) put(ref forks.LibraryRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[ref.ForkID] = ref
}

func (r *registry) get(forkID string) (forks.LibraryRef, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref, ok := r.byID[forkID]
	return ref, ok
}

func (r *registry) all() []forks.LibraryRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]forks.LibraryRef, 0, len(r.byID))
	for _, ref := range r.byID {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ForkID < out[j].ForkID })
	return out
}

func (r *registry) remove(forkID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, forkID)
}

// newRealBackend 构造真实后端：加载 native 库（P-2 判定）→ 载入同步范围
// 配置（config.json；解析失败如实上抛，由 mock-fallback 呈现原因，§0-G1）
// → 配置端清单回注册表（RealmPath 不存在的条目留在配置但不进注册表）→
// 自动发现合并（不在配置且不在 ignored 的发现追加为双角色）→ 装配五服务
// → 恢复持久化的投放计划。
func newRealBackend(deps Deps, prog ProgressSink) (*Backend, error) {
	if deps.Hub == nil {
		return nil, errors.New("appsvc: 真实后端缺少 hub 存储")
	}
	if _, err := realmc.Load(); err != nil {
		return nil, fmt.Errorf("appsvc: native 库不可用（P-2）: %w", err)
	}
	if prog == nil {
		prog = func(ProgressEvent) {}
	}
	cfg, err := loadSyncConfig(deps.DataDir)
	if err != nil {
		return nil, err
	}
	reg := &registry{byID: map[string]forks.LibraryRef{}}
	// 配置端清单回注册表（身份取配置；版本重新探测走 Scan）。
	for _, role := range cfg.scope().Forks {
		if role.RealmPath == "" {
			continue
		}
		if _, err := os.Stat(role.RealmPath); err != nil {
			continue // RealmPath 已不存在：留在配置但不进注册表
		}
		reg.put(forks.LibraryRef{
			ForkID:    role.ForkID,
			RealmPath: role.RealmPath,
			DataDir:   filepath.Dir(role.RealmPath),
		})
	}
	// 发现失败（数据根不可读等）不阻塞后端构造：库管理页可手动注册。
	if refs, err := discoverRefs(deps); err == nil {
		for _, ref := range refs {
			ok, dErr := cfg.discoverFork(ref)
			if dErr != nil {
				return nil, dErr
			}
			if ok {
				reg.put(ref)
			}
		}
	}
	cmp := &realCompare{deps: deps, reg: reg, cfg: cfg}
	if err := cmp.loadPersistedPlan(); err != nil {
		return nil, err
	}
	b := &Backend{
		Library:  &realLibrary{deps: deps, reg: reg, cfg: cfg},
		Compare:  cmp,
		Sync:     &realSync{deps: deps, reg: reg, cfg: cfg, cmp: cmp, prog: prog},
		Export:   newRealExport(deps, reg),
		Settings: &realSettings{deps: deps, reg: reg, cfg: cfg},
		Mode:     "real",
		Reason:   "",
	}
	return b, nil
}

// discoverRefs 扫描数据根并把发现结果并入注册表（幂等重扫安全）。
// panic 兜底同 safeIdentify（目录内存在异常库文件时单文件识别缺陷
// 不应毁掉整次扫描）。
func discoverRefs(deps Deps) (refs []forks.LibraryRef, err error) {
	defer func() {
		if r := recover(); r != nil {
			refs, err = nil, fmt.Errorf("appsvc: 扫描 %s 时底层会话异常: %v", deps.DataRoot, r)
		}
	}()
	return forks.Discover(deps.DataRoot)
}

// --- LibraryService ---

type realLibrary struct {
	deps Deps
	reg  *registry
	cfg  *configStore
}

// Scan 扫描数据根（§6.1 自动发现）：不在配置且不在 ignored 的发现追加为
// 拉取+写入双角色并落盘；ignored 路径（用户 Remove 过的）不再加回。
func (s *realLibrary) Scan() ([]forks.LibraryRef, error) {
	refs, err := forks.Discover(s.deps.DataRoot)
	if err != nil {
		return nil, err
	}
	var visible []forks.LibraryRef
	for _, ref := range refs {
		ok, err := s.cfg.discoverFork(ref)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		s.reg.put(ref)
		visible = append(visible, ref)
	}
	if visible == nil {
		visible = []forks.LibraryRef{}
	}
	return visible, nil
}

// resolveRealmPath 把手动注册/指认的输入归一为库文件路径：展开 ~（GUI
// 输入框不经 shell），目录则定位其中的库文件（Ez 版本化名优先，否则
// client.realm）。不校验库文件是否存在——Register 另行把关，AssignFork
// 按路径登记（运行中锁库等场景允许先登记后探测）。
func resolveRealmPath(path string) (string, error) {
	path = expandHome(strings.TrimSpace(path))
	st, err := os.Stat(path)
	if err != nil || !st.IsDir() {
		return path, nil
	}
	realmPath, ok := forks.LocateRealmFile(path)
	if !ok {
		return "", fmt.Errorf("appsvc: 目录内没有找到库文件（client.realm）: %s", path)
	}
	return realmPath, nil
}

// expandHome 展开 ~ / ~/ 前缀；其余路径原样返回。
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~"))
}

// Register 手动注册：path 可给数据目录或库文件本身；识别失败返回 ForkID
// 为空串的 ref（携带解析后的 RealmPath 与原始版本供 UI 指认，§6.1）；
// 只有路径不可访问等硬错误走 error。
func (s *realLibrary) Register(path string) (forks.LibraryRef, error) {
	realmPath, err := resolveRealmPath(path)
	if err != nil {
		return forks.LibraryRef{}, err
	}
	if _, err := os.Stat(realmPath); err != nil {
		return forks.LibraryRef{}, fmt.Errorf("appsvc: 库文件不可访问: %w", err)
	}
	ref, _ := safeIdentify(realmPath, s.deps.Snapshots)
	if ref.ForkID != "" {
		s.reg.put(ref)
		if err := s.cfg.registerFork(ref); err != nil {
			return forks.LibraryRef{}, err
		}
	}
	return ref, nil
}

// safeIdentify 识别调用的 panic 兜底：realmc 对空/异常库文件的结构枚举
// 存在跨包缺陷（realm_get_class_keys n=0 时 &keys[0] 越界 panic，已记
// notes）；服务层绝不带着 panic 毁掉应用（P-2 不闪退的同一纪律）。
func safeIdentify(path string, snaps *forks.SnapshotStore) (ref forks.LibraryRef, err error) {
	defer func() {
		if r := recover(); r != nil {
			ref, err = forks.LibraryRef{}, fmt.Errorf("appsvc: 识别 %s 时底层会话异常: %v", path, r)
		}
	}()
	return forks.IdentifyCached(path, snaps)
}

// AssignFork 用户指认 fork 身份（绝不猜，§6.1）；指认后按 Profile 解码版本。
// path 与 Register 同样接受数据目录或库文件。
func (s *realLibrary) AssignFork(path, forkID string) (forks.LibraryRef, error) {
	p, ok := forks.Profile(forkID)
	if !ok {
		return forks.LibraryRef{}, fmt.Errorf("appsvc: 未知 fork %q（可选：%s）", forkID, forkIDs())
	}
	realmPath, err := resolveRealmPath(path)
	if err != nil {
		return forks.LibraryRef{}, err
	}
	dir := filepath.Dir(realmPath)
	var ver uint64
	if ref, err := safeIdentify(realmPath, s.deps.Snapshots); err == nil || ref.DiskVersion > 0 {
		ver = ref.DiskVersion
		if ref.VersionSource != "" && ref.DiskVersion > 0 {
			// 保留探测链已拿到的版本来源；仅身份采用用户指认。
			ref.ForkID = forkID
			ref.UpstreamVersion, ref.ForkRev = p.DecodeVersion(ref.DiskVersion)
			s.reg.put(ref)
			if err := s.cfg.registerFork(ref); err != nil {
				return forks.LibraryRef{}, err
			}
			return ref, nil
		}
	}
	ref := forks.LibraryRef{
		ForkID:        forkID,
		DataDir:       dir,
		RealmPath:     realmPath,
		DiskVersion:   ver,
		VersionSource: forks.VersionUnknownRunning,
	}
	ref.UpstreamVersion, ref.ForkRev = p.DecodeVersion(ver)
	s.reg.put(ref)
	if err := s.cfg.registerFork(ref); err != nil {
		return forks.LibraryRef{}, err
	}
	return ref, nil
}

// List 组装库卡片（状态灯数据，§6.1）。
func (s *realLibrary) List() []LibraryCard {
	refs := s.reg.all()
	running := map[string]bool{}
	if s.deps.Guard != nil {
		for _, name := range s.deps.Guard.Running() {
			running[name] = true
		}
	}
	cards := make([]LibraryCard, 0, len(refs))
	for _, ref := range refs {
		cards = append(cards, buildCard(ref, running))
	}
	return cards
}

// Remove 解除管理（不碰磁盘）：配置条目删除并把 RealmPath 记入 ignored，
// 自动发现不再加回（§0-G1；之后 Register 同路径会重新注册并摘除
// ignored 标记）。
func (s *realLibrary) Remove(forkID string) error {
	ref, ok := s.reg.get(forkID)
	if !ok {
		return fmt.Errorf("appsvc: 未注册的 fork %q", forkID)
	}
	s.reg.remove(forkID)
	return s.cfg.removeFork(forkID, ref.RealmPath)
}

// buildCard 从解析结果组装卡片：体积统计 + 版本标注 + 运行状态。
func buildCard(ref forks.LibraryRef, running map[string]bool) LibraryCard {
	card := LibraryCard{
		ForkID:          ref.ForkID,
		DataDir:         ref.DataDir,
		RealmPath:       ref.RealmPath,
		DiskVersion:     ref.DiskVersion,
		VersionSource:   ref.VersionSource,
		UpstreamVersion: ref.UpstreamVersion,
		ForkRev:         ref.ForkRev,
		VersionLabel:    versionLabel(ref),
		FilesDir:        filestore.ResolveFilesDir(ref.RealmPath),
		Registered:      true,
		RunningNames:    []string{},
	}
	if st, err := os.Stat(ref.RealmPath); err == nil {
		card.RealmSize = st.Size()
	}
	card.FilesSize, card.FilesCount = dirStats(card.FilesDir)
	if p, ok := forks.Profile(ref.ForkID); ok {
		for _, name := range p.ProcessNames {
			if running[name] {
				card.ProcessRunning = true
				card.RunningNames = append(card.RunningNames, name)
			}
		}
	}
	return card
}

// versionLabel 组装版本显示（Ez 解码呈现「52011 = 官方 52 + Ez 11」，§6.1）。
func versionLabel(ref forks.LibraryRef) string {
	if ref.DiskVersion == 0 {
		return "未知（运行中）"
	}
	if ref.ForkRev > 0 {
		return fmt.Sprintf("%d = 官方 %d + %s %d", ref.DiskVersion, ref.UpstreamVersion, ref.ForkID, ref.ForkRev)
	}
	return fmt.Sprintf("%d", ref.DiskVersion)
}

// dirStats 统计目录总字节与条目数（files/ 体积展示；链接按 Lstat 不跟进）。
func dirStats(dir string) (int64, int) {
	var size int64
	var count int
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 统计尽力而为：单点失败不毁整个卡片
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size += info.Size()
		count++
		return nil
	})
	return size, count
}

func forkIDs() string {
	ids := make([]string, 0)
	for _, p := range forks.All() {
		ids = append(ids, p.ID)
	}
	return fmt.Sprint(ids)
}

// --- CompareService ---

type realCompare struct {
	deps Deps
	reg  *registry
	cfg  *configStore // G12：数据页行态叠加三方判定要读同步范围

	mu   sync.Mutex
	plan deploy.Plan // 持久化的当前计划（plan.json）
}

// PreviewDeploy hub 真相 → 单 fork 投放预演（hub 内纯 SQL，§6.2）：
// 覆盖全部启用 kind（collection/beatmap_set 走 hub 公共对账面，skin/score
// 走本层过渡直查）。G12：行态再叠加三方判定结果——全局冲突/本端异值/
// 本端保护的单位所在行升「冲突」态（自动轮不写；勾选仍走手动处置）。
func (s *realCompare) PreviewDeploy(target string) ([]DiffRow, error) {
	ctx := context.Background()
	items, err := s.allReconcileItems(ctx, target)
	if err != nil {
		return nil, err
	}
	decs, ckIx := s.decisionOverlay(ctx)
	sum := newSummarizer(s.deps.Hub)
	rows := make([]DiffRow, 0, len(items))
	for _, it := range items {
		row := reconcileToRow(it, sum)
		if decs != nil && row.Category != CategoryRestore {
			overlayRowDecision(&row, decs, ckIx, target)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// decisionOverlay 三方判定在数据页的只读投影：参与端 = 设置里 Source||
// Target 的全部端，目标端全部按 live 计（本层只做行态标注不出计划）。
// 判定装载/计算失败返回 (nil,nil)——判定面的错误不挡对账行渲染，一轮
// 同步自身会如实报错（歧义单位 fail-closed 同理）。
func (s *realCompare) decisionOverlay(ctx context.Context) (map[unitKey]*unitDecision, map[string][]unitKey) {
	if s.cfg == nil {
		return nil, nil
	}
	sources := s.cfg.sourceForkIDs()
	targets := s.cfg.targetForkIDs()
	live := make(map[string]bool, len(targets))
	for _, t := range targets {
		live[t] = true
	}
	in, err := loadDecideInput(ctx, &s.deps, sources, targets, live,
		scopeKinds(s.cfg), false, nil)
	if err != nil {
		return nil, nil
	}
	units, ix := buildUnits(in)
	return decideAll(units, in.sources, in.targets), ix
}

// overlayRowDecision 行态叠加判定结果：行涉及的任一单位全局冲突、或本端
// 落 divergent/protected → 行升 CategoryConflict 并写清原因。version-update
// 行同时查 CurrentKey（目标持旧版、决定值在链尾时单位挂在身份键下，两端
// 内容键都进索引）。
func overlayRowDecision(row *DiffRow, decs map[unitKey]*unitDecision,
	ix map[string][]unitKey, target string) {
	seen := map[unitKey]bool{}
	check := func(ck string) bool {
		for _, uk := range ix[string(row.Kind)+"\x00"+ck] {
			if seen[uk] {
				continue
			}
			seen[uk] = true
			d := decs[uk]
			if d == nil {
				continue
			}
			switch {
			case d.conflict:
				row.SkipReason = "三方判定：多端取值分歧，本轮不写（§0.3 规则5；勾选即按 hub 变体处置）"
			case d.divergent[target]:
				row.SkipReason = "本端取值与 hub 不同且无同步基线（§0.3：保留差异；勾选即按 hub 变体处置）"
			case d.protected[target]:
				row.SkipReason = "本端自上轮同步后独立改动（§0.3 保护跳过；勾选即按 hub 变体处置）"
			default:
				continue
			}
			row.Category = CategoryConflict
			row.DefaultChecked = false
			return true
		}
		return false
	}
	if check(row.ContentKey) {
		return
	}
	if row.CurrentKey != "" {
		check(row.CurrentKey)
	}
}

// CompareForks fork↔fork 快照对比（委托 diff 层：两份 presence 差集；
// kinds 传递全部启用 kind，diff 层落地后零改动接通）。
func (s *realCompare) CompareForks(source, target string) ([]DiffRow, error) {
	entries, err := diff.CompareForks(context.Background(), s.deps.Hub, source, target, uiKinds)
	if err != nil {
		return nil, err
	}
	rows := make([]DiffRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, diffEntryToRow(e))
	}
	return rows, nil
}

// DeletedEntries 目标 fork 的「恢复（已删除）」组（tombstone 全量，HB2）：
// 遍历全部启用 kind（含暂无对账行的 beatmap——presence/tombstone 天然按
// kind 泛化，缺项自然为空）。
func (s *realCompare) DeletedEntries(target string) ([]DiffRow, error) {
	tombs, err := s.deps.Hub.Tombstones(context.Background(), target)
	if err != nil {
		return nil, err
	}
	sum := newSummarizer(s.deps.Hub)
	rows := []DiffRow{}
	for _, kind := range uiKinds {
		for key, deletedAt := range tombs[kind] {
			rows = append(rows, DiffRow{
				Kind:           kind,
				ContentKey:     key,
				Category:       CategoryRestore,
				Summary:        sum.summary(kind, key),
				DeletedAt:      deletedAt,
				DefaultChecked: false, // §6.2：恢复组默认不勾选
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Kind != rows[j].Kind {
			return rows[i].Kind < rows[j].Kind
		}
		return rows[i].ContentKey < rows[j].ContentKey
	})
	return rows, nil
}

// ForkPresence 查 hub presence 表（数据浏览页 x/y 数据源，§6.2）。
func (s *realCompare) ForkPresence(kind hub.EntityKind) ([]ForkPresenceEntry, error) {
	rows, err := s.deps.Hub.ForkPresence(context.Background(), kind)
	if err != nil {
		return nil, err
	}
	out := make([]ForkPresenceEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, ForkPresenceEntry{
			ForkID: r.ForkID, Kind: r.Kind, ContentKey: r.ContentKey, CollectedAt: r.CollectedAt,
		})
	}
	return out, nil
}

// CoverImage 谱面集封面图（DESIGN.md：bg/background 优先，否则 Files[0]
// 中的第一张图片；filestore 拿字节、转 data URL 喂前端 <img>）。找不到或
// 仓里没这份哈希就返回空串（行降级为纯色块，不报错）。
func (s *realCompare) CoverImage(contentKey string) (string, error) {
	if s.deps.Files == nil {
		return "", nil
	}
	// 拿谱面集当前版本行的 Files 列表。
	sets, err := s.deps.Hub.CurrentBeatmapSets(context.Background())
	if err != nil {
		return "", err
	}
	var target *hub.BeatmapSet
	for i := range sets {
		if sets[i].Set.Hash == contentKey {
			target = &sets[i].Set
			break
		}
	}
	if target == nil {
		return "", nil
	}
	// 选封面：文件名（不含扩展名）为 bg/background 优先，否则第一张图。
	// 图片扩展名白名单收紧到常见几类（避免 .osz 内打包的脚本/字体/视频混进来）。
	isImage := func(name string) bool {
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp":
			return true
		}
		return false
	}
	isBgName := func(name string) bool {
		base := strings.ToLower(name)
		base = strings.TrimSuffix(base, filepath.Ext(base))
		return base == "bg" || base == "background"
	}
	var chosen *hub.NamedFile
	for i := range target.Files {
		f := &target.Files[i]
		if isBgName(f.Filename) && isImage(f.Filename) {
			chosen = f
			break
		}
	}
	if chosen == nil {
		for i := range target.Files {
			f := &target.Files[i]
			if isImage(f.Filename) {
				chosen = f
				break
			}
		}
	}
	if chosen == nil {
		return "", nil
	}
	// 从 filestore 取字节。
	path := s.deps.Files.Store.Path(chosen.Hash)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil // 仓里没有/不可读 → 行降纯色
	}
	ext := strings.ToLower(filepath.Ext(chosen.Filename))
	mime := "image/jpeg"
	switch ext {
	case ".png":
		mime = "image/png"
	case ".webp":
		mime = "image/webp"
	case ".gif":
		mime = "image/gif"
	case ".bmp":
		mime = "image/bmp"
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)), nil
}

// BuildPlan 组装并持久化投放计划（§6.2 勾选 → §6.3 计划输入）。
// 动作面 = 收藏夹 + 谱面集（create/update/version-replace）+ 皮肤 + 成绩
//（create/update）+ M3 设置类三类（键位/规则集设置/ModPreset，
// create/update）——与 deploy 执行面（writer_set/writer_skin/writer_score/
// writer_setting）对齐（§5.1；预演面同源，三类经 allReconcileItems 可见
// 即可勾选投放）。
func (s *realCompare) BuildPlan(target string, sel []Selection) (deploy.Plan, error) {
	if target == "" {
		return deploy.Plan{}, errors.New("appsvc: 生成计划缺少目标 fork")
	}
	if len(sel) == 0 {
		return deploy.Plan{}, errors.New("appsvc: 未勾选任何条目")
	}
	ref, ok := s.reg.get(target)
	if !ok {
		return deploy.Plan{}, fmt.Errorf("appsvc: 目标 fork %q 未注册，请先在库管理页发现或注册", target)
	}
	// 以当前预演结果校验勾选（预览与执行之间目标库可能漂移，执行期还有
	// 现场复核兜底，§3.3）。
	items, err := s.allReconcileItems(context.Background(), target)
	if err != nil {
		return deploy.Plan{}, err
	}
	byKey := map[string]hub.ReconcileItem{}
	for _, it := range items {
		byKey[string(it.Kind)+"\x00"+it.ContentKey] = it
	}
	sum := newSummarizer(s.deps.Hub)

	var raw []deploy.PlanItem
	seen := map[string]bool{}
	for _, sl := range sel {
		key := string(sl.Kind) + "\x00" + sl.ContentKey
		if seen[key] {
			continue // 同键去重
		}
		seen[key] = true
		it, ok := byKey[key]
		if !ok {
			raw = append(raw, deploy.PlanItem{
				Kind: sl.Kind, ContentKey: sl.ContentKey, Action: deploy.ActionSkip,
				Summary:    sum.summary(sl.Kind, sl.ContentKey),
				SkipReason: "预演数据中不存在该键（快照可能已过期，请刷新预览）",
			})
			continue
		}
		raw = append(raw, planItemFrom(it, sum))
	}
	plan, err := deploy.NewPlan(target, raw)
	if err != nil {
		return deploy.Plan{}, err
	}
	plan.Target = ref // 投放执行需要 RealmPath 落盘位置（deploy.Plan 契约）
	if err := s.persistPlan(plan); err != nil {
		return deploy.Plan{}, err
	}
	return plan, nil
}

// planItemFrom 单条对账结果 → 计划项（M2 动作面：收藏夹/谱面集/皮肤/成绩）。
// 勾选恢复组 = 用户显式拍板投放（Restore 标记，投放成功清 tombstone）；
// 勾选弱键疑似 = 已拍板按版本更新处理（NeedsConfirm 置位留痕，§5.4）。
// 版本更新的 ContentKey 采用 hub 链尾键（写入面 canonical 载荷的读取键），
// 目标现场旧版本键落 OldContentKey 供执行期重寻址（§3.3）。
func planItemFrom(it hub.ReconcileItem, sum *summarizer) deploy.PlanItem {
	item := deploy.PlanItem{
		Kind:       it.Kind,
		ContentKey: it.ContentKey,
		Summary:    it.Summary, // 过渡直查面（设置类）自带键级摘要；公共面为空回落 summarizer
		Restore:    it.Restorable,
	}
	if item.Summary == "" {
		item.Summary = sum.summary(it.Kind, it.ContentKey)
	}
	// 版本更新（谱面集专属语义，§4.4）：强键默认执行；弱键勾选即拍板。
	if it.Category == hub.ReconcileVersionUpdate {
		item.ContentKey, item.OldContentKey = it.CurrentKey, it.ContentKey
		item.Action = deploy.ActionVersionReplace
		item.Summary = sum.summary(it.Kind, it.CurrentKey)
		if it.Evidence == EvidenceWeak {
			item.NeedsConfirm = true
		}
		return item
	}
	switch it.Kind {
	case hub.KindCollection, hub.KindSkin, hub.KindScore,
		hub.KindKeyBinding, hub.KindRulesetSetting, hub.KindModPreset:
		// 无版本链实体：create（恢复组勾选即拍板）/conflict（默认 hub 覆盖，
		// 就地更新白名单列，§5.4）；无版本更新语义。
		switch it.Category {
		case hub.ReconcileCreate:
			item.Action = deploy.ActionCreate
		case hub.ReconcileConflict:
			item.Action = deploy.ActionUpdate
		default:
			item.Action, item.SkipReason = deploy.ActionSkip,
				string(it.Kind)+" 无版本更新语义（内容键即身份，§4.2）"
		}
	case hub.KindBeatmapSet:
		switch it.Category {
		case hub.ReconcileCreate:
			item.Action = deploy.ActionCreate
		case hub.ReconcileConflict:
			item.Action = deploy.ActionUpdate // 冲突默认 hub 覆盖（就地更新白名单列，§5.4）
		default:
			item.Action, item.SkipReason = deploy.ActionSkip, "未知对账分类 "+string(it.Category)
		}
	default:
		// 难度（对账面不产独立行）与 M3 类型：不静默吞掉（§1.5-4）。
		item.Action, item.SkipReason = deploy.ActionSkip,
			string(it.Kind)+" 投放属 M3 写路径（§5.1 分阶段启用）"
	}
	return item
}

// CurrentPlan 返回持久化的当前计划。
func (s *realCompare) CurrentPlan() (deploy.Plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.plan.Items) == 0 && s.plan.TargetFork == "" {
		return deploy.Plan{}, ErrNoPlan
	}
	return s.plan, nil
}

func (s *realCompare) planPath() string {
	return filepath.Join(s.deps.DataDir, "plan.json")
}

func (s *realCompare) persistPlan(plan deploy.Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("appsvc: 序列化投放计划: %w", err)
	}
	if err := os.WriteFile(s.planPath(), b, 0o644); err != nil {
		return fmt.Errorf("appsvc: 写入投放计划 %s: %w", s.planPath(), err)
	}
	s.plan = plan
	return nil
}

func (s *realCompare) loadPersistedPlan() error {
	b, err := os.ReadFile(s.planPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("appsvc: 读取投放计划 %s: %w", s.planPath(), err)
	}
	var plan deploy.Plan
	if err := json.Unmarshal(b, &plan); err != nil {
		return nil // 坏文件按无计划处理（重建即可，不阻塞启动）
	}
	s.plan = plan
	return nil
}

// reconcileToRow hub 对账条目 → UI 行（含恢复组归并与默认勾选）。
func reconcileToRow(it hub.ReconcileItem, sum *summarizer) DiffRow {
	row := DiffRow{
		Kind:       it.Kind,
		ContentKey: it.ContentKey,
	}
	fields := make([]FieldDiff, 0, len(it.ConflictFields))
	for _, f := range it.ConflictFields {
		fields = append(fields, FieldDiff{Field: f.Field, HubValue: f.HubValue, ForkValue: f.ForkValue})
	}
	row.ConflictFields = fields
	row.IdentityKey, row.Evidence = it.IdentityKey, it.Evidence
	row.CurrentKey = it.CurrentKey
	row.DeletedAt = it.DeletedAt
	// 默认勾选（§6.2 + §0-B4）：新增默认勾选；冲突默认不勾（无三方基线
	// 裁决，判不准不动——显式勾选即选定 hub 变体）；版本更新强键勾选、
	// 弱键需拍板不勾；恢复组默认不勾。
	checked := false
	switch {
	case it.Restorable:
		row.Category = CategoryRestore
	case it.Category == hub.ReconcileCreate:
		row.Category, checked = CategoryCreate, true
	case it.Category == hub.ReconcileConflict:
		row.Category = CategoryConflict
		row.SkipReason = "冲突：各端取值不同，勾选投放即按 hub 值覆盖（§0 B4）"
	case it.Category == hub.ReconcileVersionUpdate:
		row.Category = CategoryVersionUpdate
		checked = it.Evidence == EvidenceStrong
		if it.Evidence == EvidenceWeak {
			row.SkipReason = "元数据弱键「疑似」版本更新，需用户拍板（§5.4 三选一）"
		}
	}
	row.DefaultChecked = checked
	row.Summary = it.Summary
	if row.Summary == "" {
		row.Summary = sum.summary(it.Kind, it.ContentKey)
	}
	return row
}

// summarizer 内容键 → 人类可读摘要（§6.2）。收藏夹内容键即 Name（§4.2
// 第一层），键本身就是摘要，无需查库；谱面集/皮肤/成绩摘要走 hub 查询面
//（当前版本集合的 Title/Artist 规范值、皮肤 Name/Creator、成绩规则集/
// 分数/日期）。
type summarizer struct {
	setTitles map[string]string // hash → "Artist - Title（Author）"
	skinNames map[string]string // hash → "Name（Creator）"
	scoreSum  map[string]string // 组合键 → "规则集 · N 分 · 日期"
}

func newSummarizer(h *hub.Store) *summarizer {
	s := &summarizer{
		setTitles: map[string]string{},
		skinNames: map[string]string{},
		scoreSum:  map[string]string{},
	}
	ctx := context.Background()
	// 标题索引覆盖全表而非仅链尾：待处理/冲突行指向的常是版本链的另一端
	// （历史版本），只索引链尾会让这些行退化成短哈希。
	if rows, err := h.DB().QueryContext(ctx,
		`SELECT content_hash, payload FROM beatmap_set`); err == nil {
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				break // 摘要尽力而为：单行失败退回内容键前缀
			}
			var bs struct{ Artist, Title, AuthorUsername string }
			if json.Unmarshal([]byte(v), &bs) == nil && bs.Title != "" {
				s.setTitles[k] = fmt.Sprintf("%s - %s（%s）", bs.Artist, bs.Title, bs.AuthorUsername)
			}
		}
		rows.Close()
	}
	if rows, err := h.DB().QueryContext(ctx,
		`SELECT content_hash, payload FROM skin`); err == nil {
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				break // 摘要尽力而为：单行失败退回内容键前缀
			}
			var sk hub.Skin
			if json.Unmarshal([]byte(v), &sk) == nil {
				s.skinNames[k] = fmt.Sprintf("皮肤「%s」", sk.Name)
				if sk.Creator != "" {
					s.skinNames[k] += "（" + sk.Creator + "）"
				}
			}
		}
		rows.Close()
	}
	if rows, err := h.DB().QueryContext(ctx,
		`SELECT beatmap_hash, content_hash, payload FROM score`); err == nil {
		for rows.Next() {
			var bh, ch, v string
			if err := rows.Scan(&bh, &ch, &v); err != nil {
				break
			}
			var sc hub.Score
			if json.Unmarshal([]byte(v), &sc) == nil {
				s.scoreSum[hub.ScoreContentKey(bh, ch)] = fmt.Sprintf("成绩 %s · %d 分 · %s",
					sc.RulesetShortName, sc.TotalScore, sc.Date.Format("2006-01-02"))
			}
		}
		rows.Close()
	}
	return s
}

func (s *summarizer) summary(kind hub.EntityKind, key string) string {
	switch kind {
	case hub.KindCollection:
		return "收藏夹「" + key + "」"
	case hub.KindBeatmapSet:
		if t, ok := s.setTitles[key]; ok {
			return t
		}
		return "谱面集 " + shortKey(key)
	case hub.KindSkin:
		if t, ok := s.skinNames[key]; ok {
			return t
		}
		return "皮肤 " + shortKey(key)
	case hub.KindScore:
		if t, ok := s.scoreSum[key]; ok {
			return t
		}
		return "成绩 " + shortKey(key)
	default:
		return string(kind) + " " + shortKey(key)
	}
}

// shortKey 长哈希截断显示。
func shortKey(key string) string {
	if len(key) > 12 {
		return key[:12] + "…"
	}
	return key
}

// --- SyncService ---

type realSync struct {
	deps  Deps
	reg   *registry
	cfg   *configStore
	cmp   *realCompare
	prog  ProgressSink
	cRun  *collect.Runner
	dRun  *deploy.Runner

	mu     sync.Mutex
	cancel context.CancelFunc
	status SyncStatus
	runs   map[string]*preparedRun // 已准备的一轮同步（只保留最新一轮）
}

// Collect 对单个 fork 执行收集（§6.3 收集阶段）。游戏运行中被守卫拦截；
// 编排主体委托 collect.Runner（桩未施工时如实上抛其错误）。
func (s *realSync) Collect(forkID string) (collect.Report, error) {
	if err := s.begin("collect", forkID); err != nil {
		return collect.Report{}, err
	}
	defer s.finish()
	ref, ok := s.reg.get(forkID)
	if !ok {
		return collect.Report{}, fmt.Errorf("appsvc: 未注册的 fork %q，请先在库管理页发现或注册", forkID)
	}
	if err := s.guardClear(forkID); err != nil {
		return collect.Report{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.setCancel(cancel)
	defer cancel()
	s.prog(ProgressEvent{Service: "collect", Stage: string(collect.StageProject), ForkID: forkID, At: time.Now(),
		Message: fmt.Sprintf("开始收集 %s", forkID)})
	report, err := s.cRunner().Collect(ctx, ref, func(stage collect.Stage, current, total int) {
		s.prog(ProgressEvent{Service: "collect", Stage: string(stage), ForkID: forkID, Current: current, Total: total,
			At: time.Now(), Message: fmt.Sprintf("%s：%d/%d", stageLabel(stage), current, total)})
	})
	if report.Entities == nil {
		report.Entities = map[hub.EntityKind]int{}
	}
	if report.AdoptModeCounts == nil {
		report.AdoptModeCounts = map[filestore.LinkMode]int{}
	}
	if report.Skips == nil {
		report.Skips = []hub.SkipStat{}
	}
	return report, err
}

// DeployCurrentPlan 执行当前计划（§6.3 投放执行：守卫 → 备份 → 批事务）。
func (s *realSync) DeployCurrentPlan() (deploy.Report, error) {
	plan, err := s.cmp.CurrentPlan()
	if err != nil {
		return deploy.Report{}, err
	}
	if err := s.begin("deploy", plan.TargetFork); err != nil {
		return deploy.Report{}, err
	}
	defer s.finish()
	if err := s.guardClear(plan.TargetFork); err != nil {
		return deploy.Report{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.setCancel(cancel)
	defer cancel()
	s.prog(ProgressEvent{Service: "deploy", Stage: string(deploy.StageGuard), ForkID: plan.TargetFork, At: time.Now(),
		Message: fmt.Sprintf("开始投放到 %s（%d 项）", plan.TargetFork, len(plan.Items))})
	report, err := s.dRunner().Deploy(ctx, plan, func(stage deploy.Stage, batchIndex, batchTotal, current, total int) {
		s.prog(ProgressEvent{Service: "deploy", Stage: string(stage), ForkID: plan.TargetFork, Current: current, Total: total,
			At: time.Now(), Message: fmt.Sprintf("批 %d/%d · %s：%d/%d", batchIndex, batchTotal, stageLabel(stage), current, total)})
	})
	if err == nil {
		// game.ini 白名单键同步（§5.1-M3/S12，deploy/gameini.go 的设计
		// 意图：与 Deploy 组合完成一次投放）。单目标投放兼容路径：无三方
		// 判定上下文，取快照最新端的全部可同步键逐键写入（源记为该端）。
		if s.deps.GameIni != nil {
			if src := s.gameIniSourceFork(plan.TargetFork); src != "" {
				if vals, _, ok, lvErr := s.deps.GameIni.Load(src); lvErr == nil && ok {
					dec := map[string]deploy.IniValue{}
					for k, v := range vals {
						dec[k] = deploy.IniValue{Value: v, SourceFork: src}
					}
					// peers 仅目标端（单投路径无全局交集上下文）。
					gi, giErr := deploy.SyncGameIniValues(plan.Target, dec, nil, s.deps.Backups, s.deps.Guard)
					if giErr != nil {
						return report, giErr
					}
					report.GameIni = &gi
				}
			}
		}
	}
	if report.Skipped == nil {
		report.Skipped = []hub.SkipStat{}
	}
	if report.Applied == nil {
		report.Applied = []deploy.ItemResult{}
	}
	if report.FailedBatches == nil {
		report.FailedBatches = []deploy.BatchFailure{}
	}
	if report.NotRun == nil {
		report.NotRun = []deploy.PlanItem{}
	}
	if report.Repointed == nil {
		report.Repointed = []deploy.RepointedItem{}
	}
	return report, err
}

// Cancel 取消当前长任务（批间生效，批事务不可打断，§3.3）。
func (s *realSync) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

// Running 当前任务状态。
func (s *realSync) Running() SyncStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// RestoreBackup 从备份还原（guard.Backups 委托；还原前自动再备份）。
func (s *realSync) RestoreBackup(forkID, backupPath string) error {
	ref, ok := s.reg.get(forkID)
	if !ok {
		return fmt.Errorf("appsvc: 未注册的 fork %q", forkID)
	}
	if s.deps.Backups == nil {
		return errors.New("appsvc: 未配置备份目录")
	}
	return s.deps.Backups.Restore(backupPath, ref.RealmPath)
}

func (s *realSync) cRunner() *collect.Runner {
	if s.cRun == nil {
		s.cRun = collect.NewRunner(collect.Deps{
			Hub: s.deps.Hub, Files: s.deps.Files,
			Snapshots: s.deps.Snapshots, Guard: s.deps.Guard,
			GameIni: s.deps.GameIni,
		})
	}
	return s.cRun
}

func (s *realSync) dRunner() *deploy.Runner {
	if s.dRun == nil {
		s.dRun = deploy.NewRunner(deploy.Deps{
			Hub: s.deps.Hub, Files: s.deps.Files,
			Backups: s.deps.Backups, Guard: s.deps.Guard,
		})
	}
	return s.dRun
}

// gameIniSourceFork 选 game.ini 快照源（deploy/gameini.go 的 source fork
// 决策）：注册表内非目标 fork 中快照最新者——ini 键无版本链，LastWrite-
// wins 的保守近似。无任何源快照返回空（跳过 ini 段）。
func (s *realSync) gameIniSourceFork(target string) string {
	best, bestAt := "", time.Time{}
	for _, ref := range s.reg.all() {
		if ref.ForkID == target {
			continue
		}
		_, at, ok, err := s.deps.GameIni.Load(ref.ForkID)
		if err != nil || !ok {
			continue
		}
		if best == "" || at.After(bestAt) {
			best, bestAt = ref.ForkID, at
		}
	}
	return best
}

// guardClear 逐端守卫预检（§0-B1/G3）：只检测该 fork Profile 名单内的
// 进程；未知 fork 回落全名单（EnsureClearFork 语义）。nil 守卫恒放行。
func (s *realSync) guardClear(forkID string) error {
	if s.deps.Guard == nil {
		return nil
	}
	return s.deps.Guard.EnsureClearFork(forkID)
}

func (s *realSync) begin(kind, forkID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Busy {
		return ErrBusy
	}
	s.status = SyncStatus{Busy: true, Kind: kind, ForkID: forkID}
	return nil
}

func (s *realSync) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = nil
	s.status = SyncStatus{}
}

func (s *realSync) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = cancel
}

// stageLabel 阶段中文名（进度消息用）。
func stageLabel(stage any) string {
	switch v := stage.(type) {
	case collect.Stage:
		switch v {
		case collect.StageProject:
			return "只读投影"
		case collect.StageApply:
			return "入 hub"
		case collect.StageAdopt:
			return "收仓+回链"
		}
	case deploy.Stage:
		switch v {
		case deploy.StageGuard:
			return "守卫复查"
		case deploy.StageBackup:
			return "自动备份"
		case deploy.StageLink:
			return "建链接"
		case deploy.StageWrite:
			return "事务写入"
		case deploy.StageVerify:
			return "结构校验"
		}
	}
	return fmt.Sprint(stage)
}

// --- SettingsService ---

type realSettings struct {
	deps Deps
	reg  *registry
	cfg  *configStore
}

// GetSyncScope 同步范围（§0-G1）。
func (s *realSettings) GetSyncScope() (SyncScope, error) {
	return s.cfg.scope(), nil
}

// SetSyncScope 校验并持久化同步范围：未知板块报错；未注册端报错；落盘
// 走配置原子写（configStore.applyScope）。
func (s *realSettings) SetSyncScope(scope SyncScope) error {
	for _, tg := range scope.Sections {
		if !knownSection(tg.Section) {
			return fmt.Errorf("appsvc: 未知同步板块 %q", tg.Section)
		}
	}
	for _, fr := range scope.Forks {
		if _, ok := s.reg.get(fr.ForkID); !ok {
			return fmt.Errorf("appsvc: 未注册的 fork %q，无法设置同步角色", fr.ForkID)
		}
	}
	return s.cfg.applyScope(scope, func(forkID string) string {
		if ref, ok := s.reg.get(forkID); ok {
			return ref.RealmPath
		}
		return ""
	})
}

// GetBaseline 基准端。
func (s *realSettings) GetBaseline() string {
	return s.cfg.baseline()
}

// SetBaseline 持久化基准端（校验注册端）。
func (s *realSettings) SetBaseline(forkID string) error {
	known := map[string]bool{}
	for _, ref := range s.reg.all() {
		known[ref.ForkID] = true
	}
	return s.cfg.setBaseline(forkID, known)
}

// Get 组装设置页数据（§6.4 M1 骨架，只读显示）。
func (s *realSettings) Get() (SettingsInfo, error) {
	info := SettingsInfo{
		Mode:         "real",
		DataDir:      s.deps.DataDir,
		HubDBPath:    filepath.Join(s.deps.DataDir, "hub.db"),
		BackupsDir:   "",
		GuardNames:   []string{},
		LinkNote:     "按 §5.3 自动分级：符号链接 → 硬链接（同卷）→ 复制兜底；探测不可用时逐级降级",
		NativeLibOK:  true,
		BaselineFork: s.cfg.baseline(),
	}
	if s.deps.Backups != nil {
		info.BackupsDir = s.deps.Backups.Dir
		if entries, err := os.ReadDir(info.BackupsDir); err == nil {
			info.BackupsCount = len(entries)
		}
	}
	names := map[string]bool{}
	for _, p := range forks.All() {
		for _, n := range p.ProcessNames {
			if !names[n] {
				names[n] = true
				info.GuardNames = append(info.GuardNames, n)
			}
		}
	}
	sort.Strings(info.GuardNames)
	if s.deps.Files != nil {
		if caps, err := s.deps.Files.Probe(); err == nil {
			info.LinkSymlink, info.LinkHardlink = caps.Symlink, caps.Hardlink
		} else {
			info.LinkNote = "链接能力探测失败（" + err.Error() + "）；投放时将降级为复制"
		}
	}
	return info, nil
}
