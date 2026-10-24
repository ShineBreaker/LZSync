package forks

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"lzsync/internal/realmc"
)

// ErrUnidentified 库身份识别失败（未知 schema / 识别歧义）：调用方须向用户
// 呈现原始版本号并要求指认 fork，绝不猜（§6.1）。
var ErrUnidentified = errors.New("forks: 无法识别库身份，需用户指认")

// VersionSource 是磁盘 schema 版本值的来源标注（§3.2 版本探测降级链）。
type VersionSource string

const (
	VersionFromOpen       VersionSource = "realm-open"      // 现场开库探测（最强）
	VersionFromFilename   VersionSource = "filename"        // 文件名推断（仅 Ez 版本化文件名可退）
	VersionFromSnapshot   VersionSource = "snapshot-cache"  // 结构档案缓存
	VersionUnknownRunning VersionSource = "unknown-running" // 游戏运行中，开库探测失败
)

// LibraryRef 是一个已注册（或候选）fork 库的解析结果。
type LibraryRef struct {
	ForkID          string        // 命中的 Profile ID；识别失败为 ""
	DataDir         string        // fork 数据目录绝对路径（files/ 的父目录）
	RealmPath       string        // 库文件绝对路径
	DiskVersion     uint64        // 磁盘 schema 版本（原始值）
	VersionSource   VersionSource // 版本值来源标注（UI 呈现，§6.1）
	UpstreamVersion int           // DecodeVersion 解码后的官方基线版本
	ForkRev         int           // fork 自身修订号（非 Ez 恒 0）
}

// structureView 是一次开库探测读出的结构投影，指纹判定输入。
type structureView struct {
	version uint64
	classes map[string]bool
	props   map[string]map[string]bool
}

