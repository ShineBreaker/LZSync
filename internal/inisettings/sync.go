package inisettings

// game.ini 白名单键同步引擎（design.md §3.1 inisettings / S12）：
//   - CollectFile（收集侧）：读源端 game.ini 的白名单键值 → 快照（不入
//     hub.db——本块纪律禁改 hub 包，hub 目录自有文件承载，见 store.go）；
//   - SyncFile（投放侧）：hub canonical 键值 → 目标 game.ini，S12 原子写
//     （写前备份原件 → 临时文件 → rename），绝不就地覆写。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"lzsync/internal/forks"
	"lzsync/internal/guard"
)

// GameIniName 是 osu.Game 层配置文件名（framework IniConfigManager 默认
// 文件名，lazer-baseline §4.2）。同步引擎只碰这一个文件——framework.ini
// /storage.ini 与各 fork 私有 ini（S10 红灯清单）天然不在面内。
const GameIniName = "game.ini"

// SkipStat 是同步面的跳过计数（hub.SkipStat 的纯文件同构形态——本包不
// 依赖 hub，保持无 SQLite 依赖的文件引擎；接线层转换）。
type SkipStat struct {
	Reason string
	Count  int
}

// SyncResult 一次 SyncFile 的执行摘要。
type SyncResult struct {
	Applied    map[string]string // 落地键 → 新值（变化键 + 追加键）
	Skipped    []SkipStat        // 跳过计数（原因稳定、排序可断言）
	BackupPath string            // 写前备份路径（无写入动作时为空）
}

// CollectFile 读源端 game.ini 的白名单键值（CollectableKeys 面）。文件
// 不存在是正常态（源端从未启动过客户端）——返回 (nil, false, nil)，
// 由调用方按「源端无 game.ini」计数。
func CollectFile(path string, source forks.ForkProfile) (map[string]string, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inisettings: 读 %s: %w", path, err)
	}
	keys := CollectableKeys(source)
	doc := Parse(data)
	out := map[string]string{}
	for _, l := range doc.Lines() {
		if l.Key != "" && keys[l.Key] {
			out[l.Key] = l.Raw[l.valueStart:l.valueEnd]
		}
	}
	return out, true, nil
}

// SyncFile 把 canonical 键值写入目标端 game.ini（S12）：
//  1. 目标文件不存在 → 跳过计数（目标端从未启动过客户端，无 ini 可写，
//     绝不凭空创建）；
//  2. 执行期复核（§3.3 纪律）：canonical 中白名单外/红灯/目标端专有键
//     一律拒写并计数（正常计划已在收集面滤除，此处是防线）；
//  3. 值一致 → 幂等跳过；值变化 → 就地替换值区间（其余字节不变）；
//     目标缺键 → 文件末尾追加；
//  4. 无落地键 → no-op（不写不备份）；有落地 → 写前备份原件（guard
//     时间戳备份复用；未配置备份目录退化为同目录时间戳副本，S2 语义）
//     → 临时文件 → rename 原子替换。
func SyncFile(target forks.ForkProfile, path string, values map[string]string, backups *guard.Backups) (SyncResult, error) {
	res := SyncResult{Applied: map[string]string{}}
	skips := map[string]int{}
	skip := func(reason string) { skips[reason]++ }

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		res.Skipped = []SkipStat{{Reason: "目标端无 game.ini（从未启动过客户端）", Count: 1}}
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("inisettings: 读 %s: %w", path, err)
	}

	// 执行期复核：同步面 = 共有 − 红灯 − 目标专有。红灯优先归类（S10
	// 的一等人话原因），其余按白名单外计数。
	names := make([]string, 0, len(values))
	for k := range values {
		names = append(names, k)
	}
	sort.Strings(names)
	doc := Parse(data)
	for _, k := range names {
		v := values[k]
		switch {
		case !IsShared(k):
			skip("白名单外键 " + k + "（不同步）")
		case IsRedLight(target, k):
			skip("红灯键 " + k + "（S10 硬锁，绝不同步）")
		case isOwn(target, k):
			skip("目标端专有键 " + k + "（Own，不同步）")
		default:
			if cur, ok := doc.Value(k); ok && cur == v {
				skip("值一致（幂等跳过）: " + k)
				continue
			}
			if !doc.SetValue(k, v) {
				doc.Append(k, v) // 目标缺该键行 → 追加（共有键拷贝语义）
			}
			res.Applied[k] = v
		}
	}
	if len(res.Applied) == 0 {
		res.Skipped = sortedSkips(skips)
		return res, nil // no-op：不产生写入与备份
	}

	// S12：写前备份 → 临时文件 → rename。
	backupPath, err := backupBeforeWrite(backups, path)
	if err != nil {
		return res, err // 备份不可绕过，失败即中止（S2）
	}
	res.BackupPath = backupPath
	if err := writeFileAtomic(path, doc.Bytes(), 0o644); err != nil {
		return res, fmt.Errorf("inisettings: 原子写 %s: %w", path, err)
	}
	res.Skipped = sortedSkips(skips)
	return res, nil
}

// backupBeforeWrite 写前备份（S2）：集中备份目录（guard 复用）优先；
// 未配置退化为文件同目录带时间戳副本——与 deploy.backupTarget 同款
// 退化语义；已存在同名备份绝不覆盖（报错中止）。
func backupBeforeWrite(backups *guard.Backups, path string) (string, error) {
	if backups != nil {
		return backups.BackupFile(path)
	}
	dst := filepath.Join(filepath.Dir(path),
		filepath.Base(path)+"."+time.Now().UTC().Format("20060102T150405.000Z"))
	if _, err := os.Stat(dst); err == nil {
		return "", fmt.Errorf("inisettings: 备份 %s 已存在，绝不覆盖（S2）", dst)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inisettings: 检查备份目标 %s: %w", dst, err)
	}
	if err := copyFile(dst, path); err != nil {
		return "", fmt.Errorf("inisettings: 生成备份 %s: %w", dst, err)
	}
	return dst, nil
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func sortedSkips(m map[string]int) []SkipStat {
	reasons := make([]string, 0, len(m))
	for r := range m {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	out := make([]SkipStat, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, SkipStat{Reason: r, Count: m[r]})
	}
	return out
}
