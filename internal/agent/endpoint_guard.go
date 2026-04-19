// endpoint_guard.go agent endpoint 的 SSRF 防护。
//
// 两道防线:
//  1. ValidateEndpointURL — 创建/更新 agent 时,解析 URL 字面量,拒绝明显指向内网/
//     元数据地址的配置(挡低级攻击,提前报错)。
//  2. NewGuardedTransport + NoRedirectPolicy — chat 真正拨号时,在 DNS 解析之后、
//     connect 之前再校验一次(挡 DNS rebinding / 解析到内网)。禁 redirect(挡
//     upstream 返回 302 到 169.254.169.254 之类绕过)。
//
// 封禁策略分两档:
//   - 硬封(不受 allowPrivate 影响):loopback / link-local(含云元数据 169.254.x)/
//     unspecified / multicast。这些永远不该是合法 agent 的目标。
//   - 软封(allowPrivate=false 才拦):RFC1918 + IPv6 ULA。Docker/K8s 同网部署时
//     agent 就在这些网段里,必须放行;纯公网部署时可 opt-in 封掉。
package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// isBlockedIP 判断 ip 是否应被 endpoint 校验拦截。
// allowPrivate=true 时仅拦硬封档;=false 时把 RFC1918 + IPv6 ULA 也拦掉。
func isBlockedIP(ip net.IP, allowPrivate bool) bool {
	if ip == nil {
		return true
	}
	// 硬封:loopback(127.x / ::1)、link-local(含 169.254.169.254 云元数据、
	// fe80::/10)、link-local multicast(224.0.0.0/24 / ff02::/16)、unspecified
	// (0.0.0.0 / ::)、multicast(224.0.0.0/4 / ff00::/8)。
	if ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() {
		return true
	}
	// 软封:RFC1918 (10/8, 172.16/12, 192.168/16) + IPv6 ULA (fc00::/7)。
	// net.IP.IsPrivate 覆盖这些段。
	if !allowPrivate && ip.IsPrivate() {
		return true
	}
	return false
}

// ValidateEndpointURL 校验 agent endpoint URL 格式。
//   - scheme 必须是 http/https
//   - host 不能为空
//   - host 若是 IP 字面量,按 isBlockedIP 规则拦截
//
// 域名 host 不在此层做 DNS 查询(会阻塞接口 + DNS rebinding 骗得过),真正的域名
// 解析后校验交给 NewGuardedTransport 的 Dialer.ControlContext。
// 返回 ErrAgentEndpointInvalid,与原 validateEndpoint 保持一致。
func ValidateEndpointURL(raw string, allowPrivate bool) error {
	if raw == "" {
		return fmt.Errorf("endpoint empty: %w", ErrAgentEndpointInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("endpoint parse: %w: %w", err, ErrAgentEndpointInvalid)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint scheme must be http/https: %w", ErrAgentEndpointInvalid)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("endpoint host empty: %w", ErrAgentEndpointInvalid)
	}
	// 仅当 host 是 IP 字面量时在此层校验;域名留给拨号阶段再解析再校验。
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip, allowPrivate) {
			return fmt.Errorf("endpoint points to forbidden IP %s: %w", host, ErrAgentEndpointInvalid)
		}
	}
	return nil
}

// NewGuardedTransport 构造带 SSRF 防护的 http.Transport,用于 chat 转发上游请求。
// ControlContext 在 DNS 解析之后、connect 之前触发,拿到最终要连的 IP 做二次校验,
// 挡 DNS rebinding(创建时域名合法,解析到内网 IP)。
// 连接池参数沿用原 chat_service 默认值(MaxIdleConns=500, PerHost=50, MaxConns=200)。
func NewGuardedTransport(allowPrivate bool) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		ControlContext: func(_ context.Context, network, address string, _ syscall.RawConn) error {
			// Control 阶段 address 一定是 "IP:port"(已过 DNS),这里只校验 IP。
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("split dial addr %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				// 理论上不会发生,保守拒绝避免被"不认识的地址形式"绕过。
				return fmt.Errorf("dial addr %q not a parseable IP", address)
			}
			if isBlockedIP(ip, allowPrivate) {
				return fmt.Errorf("dial blocked for IP %s (forbidden range)", ip)
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext:         dialer.DialContext,
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 50,
		MaxConnsPerHost:     200,
		IdleConnTimeout:     90 * time.Second,
	}
}

// NoRedirectPolicy 作为 http.Client.CheckRedirect,拒绝跟随任何 3xx redirect。
// 避免 upstream 返回 Location 指向内网/元数据地址绕过 URL + Dialer 两层校验。
// 返回 ErrUseLastResponse 让 3xx 原样返回,由 parseUpstreamResponse 把非 2xx
// 当作上游错误(映射到 ErrChatUpstreamUnreachable)处理。
func NoRedirectPolicy(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}
