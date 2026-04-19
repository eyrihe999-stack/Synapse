// session.go /oauth 流程用的短寿命 HMAC-签名 cookie。
//
// 目的:让用户在 /oauth/login 登完后,/oauth/authorize 能识别"这是同一个已登录的浏览器"。
// 不复用 web JWT —— 作用域窄(Path=/oauth),TTL 短(10 min 刚好够走完 OAuth 流程)。
//
// Cookie 格式:"<userID>.<unix_expiry>.<hmac_base64url>"
//   - HMAC 用 cfg.CookieSecret 签 "<userID>.<unix_expiry>" 前缀
//   - 验证时先看 expiry 再比 hmac,避免把"已过期 + 伪造签名"和"未过期 + 伪造签名"区分开
//
// 不用 JWT 是故意的:JWT 库最小 2 KB overhead,而本场景只需要 "userID + exp + 签名" 三件套,
// 手写更短更快。
package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	flowCookieName = "synapse_oauth_flow"
	flowCookiePath = "/oauth"
	flowCookieTTL  = 10 * time.Minute
)

type flowCookie struct {
	secret []byte
	secure bool
}

func newFlowCookie(secret []byte, secure bool) (*flowCookie, error) {
	if len(secret) < 32 {
		return nil, errors.New("cookie secret must be >= 32 bytes")
	}
	return &flowCookie{secret: secret, secure: secure}, nil
}

// Issue 签发 cookie 并写到响应。TTL 固定为 flowCookieTTL。
func (f *flowCookie) Issue(c *gin.Context, userID uint64) {
	exp := time.Now().Add(flowCookieTTL).Unix()
	payload := fmt.Sprintf("%d.%d", userID, exp)
	mac := f.sign(payload)
	value := payload + "." + mac

	// SameSite=Lax:支持 OAuth 标准的 top-level redirect(GET /authorize),
	// 禁止 cross-site POST(避免 CSRF 伪造 consent submit)。
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(flowCookieName, value, int(flowCookieTTL.Seconds()), flowCookiePath, "", f.secure, true)
}

// Read 从请求里读 cookie 并校验。ok = 校验通过且未过期。
// 过期 / 签名不对 / 格式不对统一返 (0, false) —— 外部 observation 一致,不区分。
func (f *flowCookie) Read(c *gin.Context) (uint64, bool) {
	raw, err := c.Cookie(flowCookieName)
	if err != nil || raw == "" {
		return 0, false
	}
	parts := strings.SplitN(raw, ".", 3)
	if len(parts) != 3 {
		return 0, false
	}
	userID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return 0, false
	}
	expectedMAC := f.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(expectedMAC), []byte(parts[2])) {
		return 0, false
	}
	return userID, true
}

// Clear 删 cookie(登出或流程结束时调)。
func (f *flowCookie) Clear(c *gin.Context) {
	c.SetCookie(flowCookieName, "", -1, flowCookiePath, "", f.secure, true)
}

// sign HMAC-SHA256,输出 base64url no padding(和 JWT 签名一致风格)。
func (f *flowCookie) sign(payload string) string {
	mac := hmac.New(sha256.New, f.secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
