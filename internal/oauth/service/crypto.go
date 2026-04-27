package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// randomHex 生成 n 字节随机数 hex 编码(2n 字符)。
func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// randomBase64URL 生成 n 字节随机 base64url (no padding),用于 token 主体。
func randomBase64URL(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// sha256Hex 计算 sha256 hex。Token 存 DB 前走这个,比明文安全、查询时 UNIQUE 索引精确匹配。
// 选 sha256 不选 bcrypt:token 是高熵 random(相当于随机 hash 输入),彩虹表 / 字典攻击无效;
// UNIQUE 索引走等值查询,比 bcrypt 遍历所有 hash 匹配快几个数量级。
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
