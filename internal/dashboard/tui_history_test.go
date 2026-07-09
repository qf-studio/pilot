package dashboard

import (
	"testing"

	"github.com/qf-studio/pilot/internal/memory"
)

func taskIDs(execs []*memory.Execution) []string {
	out := make([]string, len(execs))
	for i, e := range execs {
		out[i] = e.TaskID
	}
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFirstNDistinctByTask(t *testing.T) {
	ex := func(id, status string) *memory.Execution {
		return &memory.Execution{TaskID: id, Status: status}
	}

	tests := []struct {
		name string
		in   []*memory.Execution
		n    int
		want []string
	}{
		{
			name: "retried task does not crowd out the rest (GH-4100 scenario)",
			// created_at DESC: GH-4100 retried 4×, then distinct tasks.
			in: []*memory.Execution{
				ex("GH-4100", "skipped"), ex("GH-4100", "completed"), ex("GH-4100", "completed"),
				ex("GH-4100", "completed"), ex("GH-4105", "no_op"), ex("GH-4106", "no_op"),
				ex("GH-4107", "failed"), ex("GH-4101", "completed"),
			},
			n:    5,
			want: []string{"GH-4100", "GH-4105", "GH-4106", "GH-4107", "GH-4101"},
		},
		{
			name: "keeps first (latest) occurrence per task",
			in:   []*memory.Execution{ex("A", "skipped"), ex("A", "completed"), ex("B", "completed")},
			n:    5,
			want: []string{"A", "B"},
		},
		{
			name: "caps at n distinct tasks",
			in:   []*memory.Execution{ex("A", "c"), ex("B", "c"), ex("C", "c"), ex("D", "c")},
			n:    2,
			want: []string{"A", "B"},
		},
		{
			name: "fewer distinct than n returns all",
			in:   []*memory.Execution{ex("A", "c"), ex("A", "c")},
			n:    5,
			want: []string{"A"},
		},
		{name: "n<=0 returns nil", in: []*memory.Execution{ex("A", "c")}, n: 0, want: nil},
		{name: "empty input", in: nil, n: 5, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskIDs(firstNDistinctByTask(tt.in, tt.n))
			if len(tt.want) == 0 && len(got) == 0 {
				return
			}
			if !eq(got, tt.want) {
				t.Errorf("firstNDistinctByTask = %v, want %v", got, tt.want)
			}
			// the kept row for a retried task must be the FIRST (latest) one
			if tt.name == "keeps first (latest) occurrence per task" {
				kept := firstNDistinctByTask(tt.in, tt.n)
				if kept[0].Status != "skipped" {
					t.Errorf("kept status = %q, want %q (latest, not an older retry)", kept[0].Status, "skipped")
				}
			}
		})
	}
}
