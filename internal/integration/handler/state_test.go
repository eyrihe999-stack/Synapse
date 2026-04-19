package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestStateSigner_SignVerifyRoundTrip(t *testing.T) {
	signer, err := NewStateSigner([]byte("test-secret-at-least-16-bytes-long"))
	if err != nil {
		t.Fatalf("NewStateSigner: %v", err)
	}
	state, err := signer.Sign(42, 7)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	userID, orgID, err := signer.Verify(state)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if userID != 42 || orgID != 7 {
		t.Errorf("got (%d,%d), want (42,7)", userID, orgID)
	}
}

func TestStateSigner_RejectTampered(t *testing.T) {
	signer, _ := NewStateSigner([]byte("test-secret-at-least-16-bytes-long"))
	state, _ := signer.Sign(42, 7)
	raw, _ := base64.RawURLEncoding.DecodeString(state)
	raw[5] ^= 0xff // flip a byte in user_id
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	if _, _, err := signer.Verify(tampered); !errors.Is(err, ErrInvalidState) {
		t.Errorf("tampered state accepted: err=%v", err)
	}
}

func TestStateSigner_RejectWrongSecret(t *testing.T) {
	a, _ := NewStateSigner([]byte("alpha-secret-at-least-16-bytes-X"))
	b, _ := NewStateSigner([]byte("beta-secret-at-least-16-bytes-XX"))
	state, _ := a.Sign(1, 1)
	if _, _, err := b.Verify(state); !errors.Is(err, ErrInvalidState) {
		t.Errorf("state signed by A accepted by B: err=%v", err)
	}
}

func TestStateSigner_Expired(t *testing.T) {
	signer, _ := NewStateSigner([]byte("test-secret-at-least-16-bytes-long"))
	state, _ := signer.Sign(42, 7)
	// 强制"过期":直接篡改 exp 字段成过去,再重新签名(内部不可访问,只能走公共 API)。
	// 这里换个路子:构造超短 TTL signer 直接校验后 sleep,但不想让测试慢。
	// 改为用 monkey patch time: 但 Go 不方便。替代方案:base64 解码后改 exp,重新 HMAC(已知 secret)。
	raw, _ := base64.RawURLEncoding.DecodeString(state)
	// 把 exp 设为 1970 年:
	for i := 17; i < 25; i++ {
		raw[i] = 0
	}
	// 重新签名(内部 secret 我们已知)
	signature := hmacSHA256([]byte("test-secret-at-least-16-bytes-long"), raw[:statePayloadLen])
	copy(raw[statePayloadLen:], signature)

	expired := base64.RawURLEncoding.EncodeToString(raw)
	if _, _, err := signer.Verify(expired); !errors.Is(err, ErrStateExpired) {
		t.Errorf("expired state not rejected: err=%v", err)
	}
}

func TestStateSigner_ShortSecretRejected(t *testing.T) {
	if _, err := NewStateSigner([]byte("short")); err == nil {
		t.Error("short secret should error")
	}
}

func TestStateSigner_StateLooksURLSafe(t *testing.T) {
	signer, _ := NewStateSigner([]byte("test-secret-at-least-16-bytes-long"))
	state, _ := signer.Sign(1, 1)
	// base64url 不含 + / =,URL 里直接嵌入安全。
	for _, ch := range state {
		if ch == '+' || ch == '/' || ch == '=' {
			t.Errorf("state contains URL-unfriendly char %q: %s", ch, state)
		}
	}
	// 大致 ~98 字符(base64url of 73 bytes)。
	if len(state) < 80 || len(state) > 120 {
		t.Errorf("state length %d outside expected range", len(state))
	}
	_ = time.Now() // 引用 time 避免 import 警告
}

// hmacSHA256 测试用的 helper,复现 Sign 的内部签名逻辑。
func hmacSHA256(secret, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return mac.Sum(nil)
}
