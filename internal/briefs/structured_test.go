package briefs

import (
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qf-studio/pilot/internal/memory"
)

var structuredNow = time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func completedExec(id, keySource string, cost float64) *memory.Execution {
	return &memory.Execution{
		ID: "e-" + id, TaskID: "GH-" + id, Status: "completed", TaskTitle: "Title " + id,
		TaskSourceAdapter: "github", PRUrl: "https://github.com/acme/w/pull/" + id,
		DurationMs: 14 * 60 * 1000, LinesAdded: 88, LinesRemoved: 15,
		KeySource: keySource, EstimatedCostUSD: cost,
	}
}

func TestBuildStructured_SectionOrderInJSON(t *testing.T) {
	b := BuildStructured(structuredNow, BriefPeriod{}, nil, nil, ptr(42.0))
	raw, err := b.MarshalPayload()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	prev := -1
	for _, key := range []string{`"shipped"`, `"review_needed"`, `"pool"`, `"failed"`} {
		i := strings.Index(s, key)
		if i < 0 {
			t.Fatalf("payload missing %s: %s", key, s)
		}
		if i < prev {
			t.Errorf("section %s out of order in %s", key, s)
		}
		prev = i
	}
	if b.Schema != 1 {
		t.Errorf("schema = %d, want 1", b.Schema)
	}
}

func TestBuildStructured_CostOnlyOnOwnKey(t *testing.T) {
	tests := []struct {
		name      string
		keySource string
		wantCost  bool
	}{
		{"own key shows cost", "own", true},
		{"platform key omits cost", "platform", false},
		{"unknown key omits cost", "unknown", false},
		{"empty key omits cost", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := BuildStructured(structuredNow, BriefPeriod{}, []*memory.Execution{completedExec("1", tt.keySource, 2.75)}, nil, nil)
			if len(b.Shipped) != 1 {
				t.Fatalf("shipped = %d, want 1", len(b.Shipped))
			}
			if got := b.Shipped[0].CostUSD != nil; got != tt.wantCost {
				t.Fatalf("cost present = %v, want %v", got, tt.wantCost)
			}
			raw, _ := b.MarshalPayload()
			if has := strings.Contains(string(raw), "cost_usd"); has != tt.wantCost {
				t.Errorf("payload contains cost_usd = %v, want %v: %s", has, tt.wantCost, raw)
			}
			html, _ := NewEmailFormatter().FormatStructured(b)
			if has := strings.Contains(html, "$2.75"); has != tt.wantCost {
				t.Errorf("email contains cost = %v, want %v", has, tt.wantCost)
			}
			if tt.wantCost && *b.Shipped[0].CostUSD != 2.75 {
				t.Errorf("cost = %v, want 2.75", *b.Shipped[0].CostUSD)
			}
		})
	}
}

func TestBuildStructured_ShippedFields(t *testing.T) {
	b := BuildStructured(structuredNow, BriefPeriod{}, []*memory.Execution{completedExec("5214", "own", 1)}, nil, nil)
	got := b.Shipped[0]
	if got.IssueRef != "#5214" || got.Title != "Title 5214" || got.PRURL == "" ||
		got.DurationMs != 14*60*1000 || got.LinesAdded != 88 || got.LinesRemoved != 15 {
		t.Errorf("unexpected shipped run: %+v", got)
	}
}

