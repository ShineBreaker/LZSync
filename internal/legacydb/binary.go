package legacydb

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// int32/int8 是无符号读取的有符号视角（文档整数字段语义按值域均为正，
// 仅 LocalOffset/OnlineOffset 允许负值走 i16）。
func (r *reader) int32() (int, error) {
	v, err := r.u32()
	return int(v), err
}

func (r *reader) int16() (int, error) {
	v, err := r.u16()
	return int(v), err
}

func (r *reader) int8() (int, error) {
	v, err := r.u8()
	return int(v), err
}

// reader 是小端二进制读取原语（stable-legacy §1.1）。整数无符号；越界
// 即报错，不做静默截断。
type reader struct {
	data []byte
	pos  int
}

func (r *reader) err(what string) error {
	return fmt.Errorf("legacydb: 读 %s 越界 offset=%d len=%d", what, r.pos, len(r.data))
}

func (r *reader) u8() (byte, error) {
	if r.pos+1 > len(r.data) {
		return 0, r.err("byte")
	}
	v := r.data[r.pos]
	r.pos++
	return v, nil
}

func (r *reader) u16() (uint16, error) {
	if r.pos+2 > len(r.data) {
		return 0, r.err("short")
	}
	v := binary.LittleEndian.Uint16(r.data[r.pos:])
	r.pos += 2
	return v, nil
}

func (r *reader) i16() (int16, error) {
	v, err := r.u16()
	return int16(v), err
}

func (r *reader) u32() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, r.err("int")
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos:])
	r.pos += 4
	return v, nil
}

func (r *reader) u64() (uint64, error) {
	if r.pos+8 > len(r.data) {
		return 0, r.err("long")
	}
	v := binary.LittleEndian.Uint64(r.data[r.pos:])
	r.pos += 8
	return v, nil
}

func (r *reader) single() (float32, error) {
	v, err := r.u32()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(v), nil
}

func (r *reader) double() (float64, error) {
	v, err := r.u64()
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(v), nil
}

func (r *reader) boolean() (bool, error) {
	v, err := r.u8()
	return v != 0, err
}

// str 读 stable 字符串：首字节 0x00 = 空串；0x0b = ULEB128 字节长度 +
// UTF-8 内容（stable-legacy §1.1）。两种空表示折叠为 ""。
func (r *reader) str() (string, error) {
	head, err := r.u8()
	if err != nil {
		return "", err
	}
	if head == 0x00 {
		return "", nil
	}
	if head != 0x0b {
		return "", fmt.Errorf("legacydb: 非法字符串头 %#x offset=%d", head, r.pos-1)
	}
	n, err := r.uleb()
	if err != nil {
		return "", err
	}
	if r.pos+int(n) > len(r.data) {
		return "", r.err("string body")
	}
	s := string(r.data[r.pos : r.pos+int(n)])
	r.pos += int(n)
	return s, nil
}

func (r *reader) uleb() (uint64, error) {
	var result uint64
	var shift uint
	for {
		b, err := r.u8()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
		if shift > 63 {
			return 0, fmt.Errorf("legacydb: ULEB128 超长 offset=%d", r.pos)
		}
	}
}

// taggedPair 读 Int-浮点 pair：0x08 + Int + tag(0x0c=Single / 0x0d=Double)
// + 浮点（stable-legacy §1.1）。
func (r *reader) taggedPair() (mods int, val float64, err error) {
	if err = r.expectTag(0x08); err != nil {
		return
	}
	m, err := r.u32()
	if err != nil {
		return
	}
	mods = int(m)
	tag, err := r.u8()
	if err != nil {
		return
	}
	switch tag {
	case 0x0c:
		var f float32
		if f, err = r.single(); err == nil {
			val = float64(f)
		}
	case 0x0d:
		val, err = r.double()
	default:
		err = fmt.Errorf("legacydb: 非法 pair 浮点标记 %#x offset=%d", tag, r.pos-1)
	}
	return
}

func (r *reader) expectTag(want byte) error {
	got, err := r.u8()
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("legacydb: 非法 pair 标记 %#x（应为 %#x）offset=%d", got, want, r.pos-1)
	}
	return nil
}

// unixEpochTicks 是 1970-01-01 UTC 的 .NET ticks 值。DateTime 换算必须
// 以它为中介：2026 年相对 0001 年的间隔约 6.4e19 ns，超出 time.Duration
// （int64）±292 年表示域，直接 Add 会静默溢出。
const unixEpochTicks = 621355968000000000

// dotNetTicks 把 .NET DateTime ticks（自 0001-01-01 UTC 的 100ns 数）转为
// time.Time（stable-legacy §1.1）。0 视为 Go 零值（未解锁占位等无效态）。
func dotNetTicks(t uint64) time.Time {
	if t == 0 {
		return time.Time{}
	}
	rel := int64(t - unixEpochTicks) // 相对 Unix epoch，量级 ≤ 当前年代秒数×1e7，int64 无压力
	return time.Unix(rel/1e7, (rel%1e7)*100).UTC()
}
