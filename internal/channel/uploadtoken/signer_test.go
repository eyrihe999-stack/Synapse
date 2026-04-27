package uploadtoken

import (
	"errors"
	"testing"
	"time"
)

func TestSigner_RoundTrip(t *testing.T) {
	s, err := NewSigner()
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	in := Payload{
		DocumentID: 42,
		OSSKey:     "synapse/1/channel-docs/42/uploaded/abcd.md",
		ExpiresAt:  time.Now().Add(5 * time.Minute).Unix(),
	}
	tok, err := s.Sign(in)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	out, err := s.Verify(tok)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if out.DocumentID != in.DocumentID || out.OSSKey != in.OSSKey || out.ExpiresAt != in.ExpiresAt {
		t.Fatalf("payload mismatch: in=%+v out=%+v", in, out)
	}
}

func TestSigner_DifferentSecretRejects(t *testing.T) {
	s1, _ := NewSigner()
	s2, _ := NewSigner()
	tok, _ := s1.Sign(Payload{DocumentID: 1, OSSKey: "k", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	if _, err := s2.Verify(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expect ErrInvalidToken with foreign signer, got %v", err)
	}
}

func TestSigner_TamperedRejects(t *testing.T) {
	s, _ := NewSigner()
	tok, _ := s.Sign(Payload{DocumentID: 1, OSSKey: "k", ExpiresAt: time.Now().Add(time.Minute).Unix()})
	// 改一个字符就破签名
	bad := tok[:len(tok)-1] + "X"
	if _, err := s.Verify(bad); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expect ErrInvalidToken on tamper, got %v", err)
	}
}

func TestSigner_ExpiredRejects(t *testing.T) {
	s, _ := NewSigner()
	tok, _ := s.Sign(Payload{DocumentID: 1, OSSKey: "k", ExpiresAt: time.Now().Add(-1 * time.Second).Unix()})
	if _, err := s.Verify(tok); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expect ErrTokenExpired on past expiry, got %v", err)
	}
}

func TestSigner_BadFormatRejects(t *testing.T) {
	s, _ := NewSigner()
	for _, bad := range []string{"", "no-dot", "abc.def.ghi", "!!!.???", "."} {
		if _, err := s.Verify(bad); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expect ErrInvalidToken on %q, got %v", bad, err)
		}
	}
}