func TestBuildStructured_ReviewNeededOrdering(t *testing.T) {
	h := time.Hour
	tests := []struct {
		name    string
		waiting []time.Duration // ago, in input order
		want    []string        // IssueRef order
		overdue []bool
	}{
		{"older than 24h first", []time.Duration{2 * h, 30 * h}, []string{"r1", "r0"}, []bool{true, false}},
		{"oldest first within groups", []time.Duration{3 * h, 50 * h, 5 * h, 26 * h}, []string{"r1", "r3", "r2", "r0"}, []bool{true, true, false, false}},
		{"exactly 24h is not overdue", []time.Duration{24 * h, 1 * h}, []string{"r0", "r1"}, []bool{false, false}},
		{"none overdue keeps oldest first", []time.Duration{1 * h, 4 * h}, []string{"r1", "r0"}, []bool{false, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in []ReviewInput
			for i, ago := range tt.waiting {
				in = append(in, ReviewInput{
					IssueRef: "r" + string(rune('0'+i)), Reason: "line one\nline two",
					WaitingSince: structuredNow.Add(-ago),
				})
			}
			b := BuildStructured(structuredNow, BriefPeriod{}, nil, in, nil)
			var gotRefs []string
			var gotOverdue []bool
			for _, r := range b.ReviewNeeded {
				gotRefs = append(gotRefs, r.IssueRef)
				gotOverdue = append(gotOverdue, r.Overdue)
				if r.Reason != "line one" {
					t.Errorf("reason = %q, want one line", r.Reason)
				}
			}
			if !reflect.DeepEqual(gotRefs, tt.want) {
				t.Errorf("order = %v, want %v", gotRefs, tt.want)
			}
			if !reflect.DeepEqual(gotOverdue, tt.overdue) {
				t.Errorf("overdue = %v, want %v", gotOverdue, tt.overdue)
			}
		})
	}
}

func TestBuildStructured_FailedAndPool(t *testing.T) {
	execs := []*memory.Execution{
		{TaskID: "GH-1", Status: "failed", TaskSourceAdapter: "github", Error: "claude: rate limit exceeded"},
		{TaskID: "GH-2", Status: "failed", TaskSourceAdapter: "github", Error: "something odd"},
		{TaskID: "GH-3", Status: "failed", TaskSourceAdapter: "github", Error: "timed out"},
		{TaskID: "GH-3", Status: "completed", TaskSourceAdapter: "github"}, // retried: failure hidden
	}
	b := BuildStructured(structuredNow, BriefPeriod{}, execs, nil, nil)
	want := []FailedRun{
		{IssueRef: "#1", FailureClass: FailureRateLimit, Retryable: true},
		{IssueRef: "#2", FailureClass: FailureUnknown, Retryable: false},
	}
	if !reflect.DeepEqual(b.Failed, want) {
		t.Errorf("failed = %+v, want %+v", b.Failed, want)
	}
	if b.Pool != nil {
		t.Errorf("pool = %+v, want nil when unknown", b.Pool)
	}
	if !strings.Contains(mustJSON(t, b), `"pool":null`) {
		t.Errorf("unknown pool should serialize as null: %s", mustJSON(t, b))
	}
	b = BuildStructured(structuredNow, BriefPeriod{}, nil, nil, ptr(63.5))
	if b.Pool == nil || b.Pool.PercentUsed != 63.5 {
		t.Errorf("pool = %+v, want 63.5", b.Pool)
	}
}

func TestClassifyFailure(t *testing.T) {
	tests := []struct {
		in        string
		class     string
		retryable bool
	}{
		{"HTTP 429 Too Many Requests", FailureRateLimit, true},
		{"context deadline exceeded", FailureTimeout, true},
		{"dial tcp: connection refused", FailureNetwork, true},
		{"401 Unauthorized", FailureAuth, false},
		{"merge conflict in main.go", FailureConflict, false},
		{"CI failed on PR", FailureCI, false},
		{"", FailureUnknown, false},
	}
	for _, tt := range tests {
		class, retryable := ClassifyFailure(tt.in)
		if class != tt.class || retryable != tt.retryable {
			t.Errorf("ClassifyFailure(%q) = %s,%v want %s,%v", tt.in, class, retryable, tt.class, tt.retryable)
		}
	}
}

