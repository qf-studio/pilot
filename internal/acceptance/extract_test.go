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

// TestHouseStylePatterns pins the named subset's contents so reordering or
// extending MarkdownPatterns cannot silently change what Jira picks up.
func TestHouseStylePatterns(t *testing.T) {
	want := []string{
		`(?im)^##[ \t]+acceptance:?[ \t]*\r?\n([\s\S]*?)(?:\n#{1,2}[ \t]|\z)`,
		`(?im)^###[ \t]+acceptance:?[ \t]*\r?\n([\s\S]*?)(?:\n#{1,3}[ \t]|\z)`,
	}
	got := HouseStylePatterns()
	if len(got) != len(want) {
		t.Fatalf("HouseStylePatterns() has %d patterns, want %d", len(got), len(want))
	}
	for i, re := range got {
		if re.String() != want[i] {
			t.Errorf("pattern %d = %q, want %q", i, re.String(), want[i])
		}
	}

	headings := []struct {
		body string
		idx  int
	}{
		{"## Acceptance\n- x\n", 0},
		{"### Acceptance\n- x\n", 1},
	}
	for _, h := range headings {
		for i, re := range got {
			if matched := re.MatchString(h.body); matched != (i == h.idx) {
				t.Errorf("pattern %d on %q: matched=%v, want %v", i, h.body, matched, i == h.idx)
			}
		}
	}
	if got[0].MatchString("## Acceptance Criteria\n- x\n") {
		t.Error("house-style pattern must not match \"## Acceptance Criteria\"")
	}
}

// TestHouseStylePatternsSubsetOfMarkdownPatterns guards that the named subset
// is still part of the full list (the tail, as Extract relies on it).
func TestHouseStylePatternsSubsetOfMarkdownPatterns(t *testing.T) {
	all := MarkdownPatterns()
	house := HouseStylePatterns()
	tail := all[len(all)-len(house):]
	for i := range house {
		if house[i].String() != tail[i].String() {
			t.Errorf("house-style pattern %d missing from MarkdownPatterns()", i)
		}
	}
}

func TestExportedItemMatchers(t *testing.T) {
	if m := CheckboxItemRe.FindStringSubmatch("- [x] done"); len(m) < 2 || m[1] != "done" {
		t.Errorf("CheckboxItemRe = %v", m)
	}
	if m := ListItemRe.FindStringSubmatch("- item"); len(m) < 2 || m[1] != "item" {
		t.Errorf("ListItemRe = %v", m)
	}
}
