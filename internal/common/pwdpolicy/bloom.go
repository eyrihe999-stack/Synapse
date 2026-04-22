// bloom.go 极小只读 bloom filter,专用于离线打包的弱密码名单。
//
// 二进制格式(little-endian):
//
//	magic   [4]byte = "SPWB"
//	version uint8   = 1
//	k       uint8             // 哈希函数个数
//	mBits   uint32            // 位数组长度(bit)
//	bits    []byte            // ceil(mBits/8) 字节
//
// 参数:n=10000, fp=0.001 → mBits≈143776, k≈10, 体积 ~17.5KB。
// 纯只读,构造后不可变;写入逻辑在 cmd/gen-pwd-bloom。
package pwdpolicy

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
)

// bloomMagic 产物魔数,防止误把其他 blob 喂进来。
var bloomMagic = [4]byte{'S', 'P', 'W', 'B'}

const bloomVersion uint8 = 1

// readOnlyBloom 只读 bloom filter,加载后 Test 是热路径,不加锁。
type readOnlyBloom struct {
	bits  []byte
	mBits uint32
	k     uint8
}

// loadBloom 从字节切片解码一个 bloom。拒绝空/坏数据,返回明确错误。
func loadBloom(data []byte) (*readOnlyBloom, error) {
	const headerLen = 4 + 1 + 1 + 4
	if len(data) < headerLen {
		return nil, fmt.Errorf("pwdpolicy: bloom blob too short (%d<%d)", len(data), headerLen)
	}
	var magic [4]byte
	copy(magic[:], data[:4])
	if magic != bloomMagic {
		return nil, fmt.Errorf("pwdpolicy: bad bloom magic %q", magic)
	}
	ver := data[4]
	if ver != bloomVersion {
		return nil, fmt.Errorf("pwdpolicy: unsupported bloom version %d", ver)
	}
	k := data[5]
	mBits := binary.LittleEndian.Uint32(data[6:10])
	if k == 0 || mBits == 0 {
		return nil, fmt.Errorf("pwdpolicy: invalid bloom params k=%d m=%d", k, mBits)
	}
	bytesNeeded := (int(mBits) + 7) / 8
	if len(data)-headerLen != bytesNeeded {
		return nil, fmt.Errorf("pwdpolicy: bloom bits size mismatch: got %d want %d", len(data)-headerLen, bytesNeeded)
	}
	// 复制一份,避免 caller 之后改原 slice 影响内部状态。
	bits := make([]byte, bytesNeeded)
	copy(bits, data[headerLen:])
	return &readOnlyBloom{bits: bits, mBits: mBits, k: k}, nil
}

// test 查询 s 是否可能存在。false = 一定不在;true = 大概率在(有假阳性)。
func (b *readOnlyBloom) test(s string) bool {
	h1, h2 := doubleHash(s)
	m := uint64(b.mBits)
	for i := uint8(0); i < b.k; i++ {
		// Kirsch-Mitzenmacher:k 个独立哈希由两个基哈希线性组合近似。
		idx := (h1 + uint64(i)*h2) % m
		if b.bits[idx>>3]&(1<<(idx&7)) == 0 {
			return false
		}
	}
	return true
}

// doubleHash 两个基哈希。第一个 FNV-1a over s,第二个 FNV-1a over 带前缀的 s。
// 用 64 位减少碰撞,且不依赖第三方包。
func doubleHash(s string) (uint64, uint64) {
	h := fnv.New64a()
	//sayso-lint:ignore err-swallow
	_, _ = h.Write([]byte(s))
	h1 := h.Sum64()

	h.Reset()
	//sayso-lint:ignore err-swallow
	_, _ = h.Write([]byte{0x5a}) // salt byte
	//sayso-lint:ignore err-swallow
	_, _ = h.Write([]byte(s))
	h2 := h.Sum64()
	if h2 == 0 {
		// 避免 i*h2 全零退化成同一位。
		h2 = 0x9e3779b97f4a7c15
	}
	return h1, h2
}
