package fixture

// game.ini 夹具（§8.2-4 差异夹具的 M3 增量）：构造覆盖同步引擎全部
// 判定分支的典型文件——四端共有白名单键、红灯键（S10：Token/Username/
// ReleaseStream）、delta 与 g0v0 专有键（Own）、未知键、恶意注入行
//（键名含空格与 =、值内 =、无空格键值风格）与 //、;; 注释。itests
// 全链与 collect/deploy 接线测试共用。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gameIniTemplate 典型 game.ini 夹具内容（framework ini 方言，
// lazer-baseline §4.2「Key = Value 逐行」）。
const gameIniTemplate = `[General]
// framework comment
;; legacy comment
Ruleset = osu
Token = fixture.eyJhbGciOiJSUzI1NiJ9.fake.jwt.payload
Username = fixture_player
ReleaseStream = Lazer
SavePassword = true
SaveUsername = true
ShowStoryboard = true
ShowFpsDisplay = false
MenuMusic = true
Skin = 5df8f34c-204f-4d5f-b853-18b7f3e5b8d6
Version = 2026.1005.0
MenuCursorFromSkin = true
CustomApiUrl = https://g0v0.top/api
FutureKey = 42
Weird Key With = Signs = value
Injected=Key
`

// WriteGameIni 把夹具 game.ini 写入 dataDir（与 client.realm 同级，
// lazer-baseline §1.2 数据目录布局），values 按键覆盖模板默认值后
// 落盘（覆盖键不存在于模板时报错——夹具不自造键）。返回文件路径。
func WriteGameIni(dataDir string, values map[string]string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("fixture: WriteGameIni 缺 dataDir")
	}
	lines := strings.Split(gameIniTemplate, "\n")
	for key, v := range values {
		replaced := false
		for i, line := range lines {
			if _, ok := strings.CutPrefix(line, key+" = "); ok {
				lines[i] = key + " = " + v
				replaced = true
				break
			}
		}
		if !replaced {
			return "", fmt.Errorf("fixture: game.ini 夹具无键 %q（覆盖面 = 模板既有键）", key)
		}
	}
	path := filepath.Join(dataDir, "game.ini")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return "", fmt.Errorf("fixture: 写 game.ini 夹具: %w", err)
	}
	return path, nil
}
