package memory

import "testing"

func TestContains(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		keyword string
		want    bool
	}{
		// Basic substring matching
		{name: "exact match", text: "deadline", keyword: "deadline", want: true},
		{name: "prefix match", text: "deadline exceeded", keyword: "deadline", want: true},
		{name: "suffix match", text: "context deadline", keyword: "deadline", want: true},
		{name: "middle match", text: "context deadline exceeded", keyword: "deadline", want: true},
		{name: "no match", text: "context timeout exceeded", keyword: "deadline", want: false},

		// Case insensitivity
		{name: "case insensitive - lower in upper", text: "DEADLINE EXCEEDED", keyword: "deadline", want: true},
		{name: "case insensitive - upper in lower", text: "deadline exceeded", keyword: "DEADLINE", want: true},
		{name: "case insensitive - mixed", text: "DeadLine Exceeded", keyword: "dEADlINE", want: true},

		// Edge cases
		{name: "empty keyword", text: "any text", keyword: "", want: true},
		{name: "empty text", text: "", keyword: "keyword", want: false},
		{name: "both empty", text: "", keyword: "", want: true},
		{name: "keyword longer than text", text: "short", keyword: "much longer keyword", want: false},
		{name: "single char match", text: "abc", keyword: "b", want: true},
		{name: "single char no match", text: "abc", keyword: "d", want: false},

		// Real-world patterns from the issue
		{name: "issue example - should match", text: "context deadline exceeded", keyword: "deadline", want: true},
		{name: "error pattern match", text: "connection reset by peer", keyword: "reset", want: true},
		{name: "error pattern match middle", text: "failed to connect: timeout waiting", keyword: "timeout", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := contains(tt.text, tt.keyword)
			if got != tt.want {
				t.Errorf("contains(%q, %q) = %v, want %v", tt.text, tt.keyword, got, tt.want)
			}
		})
	}
}
