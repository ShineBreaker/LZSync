package appsvc

// 容器导出真实后端（§7-M3-5 路线④）：hub canonical payload + 仓内实体
// → internal/container 生成官方兼容容器到临时目录，经系统默认打开方式
// 交给目标 fork 客户端导入。与投放（deploy）互为替代：容器路径不触碰
// 任何 .realm，读不了目标库现状是其已知天花板（go-realm-options §5）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"lzsync/internal/container"
	"lzsync/internal/hub"
)

// realExport ExportService 真实实现。open 拉起函数可注入（测试替换为
// stub，避免自动化真的调 xdg-open；产品路径装 container.OpenWithDefaultApp）。
type realExport struct {
	deps Deps
	reg  *registry
	open func(path string) error
}

// newRealExport 构造产品路径实现（系统默认打开方式拉起）。
func newRealExport(deps Deps, reg *registry) *realExport {
	return &realExport{deps: deps, reg: reg, open: container.OpenWithDefaultApp}
}

// ExportContainers 生成容器到临时目录并逐个拉起。生成阶段失败走 error
//（坏数据宁失败不静默）；拉起失败只进 Note——容器已生成，用户可手动导入。
func (s *realExport) ExportContainers(forkID string, kind hub.EntityKind, contentKeys []string) (ExportResult, error) {
	if _, ok := s.reg.get(forkID); !ok {
		return ExportResult{}, fmt.Errorf("appsvc: 目标 fork %q 未注册，请先在库管理页发现或注册", forkID)
	}
	if len(contentKeys) == 0 {
		return ExportResult{}, errors.New("appsvc: 未指定要导出的内容键")
	}
	if s.deps.Files == nil || s.deps.Files.Store == nil {
		return ExportResult{}, errors.New("appsvc: 未配置内容寻址仓，无法导出容器")
	}
	openBlob := func(hash string) (io.ReadCloser, error) {
		f, err := os.Open(s.deps.Files.Store.Path(hash))
		if err != nil {
			return nil, fmt.Errorf("仓内实体 %s: %w", shortKey(hash), err)
		}
		return f, nil
	}
	dir, err := os.MkdirTemp("", "lzsync-export-")
	if err != nil {
		return ExportResult{}, fmt.Errorf("appsvc: 建导出临时目录: %w", err)
	}
	ctx := context.Background()
	used := map[string]bool{}
	paths := make([]string, 0, len(contentKeys))
	for _, key := range contentKeys {
		path, err := s.exportOne(ctx, kind, key, dir, used, openBlob)
		if err != nil {
			return ExportResult{Paths: paths}, err
		}
		paths = append(paths, path)
	}
	note := ""
	var launchFails []string
	for _, p := range paths {
		if err := s.open(p); err != nil {
			launchFails = append(launchFails, err.Error())
		}
	}
	if len(launchFails) > 0 {
		note = strings.Join(launchFails, "；")
	}
	return ExportResult{Paths: paths, Note: note}, nil
}

// exportOne 导出单个内容键：读 hub payload → 组装人话文件名 → 生成容器
// 文件（失败清理半成品，不留坏文件）。
func (s *realExport) exportOne(ctx context.Context, kind hub.EntityKind, key, dir string,
	used map[string]bool, openBlob container.BlobSource) (string, error) {
	base, write, err := s.materialize(ctx, kind, key)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, uniqName(used, sanitizeName(base, key), containerExt(kind)))
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("appsvc: 建容器文件 %s: %w", path, err)
	}
	if err := write(f, openBlob); err != nil {
		f.Close() //nolint:errcheck // 只读句柄失败无副作用
		os.Remove(path) //nolint:errcheck // 清理自己刚建的半成品
		return "", fmt.Errorf("appsvc: 生成容器失败: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(path) //nolint:errcheck
		return "", fmt.Errorf("appsvc: 落盘容器 %s: %w", path, err)
	}
	return path, nil
}

