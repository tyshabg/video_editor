package llm

import (
	"bytes"
	"context"
	"fmt"
	"hash/fnv"
	"image/jpeg"
)

// Mock is an offline stand-in that returns deterministic answers and
// realistic token estimates, so the pipeline (and its cost accounting) can be
// exercised without spending money.
type Mock struct {
	model string
}

// NewMock returns a mock reporting the given model name for pricing.
func NewMock(model string) *Mock {
	if model == "" {
		model = DefaultModel
	}
	return &Mock{model: model}
}

func (m *Mock) Model() string { return m.model }

func (m *Mock) Describe(_ context.Context, req DescribeRequest) (DescribeResult, error) {
	var in int64 = describePromptTokens
	for _, f := range req.Frames {
		w, h := 1280, 720
		if cfg, err := jpeg.DecodeConfig(bytes.NewReader(f)); err == nil {
			w, h = cfg.Width, cfg.Height
		}
		in += ImageTokens(w, h)
	}
	text := fmt.Sprintf("[mock] Segment %s-%s: %d frames, no description generated.", Timecode(req.Start), Timecode(req.End), len(req.Frames))
	return DescribeResult{Text: text, Usage: Usage{Input: in, Output: describeOutputTokens}}, nil
}

func (m *Mock) Score(_ context.Context, req ScoreRequest) (ScoreResult, error) {
	h := fnv.New32a()
	h.Write([]byte(req.Current.Description))
	score := int(h.Sum32()%10) + 1
	u := EstimateScore(len(req.Context))
	return ScoreResult{Score: score, Reason: "[mock] deterministic pseudo-score", Raw: fmt.Sprintf(`{"score":%d,"reason":"mock"}`, score), Usage: u}, nil
}
