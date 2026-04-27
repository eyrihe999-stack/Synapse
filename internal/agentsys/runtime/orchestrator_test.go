package runtime

import (
	"reflect"
	"testing"
)

func TestParseMentionCSV(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []uint64
	}{
		{"empty", "", nil},
		{"single", "42", []uint64{42}},
		{"multiple", "1,2,3", []uint64{1, 2, 3}},
		{"with spaces not handled", "1, 2,3", []uint64{1, 3}}, // " 2" parse 失败跳过
		{"trailing comma", "1,2,", []uint64{1, 2}},
		{"leading comma", ",1,2", []uint64{1, 2}},
		{"garbage items skipped", "1,abc,3", []uint64{1, 3}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMentionCSV(tc.raw)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseMentionCSV(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestContainsPID(t *testing.T) {
	ids := []uint64{1, 2, 3}
	if !containsPID(ids, 2) {
		t.Error("expected contains 2")
	}
	if containsPID(ids, 99) {
		t.Error("expected not contains 99")
	}
	if containsPID(nil, 1) {
		t.Error("expected not contains on nil")
	}
}

func TestExtractErrorMessage(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{`{"ok":false,"error":"boom"}`, "boom"},
		{`not json`, "not json"},
	}
	for _, tc := range tests {
		got := extractErrorMessage(tc.raw)
		if got != tc.want {
			t.Errorf("extractErrorMessage(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