// Discover 扫描数据根目录，按 Profile 内置目录名识别四端候选（§3.2 发现；
// P-3 的平台数据根解析由调用方完成并传入）。Ez 侧识别 client_<v>.realm
// 版本化主库（旧号文件是前驱链遗留，fork-ez2lazer §2.2）。目录身份来自
// DataDirName（release 与 -development 变体都认）；版本按降级链尽力探测
//（开库失败时非 Ez 端呈 unknown-running，Ez 端可退到文件名推断）。
// 数据根不存在视为「没装任何 fork」返回空集，不是错误。
func Discover(dataRoot string) ([]LibraryRef, error) {
	entries, err := os.ReadDir(dataRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("forks: 扫描数据根 %s: %w", dataRoot, err)
	}
	var refs []LibraryRef
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, ok := profileByDirName(e.Name())
		if !ok {
			continue
		}
		dir := filepath.Join(dataRoot, e.Name())
		realmPath, ok := locateRealm(dir, p)
		if !ok {
			continue // 目录在但无库文件，不是候选
		}
		ref, _ := IdentifyCached(realmPath, nil)
		if ref.ForkID == "" {
			// 探测未定身份：发现场景以目录名为身份兜底（Discover 的契约
			// 语义即「按 Profile 内置目录名识别」；单文件指纹判定属 Identify）。
			ref.ForkID = p.ID
			ref.UpstreamVersion, ref.ForkRev = p.DecodeVersion(ref.DiskVersion)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// Identify 对单个库文件做身份识别（手动注册路径，appsvc.Register 的底层）：
// 版本探测（降级链见 VersionSource 常量）+ fork 指纹判定（fork 身份 = 版本
// 编码 + 结构指纹）。识别失败返回 ErrUnidentified，此时返回的 LibraryRef
// 携带原始 DiskVersion 供 UI 呈现（ForkID 为空）。
// 无结构档案缓存段（Identify 即 IdentifyCached(path, nil)）：完整四段
// 降级链走 IdentifyCached 并传入 SnapshotStore。
func Identify(realmPath string) (LibraryRef, error) {
	return IdentifyCached(realmPath, nil)
}

// IdentifyCached 是带结构档案缓存段的完整版本探测降级链（§3.2）：
// ① 现场开库探测（读磁盘版本 + 结构指纹判定；游戏运行时因 Realm 进程锁
// 失败，go-realm-options §1.3-7）→ ② 文件名推断（仅 Ez 版本化文件名可退，
// ezrealmsync-design §3.1-3）→ ③ 结构档案缓存（snaps 按 SourcePath 反查
// 上次采集记录的 (fork, diskVersion)；snaps 为 nil 时跳过本段）→ ④ 未知
//（运行中）。native 库不可用（P-2 降级模式）时开库段整体跳过、不报错。
func IdentifyCached(realmPath string, snaps *SnapshotStore) (LibraryRef, error) {
	dir := filepath.Dir(realmPath)

	// ① 开库探测。realm 的「只读」也只是逻辑只读（打开会在库旁创建
	// .lock/.management 伴生文件，S10 不同步它们），故路径须可写。
	if lib, err := realmc.Load(); err == nil {
		if r, err := lib.OpenDynamicReadOnly(realmPath); err == nil {
			view, verr := readStructure(r)
			ver := r.SchemaVersion()
			r.Close()
			if verr == nil {
				if id, ok := matchProfile(view, filepath.Base(dir)); ok {
					return buildRef(id, dir, realmPath, ver, VersionFromOpen), nil
				}
				// 版本读到了但身份判定失败：带原始版本交用户指认（§6.1）。
				return buildRef("", dir, realmPath, ver, VersionFromOpen), ErrUnidentified
			}
		}
		// 开库或读结构失败（典型：运行中的进程锁）→ 降级，不区分成因
		//（降级链只有 unknown-running 一档，§6.1）。
	}

	// ② 文件名推断：client_<v>.realm 且 v>=1000 即 Ez 版本编码（§2.1），
	// 编码本身即身份，无需开库佐证。
	if v, ok := parseVersionedRealmName(realmPath); ok && v >= 1000 {
		return buildRef("ez2lazer", dir, realmPath, v, VersionFromFilename), nil
	}

	// ③ 结构档案缓存。
	if snaps != nil {
		if snap := snaps.findBySourcePath(realmPath); snap != nil {
			return buildRef(snap.ForkID, dir, realmPath, snap.DiskVersion, VersionFromSnapshot), nil
		}
	}

	// ④ 未知（运行中）。
	return buildRef("", dir, realmPath, 0, VersionUnknownRunning), ErrUnidentified
}

// buildRef 组装 LibraryRef，并按命中 profile 的 DecodeVersion 解码。
func buildRef(forkID, dataDir, realmPath string, v uint64, src VersionSource) LibraryRef {
	ref := LibraryRef{
		ForkID:        forkID,
		DataDir:       dataDir,
		RealmPath:     realmPath,
		DiskVersion:   v,
		VersionSource: src,
	}
	if p, ok := Profile(forkID); ok {
		ref.UpstreamVersion, ref.ForkRev = p.DecodeVersion(v)
	}
	return ref
}

// profileByDirName 按数据目录名反查 Profile（含 -development 变体）。
func profileByDirName(name string) (ForkProfile, bool) {
	for _, p := range profiles {
		if p.ownsDir(name) {
			return p, true
		}
	}
	return ForkProfile{}, false
}

// locateRealm 在数据目录内定位库文件。非 Ez 端按 RealmFilePat 字面名
//（client.realm）；Ez 端取版本化 client_<v>.realm 中版本最大者——Ez 客户端
//（含 release）打开的总是其当前版本号文件，旧号是前驱搜索链遗留
//（client_52010 → … → client_52 → client_51011 → … → client.realm，
// fork-ez2lazer §2.2）；无版本化文件时回退链尾 client.realm。
func locateRealm(dir string, p ForkProfile) (string, bool) {
	if p.ID == "ez2lazer" {
		entries, err := os.ReadDir(dir)
		if err == nil {
			var best uint64
			var bestName string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if v, ok := parseVersionedRealmName(e.Name()); ok && v > best {
					best, bestName = v, e.Name()
				}
			}
			if bestName != "" {
				return filepath.Join(dir, bestName), true
			}
		}
	}
	path := filepath.Join(dir, "client.realm")
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	return path, true
}

// parseVersionedRealmName 解析 client_<v>.realm 形态的版本化库名；
// 非该形态或后缀非十进制数字返回 false。
func parseVersionedRealmName(name string) (uint64, bool) {
	base := filepath.Base(name)
	if !strings.HasPrefix(base, "client_") || !strings.HasSuffix(base, ".realm") {
		return 0, false
	}
	v, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(base, "client_"), ".realm"), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// matchProfile 结构指纹 + 版本 + 目录名三信号判定 fork 身份：
//  1. 指纹优先：任一 Profile 声明的专有表/列出现在结构中即命中
//     （ezrealmsync-design §6.2-2 的教诲：结构特征优先于「<1000 即官方」
//     的单叉判断；误把 fork 库当官方基线会污染写集，代价远大于反向）。
//  2. 无指纹时按版本匹配候选：Ez 编码版本（DecodeVersion 解出 forkRev>0）
//     编码本身即身份，无需目录佐证；裸版本需数据目录名消歧——lazer 与
//     g0v0 结构全等（fork-g0v0 §2），目录名是唯一区分信号。
//  3. 候选不唯一或版本无主 → 不识别（绝不猜，§6.1）。
func matchProfile(v structureView, dirName string) (string, bool) {
	for _, p := range profiles {
		if p.matchesFingerprint(v) {
			return p.ID, true
		}
	}
	var cands []ForkProfile
	for _, p := range profiles {
		if p.DiskVersion != v.version {
			continue
		}
		if _, forkRev := p.DecodeVersion(v.version); forkRev > 0 || p.ownsDir(dirName) {
			cands = append(cands, p)
		}
	}
	if len(cands) == 1 {
		return cands[0].ID, true
	}
	return "", false
}

// readStructure 从动态会话读出结构投影（全部 class 与属性名）。
func readStructure(r *realmc.Realm) (structureView, error) {
	classes, err := r.Classes()
	if err != nil {
		return structureView{}, err
	}
	view := structureView{
		version: r.SchemaVersion(),
		classes: make(map[string]bool, len(classes)),
		props:   make(map[string]map[string]bool, len(classes)),
	}
	names := make([]string, 0, len(classes))
	for _, c := range classes {
		view.classes[c.Name] = true
		names = append(names, c.Name)
	}
	// 按名稳定排序，保证采集行为可复现（结构档案 golden 比对友好）。
	sort.Strings(names)
	for _, name := range names {
		key, err := r.FindClass(name)
		if err != nil {
			return structureView{}, err
		}
		props, err := r.Properties(key)
		if err != nil {
			return structureView{}, err
		}
		set := make(map[string]bool, len(props))
		for _, pr := range props {
			set[pr.Name] = true
		}
		view.props[name] = set
	}
	return view, nil
}
