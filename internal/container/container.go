// Package container 是容器降级路径（design.md §2 路线④、§7-M3-5）：从
// hub canonical payload + 内容寻址仓实体生成官方兼容容器（.osz/.osk/.osr），
// 配套系统默认打开方式的客户端拉起（lazer 启动参数导入，go-realm-options
// §5-1）。不碰任何 .realm——读不了库内现状是该路线的已知能力天花板
//（go-realm-options §5），仅在无法部署 native 库或只需推送谱面/皮肤/
// 成绩时使用（design.md §7-M3-5）。
//
// 分层：与 collect/deploy 同级的只读编排（hub DTO + filestore 字节 →
// 容器），内核不知道任何具体 fork；zip 写法与官方容器兼容（store/deflate
// 均可，go-realm-options §5-1「公开文档的 ZIP 类格式」）。
package container

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"lzsync/internal/hub"
)

// BlobSource 按内容哈希打开仓内实体（filestore.Store.Path 的适配形态，
// 不让本包直接依赖仓实现）。
type BlobSource func(hash string) (io.ReadCloser, error)

// 容器扩展名与各 kind 的容器形态（官方生态公开文档的格式，§7-M3-5）。
const (
	ExtOSZ = ".osz" // 谱面集：各难度 .osu（文件名=难度名）+ 资产按 Files.Filename 布局
	ExtOSK = ".osk" // 皮肤：skin.ini 等按 Skin.Files.Filename 布局
	ExtOSR = ".osr" // 成绩 replay 单文件（字节取仓内 .osr）
)

// WriteOSZ 把谱面集写成官方兼容 .osz：难度 .osu 与资产文件全部按
// set.Files.Filename 布局于 zip 根（收集链保证难度 .osu 以
// 「难度名.osu」的 NamedFile 在场，collect/project.go readNamedFiles）。
// 每个难度的实体必须在 Files 中可寻——缺任一难度的 .osu 即坏容器
//（导入器按 .osu 解析难度），明确失败而非静默残缺。
func WriteOSZ(w io.Writer, set hub.BeatmapSet, open BlobSource) error {
	byHash, err := layoutCheck(set.Files)
	if err != nil {
		return err
	}
	for _, bm := range set.Beatmaps {
		if _, ok := byHash[bm.Hash]; !ok {
			return fmt.Errorf("container: 谱面集 %s 缺难度 %q 的 .osu 实体（Files 无哈希 %s），拒绝生成坏容器",
				shortHash(set.Hash), bm.DifficultyName, shortHash(bm.Hash))
		}
	}
	return writeZip(w, set.Files, open)
}

// WriteOSK 把皮肤写成官方兼容 .osk：文件按 skin.Files.Filename 布局
//（skin.ini 即布局的一员，内容不做任何改写——文件字节是哈希体系的根，
// design.md §1「不做」清单）。
func WriteOSK(w io.Writer, skin hub.Skin, open BlobSource) error {
	if len(skin.Files) == 0 {
		return fmt.Errorf("container: 皮肤 %q 无文件，无从生成 .osk", skin.Name)
	}
	if _, err := layoutCheck(skin.Files); err != nil {
		return err
	}
	return writeZip(w, skin.Files, open)
}

// WriteOSR 把成绩写成 .osr 单文件容器：字节取仓内 .osr（Score.Files 的
// .osr usage；lazer 成绩以单 replay 文件为主，多段回放不构成 .osr 格式，
// 取首条）。
func WriteOSR(w io.Writer, score hub.Score, open BlobSource) error {
	for _, f := range score.Files {
		if strings.HasSuffix(strings.ToLower(f.Filename), ExtOSR) {
			return copyVerified(w, f.Hash, open)
		}
	}
	return fmt.Errorf("container: 成绩 %s 无 .osr 回放文件，无从生成容器", shortHash(score.Hash))
}

// layoutCheck 布局前置校验：zip 条目名合法（无路径逃逸）且同名不指向
// 不同哈希（重名条目是非法 zip，官方导入器行为未定义）。返回
// hash → filename 映射供调用方寻址。
func layoutCheck(files []hub.NamedFile) (map[string]string, error) {
	byHash := make(map[string]string, len(files))
	byName := make(map[string]string, len(files))
	for _, f := range files {
		if err := safeZipName(f.Filename); err != nil {
			return nil, err
		}
		if prev, dup := byName[f.Filename]; dup {
			if prev != f.Hash {
				return nil, fmt.Errorf("container: 文件名 %q 重复指向不同哈希（%s / %s）", f.Filename, shortHash(prev), shortHash(f.Hash))
			}
			continue
		}
		byName[f.Filename] = f.Hash
		byHash[f.Hash] = f.Filename
	}
	return byHash, nil
}

// safeZipName 拒绝空名、绝对路径与 ".." 段（生成侧不产出路径逃逸名）。
func safeZipName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\\x00") {
		return fmt.Errorf("container: 非法 zip 条目名 %q", name)
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == ".." {
			return fmt.Errorf("container: zip 条目名 %q 含 .. 段", name)
		}
	}
	return nil
}

// writeZip 按布局逐条流式写入（deflate；store 亦为官方兼容，此处取压缩）。
func writeZip(w io.Writer, files []hub.NamedFile, open BlobSource) error {
	zw := zip.NewWriter(w)
	for _, f := range files {
		ew, err := zw.CreateHeader(&zip.FileHeader{Name: f.Filename, Method: zip.Deflate})
		if err != nil {
			return fmt.Errorf("container: 建 zip 条目 %s: %w", f.Filename, err)
		}
		if err := copyVerified(ew, f.Hash, open); err != nil {
			return fmt.Errorf("container: 条目 %s: %w", f.Filename, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("container: 收尾 zip: %w", err)
	}
	return nil
}

// copyVerified 流式拷贝仓内实体并校验 SHA-256：仓是唯一权威副本（§5.3），
// 校验不符即数据损坏——宁失败不产出内容与哈希不符的坏容器。
func copyVerified(w io.Writer, hash string, open BlobSource) error {
	rc, err := open(hash)
	if err != nil {
		return fmt.Errorf("打开仓内实体: %w", err)
	}
	defer rc.Close() //nolint:errcheck // 只读句柄，Close 失败无副作用
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, sum), rc); err != nil {
		return fmt.Errorf("读仓内实体: %w", err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != hash {
		return fmt.Errorf("仓内实体哈希不符：期望 %s 实得 %s", hash, got)
	}
	return nil
}

// shortKey 长哈希截断显示。
func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12] + "…"
	}
	return h
}
