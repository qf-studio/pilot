package typesafe

import (
	"encoding/json"
	"testing"
)

func TestChoiceQuestion(t *testing.T) {
	q := ChoiceQuestion("pick one", map[string]any{"a": "rubric a", "b": nil})
	if q.Type != "choice" {
		t.Errorf("type = %q", q.Type)
	}
	raw, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["instructions"] != "pick one" {
		t.Errorf("instructions = %v", got["instructions"])
	}
	crit, ok := got["criteria"].(map[string]any)
	if !ok || len(crit) != 2 || crit["a"] != "rubric a" {
		t.Errorf("criteria = %v", got["criteria"])
	}
	if v, present := crit["b"]; !present || v != nil {
		t.Errorf("null rubric must serialise as null, got %v (present=%v)", v, present)
	}
}

func TestAnswers_Unmarshal(t *testing.T) {
	raw := `{"model":"jev-latest","answers":{"k":{"type":"choice","choice":"x","probabilities":{"x":0.9,"y":0.1},"confidence":0.8}},"usage":{"input_tokens":12,"output_tokens":3}}`
	var a Answers
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	if a.Model != "jev-latest" || a.Usage.InputTokens != 12 || a.Usage.OutputTokens != 3 {
		t.Errorf("answers = %+v", a)
	}
	if ans := a.Answers["k"]; ans.Choice != "x" || ans.Confidence != 0.8 || ans.Probabilities["x"] != 0.9 {
		t.Errorf("answer = %+v", ans)
	}
}
