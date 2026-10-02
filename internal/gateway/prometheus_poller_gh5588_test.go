package gateway

import (
	"bytes"
	"strings"
	"testing"
)

type fakePollerUpSource map[string]bool

func (f fakePollerUpSource) PollerUpSnapshot() map[string]bool { return f }

func TestPrometheusExporter_PollerUp(t *testing.T) {
	e := NewPrometheusExporter(&mockMetricsSource{})

	var buf bytes.Buffer
	if err := e.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "pilot_poller_up") {
		t.Error("pilot_poller_up must be absent when no poller source is wired")
	}

	e.SetPollerSource(fakePollerUpSource{"linear": false, "jira": true})
	buf.Reset()
	if err := e.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# TYPE pilot_poller_up gauge",
		`pilot_poller_up{adapter="jira"} 1`,
		`pilot_poller_up{adapter="linear"} 0`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}
