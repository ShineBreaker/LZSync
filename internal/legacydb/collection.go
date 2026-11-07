package legacydb

import (
	"bytes"
	"encoding/binary"
)

// ParseCollectionDB 解析 collection.db（stable-legacy §2）。
func ParseCollectionDB(data []byte) (*CollectionDB, error) {
	r := &reader{data: data}
	db := &CollectionDB{}
	ver, err := r.u32()
	if err != nil {
		return nil, err
	}
	db.Version = int(ver)
	count, err := r.u32()
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < count; i++ {
		c := Collection{}
		if c.Name, err = r.str(); err != nil {
			return nil, err
		}
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		for j := uint32(0); j < n; j++ {
			md5, err := r.str()
			if err != nil {
				return nil, err
			}
			c.BeatmapMD5s = append(c.BeatmapMD5s, md5)
		}
		db.Collections = append(db.Collections, c)
	}
	return db, nil
}

// WriteCollectionDB 序列化 collection.db（stable-legacy §2）。版本号沿用
// db.Version 原值（不改写、不升版，S3 同精神）；落盘的原子替换由调用方
// 兑现（S9）。
func WriteCollectionDB(db CollectionDB) []byte {
	var buf bytes.Buffer
	writeU32(&buf, uint32(db.Version))
	writeU32(&buf, uint32(len(db.Collections)))
	for _, c := range db.Collections {
		writeStr(&buf, c.Name)
		writeU32(&buf, uint32(len(c.BeatmapMD5s)))
		for _, md5 := range c.BeatmapMD5s {
			writeStr(&buf, md5)
		}
	}
	return buf.Bytes()
}

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

// writeStr 写 stable 字符串：空串写 0x00 单字节，非空写 0x0b + ULEB128
// 长度 + UTF-8（stable-legacy §1.1，与读取侧两种空表示折叠规则对偶）。
func writeStr(buf *bytes.Buffer, s string) {
	if s == "" {
		buf.WriteByte(0x00)
		return
	}
	buf.WriteByte(0x0b)
	v := len(s)
	for v >= 0x80 {
		buf.WriteByte(byte(v&0x7f) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
	buf.WriteString(s)
}
