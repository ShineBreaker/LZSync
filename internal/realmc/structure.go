package realmc

// 结构档案采集（design §3.1：结构档案采集；§4.3 运行时数据源 1）：
// class/property 清单导出 JSON（forks 模块落盘 snapshots/{fork}-{diskVersion}.json），
// 以及写前/写后快照相等性（DriftGuard 支撑，S1：任何漂移整批回滚）。

import (
	"encoding/json"
	"reflect"
)

// PropertySnapshot 是 realm_property_info_t 的完整投影（realm.h:284-294）。
// Type/Collection/Flags 为 realm 枚举原值（PropType / realm_collection_type_e /
// realm_property_flags_e），LinkTarget 是对象链接的目标 class 名。
type PropertySnapshot struct {
	Name       string   `json:"name"`
	Type       PropType `json:"type"`
	Collection uint32   `json:"collection"`
	LinkTarget string   `json:"linkTarget"`
	Key        int64    `json:"key"`
	Flags      int32    `json:"flags"`
}

// ClassSnapshot 是一个 class 的结构档案行。Flags 含 RLM_CLASS_EMBEDDED(=1)；
// NumComputedProperties 是 backlink 类计算属性数（不占持久列，不在 Properties 内）。
type ClassSnapshot struct {
	Name                  string             `json:"name"`
	PrimaryKey            string             `json:"primaryKey"`
	Key                   uint32             `json:"key"`
	Flags                 int32              `json:"flags"`
	NumComputedProperties uint64             `json:"numComputedProperties"`
	Properties            []PropertySnapshot `json:"properties"`
}

// SchemaSnapshot 是一次采集的完整结构档案（含磁盘 schema 版本）。
type SchemaSnapshot struct {
	SchemaVersion uint64          `json:"schemaVersion"`
	Classes       []ClassSnapshot `json:"classes"`
}

// CaptureSchemaSnapshot 采集当前会话可见的完整结构（动态会话 = 磁盘结构，
// S3 钉死）。要求已开启事务（读写皆可）。
func (r *Realm) CaptureSchemaSnapshot() (*SchemaSnapshot, error) {
	classes, err := r.Classes()
	if err != nil {
		return nil, err
	}
	snap := &SchemaSnapshot{SchemaVersion: r.SchemaVersion(), Classes: make([]ClassSnapshot, 0, len(classes))}
	for _, c := range classes {
		raw, err := r.rawProperties(c.Key)
		if err != nil {
			return nil, err
		}
		cs := ClassSnapshot{
			Name:                  c.Name,
			PrimaryKey:            c.PrimaryKey,
			Key:                   c.Key,
			Flags:                 c.Flags,
			NumComputedProperties: c.NumComputedProperties,
			Properties:            make([]PropertySnapshot, 0, len(raw)),
		}
		for _, p := range raw {
			cs.Properties = append(cs.Properties, PropertySnapshot{
				Name:       goString(p.name),
				Type:       PropType(p.typ),
				Collection: p.collectionType,
				LinkTarget: goString(p.linkTarget),
				Key:        p.key,
				Flags:      p.flags,
			})
		}
		snap.Classes = append(snap.Classes, cs)
	}
	return snap, nil
}

// Equal 逐字段比较两份结构档案（含 class/property key）。DriftGuard 的
// 写前/写后一致性判定：动态会话不迁移（S3），结构漂移只可能是工具缺陷。
func (a *SchemaSnapshot) Equal(b *SchemaSnapshot) bool {
	return reflect.DeepEqual(a, b)
}

// JSON 导出结构档案（snapshots/{fork}-{diskVersion}.json 的落盘形态）。
func (s *SchemaSnapshot) JSON() ([]byte, error) {
	return json.MarshalIndent(s, "", "  ")
}
