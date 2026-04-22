// Package pwdpolicy 实现 M1.5 密码策略:最短长度 + top-N 常用密码拒绝。
//
// 弱密名单以离线构建的 bloom filter 存在同目录的 weak_passwords.bloom,
// 由 cmd/gen-pwd-bloom 基于 SecLists 的 top-10k 列表生成,产物 check-in。
// 运行时用 //go:embed 打包进二进制,无网络/文件 IO 依赖。
//
// 假阳性率默认 0.1%,约 10 个正常密码会被误拦 —— 文案需明确
// "换一个更独特的密码",而不是"密码错误"。
package pwdpolicy

import (
	_ "embed"
	"fmt"
	"strings"
)

// weakBloomData top-10k 弱密 bloom filter 原始字节。
// 由 cmd/gen-pwd-bloom 生成,不要手改。
//
//go:embed weak_passwords.bloom
var weakBloomData []byte

// DefaultMinLen 默认最短长度,对应 M1.5 要求。
const DefaultMinLen = 10

// Policy 密码策略,构造后并发安全(内部只读)。
type Policy struct {
	minLen        int
	weak          *readOnlyBloom
	checkWeakList bool
}

// Option Policy 构造选项。
type Option func(*Policy)

// WithMinLen 覆盖默认最短长度。<=0 视为无效,走 DefaultMinLen。
func WithMinLen(n int) Option {
	return func(p *Policy) {
		if n > 0 {
			p.minLen = n
		}
	}
}

// WithWeakListCheck 打开/关闭弱密名单校验。默认 true;
// 本地调试或单测需要用弱密时可关。
func WithWeakListCheck(enable bool) Option {
	return func(p *Policy) { p.checkWeakList = enable }
}

// New 构造一个 Policy。bloom 产物异常(embed 空/格式坏)返回 error,
// 调用方应 fail-fast —— 带破损策略跑比拒绝启动更危险。
func New(opts ...Option) (*Policy, error) {
	p := &Policy{
		minLen:        DefaultMinLen,
		checkWeakList: true,
	}
	for _, opt := range opts {
		opt(p)
	}
	if len(weakBloomData) == 0 {
		return nil, fmt.Errorf("pwdpolicy: embedded weak list is empty — run `go run ./cmd/gen-pwd-bloom` to build it")
	}
	b, err := loadBloom(weakBloomData)
	if err != nil {
		return nil, fmt.Errorf("pwdpolicy: load weak list: %w", err)
	}
	p.weak = b
	return p, nil
}

// MinLen 返回当前最短长度策略,handler 可据此返回前端提示。
func (p *Policy) MinLen() int { return p.minLen }

// Validate 按策略校验一个候选密码。
//
// 返回:
//   - ErrTooShort:长度不足
//   - ErrTooCommon:命中弱密 bloom(含 ~0.1% 假阳性)
//   - nil:通过
//
// 弱密查询前统一 ToLower,和构建时一致。
func (p *Policy) Validate(pw string) error {
	if len(pw) < p.minLen {
		return ErrTooShort
	}
	if p.checkWeakList && p.weak != nil {
		if p.weak.test(strings.ToLower(pw)) {
			return ErrTooCommon
		}
	}
	return nil
}
