package inisettings

// ini 方言解析与保真写回的验收（S12 的字面子集：非目标行逐字节不变）。

import (
	"bytes"
	"testing"
)

// rawIni 是覆盖方言全貌的夹具：[Section] 头、// 与 ;; 注释、键值行
//（含等号无空格风格）、CRLF 行、空行、缺行尾换行。
const rawIni = "[General]\n" +
	"// framework comment\n" +
	";; legacy comment\n" +
	"Ruleset = osu\n" +
	"Token=eyJ.fake.jwt\n" +
	"ShowStoryboard = true\r\n" +
	"\n" +
	"[OsuSetting]\n" +
	"UnknownKey = 42\n" +
	"Weird Key With = Signs = value\n" +
	"EmptyValue =\n" +
	"no equals line"

func TestParseBytesRoundtrip(t *testing.T) {
	doc := Parse([]byte(rawIni))
	if got := doc.Bytes(); !bytes.Equal(got, []byte(rawIni)) {
		t.Fatalf("往返不逐字节相等:\n got %q\nwant %q", got, rawIni)
	}
}

func TestParseDialect(t *testing.T) {
	doc := Parse([]byte(rawIni))
	cases := []struct {
		line int
		key  string // "" = 非键值行
		sec  string
	}{
		{0, "", "General"},  // section 头
		{1, "", "General"},  // // 注释
		{2, "", "General"},  // ;; 注释
		{3, "Ruleset", "General"},
		{4, "Token", "General"}, // 等号无空格风格
		{5, "ShowStoryboard", "General"},
		{6, "", "General"}, // 空行
		{7, "", "OsuSetting"},
		{8, "UnknownKey", "OsuSetting"},
		{9, "Weird Key With", "OsuSetting"}, // 键含空格（= 首个切分）
		{10, "EmptyValue", "OsuSetting"},
		{11, "", "OsuSetting"}, // 无等号普通行
	}
	for _, c := range cases {
		l := doc.Lines()[c.line]
		if l.Key != c.key || l.Section != c.sec {
			t.Errorf("行 %d 解析 = (%q, %q)，期望 (%q, %q)", c.line, l.Key, l.Section, c.key, c.sec)
		}
	}
	// 值语义：等号右侧跳过前导空白；值内 = 原样；空值合法。
	if v, ok := doc.Value("Token"); !ok || v != "eyJ.fake.jwt" {
		t.Errorf("Token 值 = (%q,%v)，期望 (\"eyJ.fake.jwt\",true)", v, ok)
	}
	if v, _ := doc.Value("Weird Key With"); v != "Signs = value" {
		t.Errorf("含 = 的值应原样保留，got %q", v)
	}
	if v, ok := doc.Value("EmptyValue"); !ok || v != "" {
		t.Errorf("空值 = (%q,%v)，期望 (\"\",true)", v, ok)
	}
	// 恶意注入的键名（含空白/等号/换行残段）永不命中白名单标识符。
	for _, evil := range []string{"Weird Key With", "Injected", "Bad\nKey", "Key=x"} {
		if IsShared(evil) {
			t.Errorf("恶意键名 %q 不应命中白名单", evil)
		}
	}
}

func TestSetValuePreservesEverythingElse(t *testing.T) {
	doc := Parse([]byte(rawIni))
	doc.SetValue("ShowStoryboard", "false")
	got := doc.Bytes()
	// 期望：仅 "true"（CRLF 行的值区间，\r 之外）替换为 "false"。
	want := "[General]\n" +
		"// framework comment\n" +
		";; legacy comment\n" +
		"Ruleset = osu\n" +
		"Token=eyJ.fake.jwt\n" +
		"ShowStoryboard = false\r\n" +
		"\n" +
		"[OsuSetting]\n" +
		"UnknownKey = 42\n" +
		"Weird Key With = Signs = value\n" +
		"EmptyValue =\n" +
		"no equals line"
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("SetValue 触碰了值区间之外的字节:\n got %q\nwant %q", got, want)
	}
	if v, _ := doc.Value("ShowStoryboard"); v != "false" {
		t.Errorf("替换后值 = %q，期望 false", v)
	}
	// 缺键返回 false（追加语义由调用方处理）。
	if doc.SetValue("NoSuchKey", "x") {
		t.Error("缺键 SetValue 应返回 false")
	}
}

func TestAppendKeepsPrefixBytes(t *testing.T) {
	// 文件缺行尾换行：追加先补换行，前缀逐字节不变；追加行落在完整换行
	// 之后（framework 文件以换行收尾的常规形态）。
	doc := Parse([]byte("Ruleset = osu"))
	doc.Append("ShowFpsDisplay", "true")
	want := "Ruleset = osu\nShowFpsDisplay = true\n"
	if got := string(doc.Bytes()); got != want {
		t.Fatalf("追加结果 = %q，期望 %q", got, want)
	}
	// 以换行收尾的文件：不引入空行。
	doc2 := Parse([]byte("Ruleset = osu\n"))
	doc2.Append("ShowFpsDisplay", "true")
	if got, want := string(doc2.Bytes()), "Ruleset = osu\nShowFpsDisplay = true\n"; got != want {
		t.Fatalf("追加结果 = %q，期望 %q", got, want)
	}
}
