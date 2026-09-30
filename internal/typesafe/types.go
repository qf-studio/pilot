package typesafe

import "context"

// Question is one System One question. Type is "choice" for the only kind
// gates use today.
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions,omitempty"`
	Criteria     any    `json:"criteria,omitempty"`
}

// ChoiceQuestion builds a choice question. criteria maps each option to its
// rubric text, or nil for an option without a rubric (max 255 options).
func ChoiceQuestion(instructions string, criteria map[string]any) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: criteria}
}

// Answer is the model's answer to one question.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Confidence is computed server-side from the probability spread.
	Confidence float64 `json:"confidence"`
	// Noul is carried for completeness; no gate uses it yet.
	Noul float64 `json:"noul,omitempty"`
}

// Answers is the response to one Ask call, keyed by the question keys supplied.
type Answers struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Asker is what gates depend on, so tests can substitute a fake.
type Asker interface {
	Ask(ctx context.Context, state any, questions map[string]Question) (Answers, error)
}

var _ Asker = (*Client)(nil)