// materialize 按 kind 读取 hub canonical 载荷，产出「文件名基底 + 容器
// 写入器」。查询面：谱面集按版本链尾直查（hub 公共读取面只放开全量
// CurrentBeatmapSets；superseded_by IS NULL 即 HB1 的「只认最新」条件，
// 只读直查先例同 summarizer），皮肤/成绩走 hub 公共 Load 面。
func (s *realExport) materialize(ctx context.Context, kind hub.EntityKind, key string) (string, func(io.Writer, container.BlobSource) error, error) {
	switch kind {
	case hub.KindBeatmapSet:
		var payload string
		err := s.deps.Hub.DB().QueryRowContext(ctx,
			`SELECT payload FROM beatmap_set WHERE content_hash=? AND superseded_by IS NULL`, key).
			Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, fmt.Errorf("appsvc: 谱面集 %s 不在 hub（或非当前版本链尾）", shortKey(key))
		}
		if err != nil {
			return "", nil, fmt.Errorf("appsvc: 读谱面集 %s: %w", shortKey(key), err)
		}
		var set hub.BeatmapSet
		if err := json.Unmarshal([]byte(payload), &set); err != nil {
			return "", nil, fmt.Errorf("appsvc: 解码谱面集 %s payload: %w", shortKey(key), err)
		}
		base := fmt.Sprintf("%s - %s (%s)", set.Artist, set.Title, set.AuthorUsername)
		return base, func(w io.Writer, open container.BlobSource) error {
			return container.WriteOSZ(w, set, open)
		}, nil
	case hub.KindSkin:
		skin, _, ok, err := s.deps.Hub.LoadSkin(ctx, key)
		if err != nil {
			return "", nil, fmt.Errorf("appsvc: 读皮肤 %s: %w", shortKey(key), err)
		}
		if !ok {
			return "", nil, fmt.Errorf("appsvc: 皮肤 %s 不在 hub", shortKey(key))
		}
		base := skin.Name
		if skin.Creator != "" {
			base += " (" + skin.Creator + ")"
		}
		return base, func(w io.Writer, open container.BlobSource) error {
			return container.WriteOSK(w, skin, open)
		}, nil
	case hub.KindScore:
		beatmapHash, scoreHash, ok := strings.Cut(key, "|") // ScoreContentKey 约定（hub/apply.go）
		if !ok {
			return "", nil, fmt.Errorf("appsvc: 非法的成绩内容键 %q", key)
		}
		score, _, found, err := s.deps.Hub.LoadScore(ctx, beatmapHash, scoreHash)
		if err != nil {
			return "", nil, fmt.Errorf("appsvc: 读成绩 %s: %w", shortKey(key), err)
		}
		if !found {
			return "", nil, fmt.Errorf("appsvc: 成绩 %s 不在 hub", shortKey(key))
		}
		base := fmt.Sprintf("%s %s", score.RulesetShortName, score.Date.Format("20060102-150405"))
		return base, func(w io.Writer, open container.BlobSource) error {
			return container.WriteOSR(w, score, open)
		}, nil
	default:
		return "", nil, fmt.Errorf("appsvc: %s 无容器形态（仅谱面集/皮肤/成绩可导出，§7-M3-5）", kind)
	}
}

// containerExt kind → 容器扩展名（materialize 已限定三 kind，default 兜底）。
func containerExt(kind hub.EntityKind) string {
	switch kind {
	case hub.KindBeatmapSet:
		return container.ExtOSZ
	case hub.KindSkin:
		return container.ExtOSK
	default:
		return container.ExtOSR
	}
}

// sanitizeName 组装文件名：去首尾空白、路径分隔符与各平台保留字符替换为
// 下划线；空结果回退内容键短缀（保证非空）。
func sanitizeName(base, key string) string {
	mapped := strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|', '\x00':
			return '_'
		}
		return r
	}, strings.TrimSpace(base))
	if mapped == "" {
		mapped = shortKey(key)
	}
	return mapped
}

// uniqName 同批唯一文件名：撞名时追加序号。
func uniqName(used map[string]bool, base, ext string) string {
	name := base + ext
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
	used[name] = true
	return name
}
