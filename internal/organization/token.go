// token.go 邀请 token 生成与 hash 计算。
//
// 设计要点:
//   - raw token 只出现在内存 + 一次性邮件链接里,DB 只存 SHA-256(token) = token_hash
//   - 即使 DB 泄漏,攻击者拿到的 hash 无法反推 raw token,也就无法伪造 accept 请求
//   - hash 用 hex 编码存(64 字符),不用 base64url,因为 DB 查询用 `=` 命中
//     直接用 uniqueIndex;hex 固定长度 + 只包含 [0-9a-f],对索引友好
//   - raw token 用 base64url(URL 安全,可直接拼 `?token=...`),32 字节原始 → ~43 字符
package organization

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// GenerateInvitationToken 生成一组邀请 token。
//
// 返回:
//   - rawToken:塞到邮件链接里发给收件人,一次性
//   - tokenHash:入库,后续 Accept 时查找匹配
//
// 错误只来自 crypto/rand 读取失败(极罕见)。
func GenerateInvitationToken() (rawToken, tokenHash string, err error) {
	buf := make([]byte, InvitationTokenRandomBytes)
	if _, readErr := rand.Read(buf); readErr != nil {
		return "", "", fmt.Errorf("generate invitation token: %w", readErr)
	}
	rawToken = base64.RawURLEncoding.EncodeToString(buf)
	tokenHash = HashInvitationToken(rawToken)
	return rawToken, tokenHash, nil
}

// HashInvitationToken 计算一个 raw token 的 SHA-256 hash(hex 编码,64 字符)。
//
// Accept / Preview 时,service 对传入的 raw token 调这个函数,再去 repo 按 token_hash 查。
func HashInvitationToken(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
