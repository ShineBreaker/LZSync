package inisettings

// osu-framework IniConfigManager 方言的最小保真模型（lazer-baseline §4.2：
// 「格式为 Key = Value 逐行」；任务方言：[Section] 头、// 与 ;; 注释）。
// 写回纪律是 S12 的字面子集——「其余内容逐字节不变」要求非目标行的
// 输出恒等于输入原文：解析保留每行原文与值在行内的字节区间，替换值只
// 重写区间本身，键名、等号两侧空格风格、CRLF 尾、注释、未知行全部原样。

import (
	"bytes"
	"strings"
)

// Line 是 ini 文件的一行原文与解析产物。非键值行（空行/注释/section 头/
// 无等号的任意行）Key 为空、值区间无效，写回时原文输出。
type Line struct {
	Raw     string // 行原文（不含换行符；CRLF 文件的 \r 保留在尾部）
	Section string // 所属 section（最近一次 [Section] 头的名；无头前为 ""）
	Key     string // 键值行的键名（首个 = 左侧 trim 后）；非键值行为 ""

	// 值在 Raw 中的字节区间 [valueStart, valueEnd)：等号右侧跳过前导空白
	// 起、行尾 \r 前（含）止；CRLF 由区间外的 \r 尾天然保真。非键值行
	// valueStart < 0。
	valueStart int
	valueEnd   int
}

// Document 是一份已解析的 ini 文件（有序行模型）。
type Document struct {
	lines []Line
	// endsWithNewline 是原文件是否以 \n 收尾（Bytes 的收尾保真；缺行尾
	// 换行的文件重写后仍缺）。
	endsWithNewline bool
}

// Parse 解析 ini 文本。方言规则：
//   - [Section] 头：行 trim 左侧空白后以 "[" 开头并以 "]" 结尾；
//   - 注释：行 trim 左侧空白后以 "//" 或 ";;" 开头；
//   - 键值行：含 "=" 且 = 左侧 trim 后非空（" = v" 空键行按普通行原样）；
//     键与值取首个 = 切分，值右侧不 trim（保真）。
//
// 其余行（含键名带空格/等号/无法归类的注入残行）一律按普通行原样承载，
// 不参与键匹配——白名单键是静态 ASCII 标识符集，键名含 "="、空白等
// 字符的行天然永不命中（恶意注入的第一道防线）。
func Parse(data []byte) *Document {
	s := string(data)
	endsWithNewline := strings.HasSuffix(s, "\n")
	parts := strings.Split(s, "\n")
	if endsWithNewline {
		// 末尾空串是「结尾换行」的切分产物，不作为行保留（Bytes 由
		// endsWithNewline 补回），否则换行被双重表达。
		parts = parts[:len(parts)-1]
	}
	var lines []Line
	section := ""
	for _, raw := range parts {
		line := Line{Raw: raw, Section: section, valueStart: -1, valueEnd: -1}
		trimmed := strings.TrimLeft(raw, " \t")
		switch {
		case trimmed == "":
			// 空行。
		case strings.HasPrefix(trimmed, "//"), strings.HasPrefix(trimmed, ";;"):
			// 注释行。
		case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			line.Section = section
		default:
			if eq := strings.Index(raw, "="); eq >= 0 {
				if key := strings.TrimSpace(raw[:eq]); key != "" {
					line.Key = key
					// 值区间：等号右侧跳过前导空白，止于行尾（\r 之前）。
					vs := eq + 1
					for vs < len(raw) && (raw[vs] == ' ' || raw[vs] == '\t') {
						vs++
					}
					ve := len(raw)
					if strings.HasSuffix(raw, "\r") {
						ve--
					}
					// 值全空白（含空值）时区间收敛为 [ve, ve)，替换后值
					// 落在 \r 前。
					if vs > ve {
						vs = ve
					}
					line.valueStart = vs
					line.valueEnd = ve
				}
			}
		}
		lines = append(lines, line)
	}
	return &Document{lines: lines, endsWithNewline: endsWithNewline}
}

// Bytes 重绘文件全文。非目标行逐字节等于原文（保真写回的载体）。
func (d *Document) Bytes() []byte {
	var buf bytes.Buffer
	for i, l := range d.lines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(l.Raw)
	}
	if d.endsWithNewline && len(d.lines) > 0 {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// Lines 返回行模型（测试断言与外部只读检视用）。
func (d *Document) Lines() []Line { return d.lines }

// Value 取键的当前值（首个命中行；不存在返回 false）。
func (d *Document) Value(key string) (string, bool) {
	for _, l := range d.lines {
		if l.Key == key {
			return l.Raw[l.valueStart:l.valueEnd], true
		}
	}
	return "", false
}

// SetValue 就地替换键的值区间（首个命中行；仅重写值字节，键名与等号
// 风格原样）。命中返回 true；键不存在返回 false（追加语义由调用方处理）。
func (d *Document) SetValue(key, value string) bool {
	for i, l := range d.lines {
		if l.Key == key {
			d.lines[i].Raw = l.Raw[:l.valueStart] + value + l.Raw[l.valueEnd:]
			d.lines[i].valueEnd = l.valueStart + len(value) // 区间随新值同步
			return true
		}
	}
	return false
}

// Append 追加一个键值行（目标文件缺该键时的共有键拷贝落点，framework
// 写出风格 "Key = Value"；文件缺行尾换行时先补）。追加行落在 section
// 模型的末尾——game.ini 的 OsuSetting 键全局唯一、不受 section 影响。
func (d *Document) Append(key, value string) {
	if len(d.lines) > 0 && !d.endsWithNewline {
		d.endsWithNewline = true
	}
	section := ""
	if n := len(d.lines); n > 0 {
		section = d.lines[n-1].Section
	}
	d.lines = append(d.lines, Line{
		Raw:       key + " = " + value,
		Section:   section,
		Key:       key,
		valueStart: len(key) + 3,
		valueEnd:   len(key) + 3 + len(value),
	})
}