func mustJSON(t *testing.T, b *StructuredBrief) string {
	t.Helper()
	raw, err := b.MarshalPayload()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestStructuredBrief_JSONRoundTrip(t *testing.T) {
	execs := []*memory.Execution{
		completedExec("1", "own", 1.5),
		completedExec("2", "platform", 9),
		{TaskID: "GH-3", Status: "failed", TaskSourceAdapter: "github", Error: "timeout"},
	}
	reviews := []ReviewInput{{IssueRef: "#9", Title: "T", PRURL: "https://x/pr/9", Reason: "needs approval", WaitingSince: structuredNow.Add(-30 * time.Hour)}}
	orig := BuildStructured(structuredNow, BriefPeriod{Start: structuredNow.Add(-24 * time.Hour), End: structuredNow}, execs, reviews, ptr(12.0))

	raw, err := orig.MarshalPayload()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back StructuredBrief
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(orig, &back) {
		t.Errorf("round trip mismatch:\n orig %+v\n back %+v", orig, &back)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal generic: %v", err)
	}
	if generic["schema"] != float64(1) {
		t.Errorf("schema = %v, want 1", generic["schema"])
	}
	if generic["timezone"] != "UTC" || back.Timezone != "UTC" {
		t.Errorf("timezone = %v / %q, want UTC", generic["timezone"], back.Timezone)
	}
}

// With daily_brief.timezone Europe/Berlin the payload names the zone, its
// timestamps carry the Berlin offset and the email date line and period
// bounds are rendered in Berlin time (GH-5814).
func TestStructuredBrief_TimezoneBerlin(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	config := DefaultBriefConfig()
	config.Timezone = "Europe/Berlin"
	g := NewGenerator(store, config)

	// 23:30 UTC on Oct 22 is already Oct 23, 01:30 in Berlin (CEST, +02:00).
	// The clock is fixed to the same instant so the date line does not
	// depend on the day the test runs.
	end := time.Date(2026, 10, 22, 23, 30, 0, 0, time.UTC)
	g.SetClock(func() time.Time { return end })
	sb, err := g.GenerateStructured(BriefPeriod{Start: end.Add(-24 * time.Hour), End: end})
	if err != nil {
		t.Fatalf("GenerateStructured: %v", err)
	}
	if sb.Timezone != "Europe/Berlin" {
		t.Errorf("timezone = %q, want Europe/Berlin", sb.Timezone)
	}
	if sb.Schema != 1 {
		t.Errorf("schema = %d, want 1", sb.Schema)
	}
	if !sb.PeriodEnd.Equal(end) {
		t.Errorf("period end instant changed: %v", sb.PeriodEnd)
	}
	raw := mustJSON(t, sb)
	if !strings.Contains(raw, `"timezone":"Europe/Berlin"`) || !strings.Contains(raw, `"period_end":"2026-10-23T01:30:00+02:00"`) {
		t.Errorf("payload not in Berlin time: %s", raw)
	}

	out, err := NewEmailFormatter().FormatStructured(sb)
	if err != nil {
		t.Fatalf("FormatStructured: %v", err)
	}
	if !strings.Contains(out, "Friday, October 23, 2026") {
		t.Errorf("email date line not in Berlin time:\n%s", out)
	}
	if !strings.Contains(out, "Oct 22, 01:30 – Oct 23, 01:30 CEST") {
		t.Errorf("email period not in Berlin time:\n%s", out)
	}

	// The formatter honors the payload's zone even when the timestamps
	// carry another offset.
	utc := *sb
	utc.GeneratedAt, utc.PeriodStart, utc.PeriodEnd = sb.GeneratedAt.UTC(), sb.PeriodStart.UTC(), sb.PeriodEnd.UTC()
	out, _ = NewEmailFormatter().FormatStructured(&utc)
	if !strings.Contains(out, "Oct 23, 01:30 CEST") {
		t.Errorf("email ignored Timezone field:\n%s", out)
	}
}

func TestEmailFormatter_FormatStructured(t *testing.T) {
	b := BuildStructured(structuredNow, BriefPeriod{}, []*memory.Execution{completedExec("1", "own", 1.5)}, nil, nil)
	out, err := NewEmailFormatter().FormatStructured(b)
	if err != nil {
		t.Fatalf("FormatStructured: %v", err)
	}
	prev := -1
	for _, h := range []string{"<h2>Shipped", "<h2>Review needed", "<h2>Pool", "<h2>Failed"} {
		i := strings.Index(out, h)
		if i < 0 {
			t.Fatalf("email missing heading %q:\n%s", h, out)
		}
		if i < prev {
			t.Errorf("heading %q out of order", h)
		}
		prev = i
	}
	if !strings.Contains(out, "+88 −15") {
		t.Errorf("email missing diff size: %s", out)
	}
	if subj := NewEmailFormatter().SubjectStructured(b); !strings.Contains(subj, "Oct 22, 2026") {
		t.Errorf("subject = %q", subj)
	}
}

func TestFormatReceiptLine_CostOnlyForOwnKey(t *testing.T) {
	tests := []struct {
		name      string
		keySource string
		wantCost  bool
	}{
		{"own shows cost", "own", true},
		{"platform omits cost", "platform", false},
		{"unknown omits cost", "unknown", false},
		{"empty omits cost", "", false},
		{"free text omits cost", "something-else", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exec := completedExec("1", tt.keySource, 2.75)
			if got := formatReceiptLine(exec); strings.Contains(got, "$") != tt.wantCost {
				t.Errorf("line = %q, wantCost %v", got, tt.wantCost)
			}
			if got := formatReceiptsTotal([]*memory.Execution{exec}); strings.Contains(got, "$2.75") != tt.wantCost {
				t.Errorf("total = %q, wantCost %v", got, tt.wantCost)
			}
		})
	}
}

