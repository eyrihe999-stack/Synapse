// verification.go 邮箱/短信验证码生成 + 密码重置 token 生成。
package verification

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
)

// GenerateVerificationCode 生成 6 位数字验证码(带前导 0 补齐)。
// 基于 crypto/rand,不可预测。
//sayso-lint:ignore godoc-error-undoc
func GenerateVerificationCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("generate verification code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// GenerateResetToken 生成 32 字节 crypto/rand base64url(no padding) token,
// 用于密码重置链接。输出 43 字符,url-safe,可直接放 query string。
//sayso-lint:ignore godoc-error-undoc
func GenerateResetToken() (string, error) {
	var b [32]byte
	//sayso-lint:ignore err-swallow
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
