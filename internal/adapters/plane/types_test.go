package plane

import (
	"encoding/json"
	"testing"
)

func TestPriority_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Priority
		wantErr bool
	}{
		{name: "string none", input: `"none"`, want: PriorityNone},
		{name: "string urgent", input: `"urgent"`, want: PriorityUrgent},
		{name: "string high", input: `"high"`, want: PriorityHigh},
		{name: "string medium", input: `"medium"`, want: PriorityMedium},
		{name: "string low", input: `"low"`, want: PriorityLow},
		{name: "string uppercase", input: `"HIGH"`, want: PriorityHigh},
		{name: "string empty", input: `""`, want: PriorityNone},
		{name: "null", input: `null`, want: PriorityNone},
		{name: "int 0", input: `0`, want: PriorityNone},
		{name: "int 1", input: `1`, want: PriorityUrgent},
		{name: "int 2", input: `2`, want: PriorityHigh},
		{name: "int 3", input: `3`, want: PriorityMedium},
		{name: "int 4", input: `4`, want: PriorityLow},
		{name: "numeric string", input: `"2"`, want: PriorityHigh},
		{name: "unknown string", input: `"critical"`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got Priority
			err := json.Unmarshal([]byte(tt.input), &got)
			if (err != nil) != tt.wantErr {
				t.Fatalf("UnmarshalJSON(%s) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("UnmarshalJSON(%s) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestWorkItem_UnmarshalStringPriority(t *testing.T) {
	raw := `{"id":"wi-1","name":"Fix bug","state":"st-1","priority":"urgent","labels":["lbl-1"]}`
	var wi WorkItem
	if err := json.Unmarshal([]byte(raw), &wi); err != nil {
		t.Fatalf("json.Unmarshal(WorkItem) error = %v", err)
	}
	if wi.Priority != PriorityUrgent {
		t.Errorf("wi.Priority = %d, want %d (PriorityUrgent)", wi.Priority, PriorityUrgent)
	}
}
