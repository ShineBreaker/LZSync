// Package fixture 是测试库构造（design.md §8.2）：测试库是 LZSync 自产的
// 合成产物，不是用户库。经 realm-core C API 显式 schema + 版本号新建空库
// 并播种行（该能力已由 PoC 第二道验收线实证）；产品路径仍严格钉死动态
// 打开（S3 不因测试破例）。
//
// 写入能力边界（施工期对 realmc 绑定面的实测结论，见 probe 历史）：
//   - realm_set_value 仅 SetInt64/SetString 可用——bool/timestamp/double
//     列声明类型忠实但值留 realm 默认（SetBool/SetTimestamp 是 realmc
//     契约桩，double 无导出构造面）；
//   - realm_object_create 仅适用无主键类（errno=3005），realmc 未绑定
//     realm_object_create_with_primary_key——镜像一律不声明主键，行身份
//     走内容哈希列（与本工具的对账键一致，design §4.2）；
//   - 集合列（BeatmapMD5Hashes/Pauses/UserTags 等）无法声明——收藏夹
//     成员、成绩暂停列表暂不落库，留待 realmc 扩展构造面。
package fixture

import (
	"context"
	"fmt"
	"path/filepath"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// Seed 种子数据（自洽图，§8.2-3：哈希与行一致——内容寻址不要求「能玩的
// 谱面」，只要求哈希与行一致）。基础载体（Collections/BeatmapSets/Files）
// 之外，差异夹具载体按 §8.2-4 清单扩展承载。
type Seed struct {
	Collections []hub.Collection
	BeatmapSets []hub.BeatmapSet
	// Files 哈希 → 文件字节；Build 落盘 files/<h0>/<h01>/<hash> 并校验
	// 键与内容一致。内置皮肤/官方四规则集行按固定 Guid/ShortName 播种
	// （lazer-baseline §2.9/§2.10），不走本结构。
	Files map[string][]byte

	// Scores 成绩行（差异夹具载体：JudgeMode≠0、未知 StatisticsJson 键、
	// 非官方 mod、指向市场规则集等）。ReplayHashes 的每个哈希须在 Files
	// 中有字节（Build 会播 File 行 + 落盘）。
	Scores []SeedScore

	// ExtraRulesets 追加规则集行（g0v0 市场规则集 OnlineID≥10）。
	ExtraRulesets []SeedRuleset

	// ExternalSets Ez 外部托管谱面集（ext:set: Hash + 路径哈希 RealmFile
	// 行，不复制字节进 files/，fork-ez2lazer §5.1）。仅 ez2lazer 镜像可载。
	ExternalSets []ExternalSetSpec

	// EzSkillRows 在 Ez 的 7 张技能表各播 1 行固定演示行（§8.2-4）。
	// 仅 ez2lazer 镜像可载。
	EzSkillRows bool

	// Skins 追加皮肤行（Ez 专有皮肤指纹等；内置六皮肤恒播不走此）。
	Skins []SeedSkin
}

// SeedScore 成绩种子（可写列子集：int/string；bool/double/timestamp 列
// 在镜像中声明但当前不可写值）。
type SeedScore struct {
	BeatmapHash           string // 强锚点：打分时难度的 Hash（lazer-baseline §6.3）
	RulesetShortName      string
	ClientVersion         string
	Hash                  string // 全部 .osr 内容拼接 SHA-256
	TotalScore            int64
	TotalScoreWithoutMods int64
	TotalScoreVersion     int64
	MaxCombo              int64
	OnlineID              int64
	LegacyOnlineID        int64
	RankInt               int64
	Combo                 int64
	ModsJson              string
	StatisticsJson        string
	MaximumStatisticsJson string
	ReplayHashes          []string // 回放文件哈希（字节在 Seed.Files）

	// Ez 专有列（仅 ez2lazer 镜像落库；非 0 值在其他端触发构造错误）。
	JudgeMode  int64
	HealthMode int64
}

// SeedRuleset 追加规则集行。
type SeedRuleset struct {
	ShortName         string
	OnlineID          int64
	Name              string
	InstantiationInfo string
}

// ExternalSetSpec Ez 外部托管集：ContentRoot 为规范化绝对路径，Hash 由
// Build 按 ext:set: 编码现场计算。
type ExternalSetSpec struct {
	OnlineID    int64
	ContentRoot string
}

// SeedSkin 追加皮肤行。
type SeedSkin struct {
	Name    string
	Creator string
}

// Spec 建库规格。
type Spec struct {
	ForkID        string // 选择 schema 镜像（四端之一）
	SchemaVersion uint64 // 磁盘 schema 版本号（51 / 52 / 52011）
	DataDir       string // 构造的数据目录（client.realm 与 files/ 的父目录）
	Seed          Seed
}

// Build 按 spec 构造测试库：显式 schema 建库 → 播种 Seed 行 → 落盘
// files/。返回解析好的 forks.LibraryRef（可直接喂给 collect/deploy 测试）。
// 需要 librealm 在场（CI 缺库时测试调用方应显式 SKIP，错误可识别）。
func Build(ctx context.Context, spec Spec) (forks.LibraryRef, error) {
	if err := ctx.Err(); err != nil {
		return forks.LibraryRef{}, err
	}
	schema, err := SchemaFor(spec.ForkID, spec.SchemaVersion)
	if err != nil {
		return forks.LibraryRef{}, err
	}
	if spec.DataDir == "" {
		return forks.LibraryRef{}, fmt.Errorf("fixture: Spec.DataDir 为空")
	}
	if err := validateSeed(spec.ForkID, spec.Seed); err != nil {
		return forks.LibraryRef{}, fmt.Errorf("fixture: 种子不自洽: %w", err)
	}

	lib, err := realmc.Load()
	if err != nil {
		return forks.LibraryRef{}, fmt.Errorf("fixture: 需要 librealm（测试应显式 SKIP）: %w", err)
	}

	realmPath := filepath.Join(spec.DataDir, realmFileName(spec.ForkID, spec.SchemaVersion))
	r, err := lib.OpenWithSchema(realmPath, schema, spec.SchemaVersion)
	if err != nil {
		return forks.LibraryRef{}, err
	}
	if err := r.BeginWrite(); err != nil {
		r.Close()
		return forks.LibraryRef{}, err
	}
	if err := seedAll(r, spec); err != nil {
		r.Rollback()
		r.Close()
		return forks.LibraryRef{}, fmt.Errorf("fixture: 播种失败: %w", err)
	}
	if err := r.Commit(); err != nil {
		r.Rollback()
		r.Close()
		return forks.LibraryRef{}, err
	}
	if err := r.Close(); err != nil {
		return forks.LibraryRef{}, err
	}

	if err := writeFilesDir(spec.DataDir, spec.Seed.Files); err != nil {
		return forks.LibraryRef{}, err
	}

	up, rev := decodeVersion(spec.ForkID, spec.SchemaVersion)
	return forks.LibraryRef{
		ForkID:          spec.ForkID,
		DataDir:         spec.DataDir,
		RealmPath:       realmPath,
		DiskVersion:     spec.SchemaVersion,
		VersionSource:   forks.VersionFromOpen,
		UpstreamVersion: up,
		ForkRev:         rev,
	}, nil
}

// validateSeed 校验种子自洽（§8.2-3 关系一致的构造期前置）：
// Files 键 == SHA-256(值)；集引用的文件哈希有字节；score.BeatmapHash
// 命中某难度；Ez 专有值不落在非 Ez 端。
func validateSeed(forkID string, seed Seed) error {
	for h, b := range seed.Files {
		if got := sha256Hex(b); got != h {
			return fmt.Errorf("Files[%s] 键与内容 SHA-256(%s) 不一致", h, got)
		}
	}
	beatmapHashes := make(map[string]bool)
	for i, set := range seed.BeatmapSets {
		if set.Hash == "" {
			return fmt.Errorf("BeatmapSets[%d].Hash 为空", i)
		}
		for _, f := range set.Files {
			if _, ok := seed.Files[f.Hash]; !ok {
				return fmt.Errorf("BeatmapSets[%d] 引用文件哈希 %s 无字节（不在 Files）", i, f.Hash)
			}
		}
		for _, bm := range set.Beatmaps {
			beatmapHashes[bm.Hash] = true
		}
	}
	for i, s := range seed.Scores {
		if !beatmapHashes[s.BeatmapHash] {
			return fmt.Errorf("Scores[%d].BeatmapHash %s 未命中任何难度 Hash", i, s.BeatmapHash)
		}
		for _, h := range s.ReplayHashes {
			if _, ok := seed.Files[h]; !ok {
				return fmt.Errorf("Scores[%d] 回放哈希 %s 无字节（不在 Files）", i, h)
			}
		}
		if (s.JudgeMode != 0 || s.HealthMode != 0) && forkID != ForkEz2Lazer {
			return fmt.Errorf("Scores[%d] 携带 Ez 专有 JudgeMode/HealthMode 但 fork=%s 无此列", i, forkID)
		}
	}
	if len(seed.ExternalSets) > 0 && forkID != ForkEz2Lazer {
		return fmt.Errorf("ExternalSets 仅 ez2lazer 镜像可载，fork=%s", forkID)
	}
	if seed.EzSkillRows && forkID != ForkEz2Lazer {
		return fmt.Errorf("EzSkillRows 仅 ez2lazer 镜像可载，fork=%s", forkID)
	}
	return nil
}
