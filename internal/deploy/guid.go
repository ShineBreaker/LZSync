package deploy

// Guid 字符串 ↔ realm UUID 字节（lazer 各表主键 ID 列的 RLM_TYPE_UUID）。
// 手写而非引入 uuid 依赖：hub.Collection.ID 的既有形态是自由字符串（源库
// 投影产物），解析接受 36 位带连字符与 32 位纯 hex（大小写不拘），输出
// 统一小写 8-4-4-4-12——往返自洽即满足投放断言需要。

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// parseGuid 把 Guid 字符串解析为 realm UUID 字节。空串解析为零值 UUID
//（fixture 播种未写 ID 列的行投影即零值，投放侧如实还原）。
func parseGuid(s string) ([16]byte, error) {
	var out [16]byte
	if s == "" {
		return out, nil
	}
	clean := strings.ReplaceAll(s, "-", "")
	if len(clean) != 32 {
		return out, fmt.Errorf("deploy: 非法 Guid %q（非 32/36 位 hex）", s)
	}
	b, err := hex.DecodeString(strings.ToLower(clean))
	if err != nil {
		return out, fmt.Errorf("deploy: 非法 Guid %q: %w", s, err)
	}
	copy(out[:], b)
	return out, nil
}

// formatGuid 把 realm UUID 字节格式化为小写 8-4-4-4-12。
func formatGuid(b [16]byte) string {
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}
