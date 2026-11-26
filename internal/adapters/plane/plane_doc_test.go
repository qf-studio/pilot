package plane

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const planeDocPath = "../../../docs/content/integrations/plane.mdx"

func readPlaneDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(planeDocPath)
	if err != nil {
		t.Fatalf("read %s: %v", planeDocPath, err)
	}
	return string(b)
}

// planeDocSection returns the text of the section under heading, up to the
// next heading of the same or a higher level.
func planeDocSection(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("plane.mdx has no heading %q", heading)
	}
	level := strings.Index(heading, " ")
	var out []string
	for _, line := range strings.Split(doc[i+len(heading)+2:], "\n") {
		if n := len(line) - len(strings.TrimLeft(line, "#")); n > 0 && n <= level && strings.HasPrefix(line[n:], " ") {
			break
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// mutation pin: the Task ID section says the task ID is PLANE-{sequence number} on both paths, with no TASK-PLANE-42 for the webhook path -> this test fails
func TestPlaneDocTaskIDNamesWebhookTaskID(t *testing.T) {
	sec := planeDocSection(t, readPlaneDoc(t), "## Task ID Format")
	if strings.Contains(sec, "`PLANE-{sequence number}`, on both paths") {
		t.Error("Task ID section says the task ID is PLANE-{sequence number} on both paths; the webhook path gives TASK-PLANE-42 (orchestrator.ProcessPlaneTicket, bridge.PlanTicket)")
	}
	if !strings.Contains(sec, "`TASK-PLANE-42`") {
		t.Error("Task ID section does not name the webhook task ID TASK-PLANE-42")
	}
	if !strings.Contains(sec, "`pilot/PLANE-42`") {
		t.Error("Task ID section does not name the branch pilot/PLANE-42")
	}
}

// mutation pin: the Priority section says that no path maps the text priority, or does not say that the webhook path does not read it -> this test fails
func TestPlaneDocPollPathMapsTextPriorityWebhookDoesNotRead(t *testing.T) {
	sec := planeDocSection(t, readPlaneDoc(t), "### Priority")
	if strings.Contains(sec, "no path maps the text priority") {
		t.Error("Priority section says no path maps the text priority; the poll path maps it (studio-sdk Priority.UnmarshalJSON, sdkshim.PriorityFromSDK)")
	}
	if !strings.Contains(sec, "The poll path maps the text priority to the task priority") {
		t.Error("Priority section does not say that the poll path maps the text priority to the task priority")
	}
	if !strings.Contains(sec, "The webhook path does not read the priority") {
		t.Error("Priority section does not say that the webhook path does not read the priority")
	}
}

// mutation pin: the priority warning says "a later release" without the studio-sdk version that go.mod pins -> this test fails
func TestPlaneDocPriorityWarningNamesPinnedSDK(t *testing.T) {
	sec := planeDocSection(t, readPlaneDoc(t), "### Priority")
	if !strings.Contains(sec, "decodes the priority only as a number") {
		return // the warning is gone: nothing to pin
	}
	gomod, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	m := regexp.MustCompile(`github\.com/qf-studio/studio-sdk (v\S+)`).FindSubmatch(gomod)
	if m == nil {
		t.Fatal("go.mod has no github.com/qf-studio/studio-sdk requirement")
	}
	version := string(m[1])
	if !strings.Contains(sec, "`github.com/qf-studio/studio-sdk` "+version) {
		t.Errorf("priority warning does not name the pinned SDK version %s; update the warning or remove it if %s decodes the text priority", version, version)
	}
	if strings.Contains(sec, "with a later release") {
		t.Error("priority warning still says \"a later release\" without naming when it stops applying")
	}
}
