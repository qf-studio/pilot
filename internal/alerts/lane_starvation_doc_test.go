package alerts

import (
	"os"
	"strings"
	"testing"
)

// mutation pin: LaneStarvationAlertStatus doc uses "reached" for both streak == threshold and streak >= threshold -> this test fails
func TestLaneStarvationAlertStatusDocReachedMeaning(t *testing.T) {
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatalf("read engine.go: %v", err)
	}
	if strings.Contains(string(src), "whose threshold the streak has reached") {
		t.Error("LaneStarvationAlertStatus doc uses \"reached\" for streak >= threshold, but \"reached\" means streak == threshold; say \"met or passed\"")
	}
}