func TestFormatReceiptsTotal_MixedSetHasNoTotal(t *testing.T) {
	own := completedExec("1", "own", 2.75)
	for _, other := range []string{"platform", "unknown", ""} {
		t.Run("own+"+other, func(t *testing.T) {
			rows := []*memory.Execution{own, completedExec("2", other, 2.75)}
			if got := formatReceiptsTotal(rows); strings.Contains(got, "$") {
				t.Errorf("mixed total = %q, want no cost", got)
			}
		})
	}
	if got := formatReceiptsTotal([]*memory.Execution{own, completedExec("3", "own", 1)}); !strings.Contains(got, "$3.75") {
		t.Errorf("all-own total = %q, want $3.75", got)
	}
}

func TestGenerator_GenerateStructuredAndSchedulerStores(t *testing.T) {
	store, err := memory.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer func() { _ = store.Close() }()

	done := time.Now().UTC().Add(-time.Hour)
	if err := store.SaveExecution(&memory.Execution{
		ID: "e1", TaskID: "GH-77", ProjectPath: "/p", Status: "completed", TaskTitle: "Ship it",
		TaskSourceAdapter: "github", KeySource: "own", EstimatedCostUSD: 3, CreatedAt: done, CompletedAt: &done,
	}); err != nil {
		t.Fatalf("SaveExecution: %v", err)
	}

	g := NewGenerator(store, DefaultBriefConfig())
	g.SetPoolSource(func() (float64, bool) { return 55, true })
	period := BriefPeriod{Start: time.Now().Add(-24 * time.Hour), End: time.Now().Add(time.Minute)}

	sb, err := g.GenerateStructured(period)
	if err != nil {
		t.Fatalf("GenerateStructured: %v", err)
	}
	if len(sb.Shipped) != 1 || sb.Shipped[0].IssueRef != "#77" || sb.Shipped[0].CostUSD == nil {
		t.Errorf("shipped = %+v", sb.Shipped)
	}
	if sb.Pool == nil || sb.Pool.PercentUsed != 55 {
		t.Errorf("pool = %+v", sb.Pool)
	}

	s := &Scheduler{generator: g, store: store, logger: nil}
	s.logger = defaultTestLogger()
	s.storeStructured(period, KindDaily)
	rows, err := store.ListBriefPayloads(5, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored payloads = %d, err %v", len(rows), err)
	}
	var back StructuredBrief
	if err := json.Unmarshal(rows[0].Payload, &back); err != nil {
		t.Fatalf("stored payload invalid: %v", err)
	}
	if back.Schema != 1 || len(back.Shipped) != 1 {
		t.Errorf("stored payload = %+v", back)
	}
}

func defaultTestLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
