// Package fixture 是测试库构造（design.md §8.2）：测试库是 LZSync 自产的
// 合成产物，不是用户库。经 realm-core C API 显式 schema + 版本号新建空库
// 并播种行（该能力已由 PoC 第二道验收线实证）；产品路径仍严格钉死动态
// 打开（S3 不因测试破例）。
package fixture

import (
	"context"
	"errors"

	"lzsync/internal/forks"
	"lzsync/internal/hub"
	"lzsync/internal/realmc"
)

// ErrNotImplemented 标记契约桩：签名已定、实现待施工。
var ErrNotImplemented = errors.New("fixture: 契约桩未实现")

// Seed 种子数据（自洽图，§8.2-3：哈希与行一致——内容寻址不要求「能玩的
// 谱面」，只要求哈希与行一致）。
type Seed struct {
	Collections []hub.Collection
	BeatmapSets []hub.BeatmapSet
	// Files 哈希 → 文件字节；Build 落盘 files/<h0>/<h01>/<hash> 并校验
	// 键与内容一致。内置皮肤/官方四规则集行按固定 Guid/ShortName 播种
	// （lazer-baseline §2.9/§2.10），亦经本结构扩展承载。
	Files map[string][]byte
}

// Spec 建库规格。
type Spec struct {
	ForkID        string // 选择 schema 镜像（四端之一）
	SchemaVersion uint64 // 磁盘 schema 版本号（51 / 52 / 52011）
	DataDir       string // 构造的数据目录（client.realm 与 files/ 的父目录）
	Seed          Seed
}

// SchemaFor 返回 (fork, version) 的 schema 镜像：由结构档案 JSON 驱动，
// 内容从调研的字段级摘录生成（C-1 不依赖 reference/）。三重用途：建库
// 模板、白名单数据源、golden 比对基准（防绑定层写错列名/类型，§8.2-1）。
func SchemaFor(forkID string, schemaVersion uint64) (*realmc.Schema, error) {
	return nil, ErrNotImplemented
}

// Build 按 spec 构造测试库：显式 schema 建库 → 播种 Seed 行 → 落盘
// files/。返回解析好的 forks.LibraryRef（可直接喂给 collect/deploy 测试）。
// 需要 librealm 在场（CI 缺库时跳过并显式标记 SKIP，不得静默绿，§8.1）。
func Build(ctx context.Context, spec Spec) (forks.LibraryRef, error) {
	return forks.LibraryRef{}, ErrNotImplemented
}
