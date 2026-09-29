package acceptance

import (
	"reflect"
	"testing"
)

func TestExtract(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"acceptance criteria checkboxes", "### Acceptance Criteria\n- [ ] a\n- [x] b\n\n### Other\n- z", []string{"a", "b"}},
		{"plain list", "## Acceptance Criteria\n- one\n- two\n", []string{"one", "two"}},
		{"house style heading", "## Context\n\nx\n\n## Acceptance\n\n- p\n- q\n\n## Refs\n\n- r\n", []string{"p", "q"}},
		{"house style with colon", "## Acceptance:\n- p\n", []string{"p"}},
		{"no section", "## Context\n- x\n", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Extract(tt.body); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Extract() = %v, want %v", got, tt.want)
			}
		})
	}
}
