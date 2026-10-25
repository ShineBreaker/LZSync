package forks

// 端本地字段注册表（design.md §1-B7 / §0.2-B7）。
//
// B7：DateAdded、LastPlayed、StatusInt、StarRating、PP、Hidden、
// UserSettings.Offset、EditorTimestamp、LastModified 等列「语义按端各异
// 或由客户端派生」——数据面事实：
//   - 不参与三方判定的值指纹（各端取值天然发散，参与即永久冲突）；
//   - 就地更新不覆写目标端已有值（保留各端自己的游玩史/保护标记）；
//   - 新建行取同 ServerRealm 来源端的值，无来源端值取默认（写面传入的
//     载荷即来源端投影，异域新建仍随行——列本身无跨域语义风险）。
//
// 键 = realm 类名 → 磁盘列名集（MapTo 名：StatusInt 落盘 "Status"）。
// 数据化的收益：写面、判定面、对账冲突字段三处共用同一份名单，清单随
// fork 演进只改本文件。
var LocalFields = map[string]map[string]bool{
	"BeatmapSet": {
		"DateAdded": true, // 各端导入时刻
		"Status":    true, // StatusInt：客户端按在线状态派生（Ranked/Loved/…）
		"Protected": true, // 用户标记的防清理位，按端独立
	},
	"Beatmap": {
		"Status":            true, // StatusInt 同上
		"StarRating":        true, // 难度星级：端内重算/上游刷新派生
		"LastPlayed":        true, // 游玩史
		"Hidden":            true, // 用户隐藏标记
		"EditorTimestamp":   true, // 编辑器时间戳：端内编辑产物
		"LastLocalUpdate":   true, // 端内更新时刻
		"LastOnlineUpdate":  true, // 上游刷新时刻
	},
	"BeatmapUserSettings": {
		"Offset": true, // 端内偏移校准
	},
	"Score": {
		"PP": true, // 各端 PP 算法不同（D10），覆写会污染本端计分史
	},
	"BeatmapCollection": {
		"LastModified": true, // 端内编辑时刻
	},
}

// IsLocalField 判定 (realm 类名, 磁盘列名) 是否端本地字段（B7）。
// 未知类/列保守返回 false（宁可当可同步字段，不静默丢写面）。
func IsLocalField(class, column string) bool {
	return LocalFields[class][column]
}
