// Package main 构建 internal/common/pwdpolicy/weak_passwords.bloom 产物。
//
// 用法(从仓库根目录执行):
//
//	# 先把 SecLists 的 top-10k 放到 /tmp 或项目外路径:
//	curl -fsSL https://raw.githubusercontent.com/danielmiessler/SecLists/master/Passwords/Common-Credentials/10-million-password-list-top-10000.txt \
//	  -o /tmp/top10k.txt
//
//	# 生成产物(默认写回 internal/common/pwdpolicy/weak_passwords.bloom):
//	go run ./cmd/gen-pwd-bloom -input /tmp/top10k.txt
//
// 产物二进制格式见 internal/common/pwdpolicy/bloom.go 的注释。
// 参数走 -n / -fp,默认 n=10000 / fp=0.001 → ~17.5KB, k≈10。
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"hash/fnv"
	"log"
	"math"
	"os"
	"strings"
)

var (
	bloomMagic   = [4]byte{'S', 'P', 'W', 'B'}
	bloomVersion = uint8(1)
)

func main() {
	input := flag.String("input", "", "path to top-N password list (one password per line)")
	output := flag.String("output", "internal/common/pwdpolicy/weak_passwords.bloom", "output bloom file path")
	n := flag.Int("n", 10000, "expected number of entries (used to size bit array)")
	fp := flag.Float64("fp", 0.001, "target false positive rate")
	flag.Parse()

	if *input == "" {
		log.Fatal("missing -input; see -h for usage")
	}

	entries, err := loadEntries(*input)
	if err != nil {
		log.Fatalf("load entries: %v", err)
	}
	log.Printf("loaded %d unique entries from %s", len(entries), *input)

	// 用实际装载数量校准参数:优先保证 fp 目标不劣化。
	size := *n
	if len(entries) > size {
		size = len(entries)
	}
	mBits, k := sizeBloom(size, *fp)
	log.Printf("bloom params: m=%d bits (%d bytes), k=%d", mBits, (mBits+7)/8, k)

	bits := make([]byte, (mBits+7)/8)
	for _, e := range entries {
		h1, h2 := doubleHash(e)
		for i := uint8(0); i < k; i++ {
			idx := (h1 + uint64(i)*h2) % uint64(mBits)
			bits[idx>>3] |= 1 << (idx & 7)
		}
	}

	if err := writeBloom(*output, mBits, k, bits); err != nil {
		log.Fatalf("write bloom: %v", err)
	}
	log.Printf("wrote %s", *output)
}

// loadEntries 读入密码列表,去重 + ToLower + 去空行。忽略以 "#" 开头的注释行。
func loadEntries(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	seen := make(map[string]struct{}, 16384)
	sc := bufio.NewScanner(f)
	// 常见列表单词最长 < 64;放到 1MB 行缓冲避免意外长行截断。
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.ToLower(line)
		seen[line] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out, nil
}

// sizeBloom 按 n/fp 计算 m 和 k(经典公式)。
//
//	m = -n * ln(fp) / (ln 2)^2
//	k = (m/n) * ln 2
//
// k 至少 1。
func sizeBloom(n int, fp float64) (mBits uint32, k uint8) {
	if n <= 0 {
		n = 1
	}
	if fp <= 0 || fp >= 1 {
		fp = 0.001
	}
	ln2 := math.Ln2
	m := -float64(n) * math.Log(fp) / (ln2 * ln2)
	kFloat := (m / float64(n)) * ln2
	mInt := uint32(math.Ceil(m))
	kInt := uint8(math.Max(1, math.Round(kFloat)))
	return mInt, kInt
}

// writeBloom 按 internal/common/pwdpolicy/bloom.go 的格式写出。
func writeBloom(path string, mBits uint32, k uint8, bits []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(bloomMagic[:]); err != nil {
		return err
	}
	if _, err := f.Write([]byte{bloomVersion, k}); err != nil {
		return err
	}
	var mBuf [4]byte
	binary.LittleEndian.PutUint32(mBuf[:], mBits)
	if _, err := f.Write(mBuf[:]); err != nil {
		return err
	}
	if _, err := f.Write(bits); err != nil {
		return err
	}
	return nil
}

// doubleHash 与 internal/common/pwdpolicy/bloom.go 中一致(两处必须严格对齐,
// 任一改动必须同时改另一侧并重建产物)。
func doubleHash(s string) (uint64, uint64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	h1 := h.Sum64()

	h.Reset()
	_, _ = h.Write([]byte{0x5a})
	_, _ = h.Write([]byte(s))
	h2 := h.Sum64()
	if h2 == 0 {
		h2 = 0x9e3779b97f4a7c15
	}
	return h1, h2
}

