package main

import "testing"

func TestScopedProjectPath(t *testing.T) {
	const path = "/home/user/myproject"
	tests := []struct {
		scope string
		want  string
	}{
		{"project", path},
		{"all", ""},
		{"", path},
	}
	for _, tt := range tests {
		got := scopedProjectPath(tt.scope, path)
		if got != tt.want {
			t.Errorf("scopedProjectPath(%q, %q) = %q, want %q", tt.scope, path, got, tt.want)
		}
	}
}
