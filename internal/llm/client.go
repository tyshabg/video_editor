// Package llm talks to Claude for segment descriptions and narrative scores.
package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is what the pipeline uses unless overridden.
const DefaultModel = "claude-sonnet-5"

// DescribeRequest carries the frames of one segment.
type DescribeRequest struct {
	Game   string
	Lang   string
	Start  float64
	End    float64
	Times  []float64 // one per frame
	Frames [][]byte  // JPEG bytes, chronological
}

// DescribeResult is a description plus its accounting.
type DescribeResult struct {
	Text  string
	Usage Usage
}

// ScoreRequest carries the current segment and its context.
type ScoreRequest struct {
	Game     string
	Lang     string
	Current  ContextItem
	Duration float64
	Context  []ContextItem
}

// ScoreResult is a parsed score plus accounting.
type ScoreResult struct {
	Score  int
	Reason string
	Raw    string
	Usage  Usage
}

// Client is implemented by the Anthropic client and the offline mock.
type Client interface {
	Model() string
	Describe(ctx context.Context, req DescribeRequest) (DescribeResult, error)
	Score(ctx context.Context, req ScoreRequest) (ScoreResult, error)
}

// Anthropic is the real client.
type Anthropic struct {
	client anthropic.Client
	model  string
	// MaxAttempts bounds our own retry loop on top of the SDK's (429/529/5xx).
	MaxAttempts int
}

// NewAnthropic builds a client using ANTHROPIC_API_KEY from the environment.
func NewAnthropic(model string) *Anthropic {
	if model == "" {
		model = DefaultModel
	}
	return &Anthropic{
		client:      anthropic.NewClient(option.WithMaxRetries(2), option.WithRequestTimeout(120*time.Second)),
		model:       model,
		MaxAttempts: 6,
	}
}

func (a *Anthropic) Model() string { return a.model }

func usageOf(u anthropic.Usage) Usage {
	return Usage{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens, CacheWrite: u.CacheCreationInputTokens}
}

// Describe sends the frames and returns a 1-2 sentence description.
func (a *Anthropic) Describe(ctx context.Context, req DescribeRequest) (DescribeResult, error) {
	if len(req.Frames) == 0 {
		return DescribeResult{}, errors.New("no frames")
	}
	blocks := make([]anthropic.ContentBlockParamUnion, 0, 2*len(req.Frames)+1)
	for i, f := range req.Frames {
		t := 0.0
		if i < len(req.Times) {
			t = req.Times[i]
		}
		blocks = append(blocks,
			anthropic.NewTextBlock(frameLabel(i, len(req.Frames), t)),
			anthropic.NewImageBlockBase64("image/jpeg", base64.StdEncoding.EncodeToString(f)),
		)
	}
	blocks = append(blocks, anthropic.NewTextBlock(describeUser(req.Start, req.End)))
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 300,
		System:    []anthropic.TextBlockParam{{Text: describeSystem(req.Game, req.Lang)}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
	}
	msg, err := a.call(ctx, params)
	if err != nil {
		return DescribeResult{}, err
	}
	text := strings.TrimSpace(textOf(msg))
	if text == "" {
		return DescribeResult{}, fmt.Errorf("empty description (stop_reason=%s)", msg.StopReason)
	}
	return DescribeResult{Text: text, Usage: usageOf(msg.Usage)}, nil
}

var scoreSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"score":  map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
		"reason": map[string]any{"type": "string"},
	},
	"required":             []string{"score", "reason"},
	"additionalProperties": false,
}

// Score rates the current segment given its context. Structured output is
// requested; the response is still run through the tolerant parser.
func (a *Anthropic) Score(ctx context.Context, req ScoreRequest) (ScoreResult, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: 200,
		System:    []anthropic.TextBlockParam{{Text: scoreSystem(req.Game, req.Lang)}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(scoreUser(req.Current, req.Duration, req.Context)))},
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: scoreSchema},
		},
	}
	msg, err := a.call(ctx, params)
	if err != nil && isBadRequest(err) && strings.Contains(err.Error(), "output_config") {
		// Model/platform without structured outputs: fall back to prompt-only JSON.
		params.OutputConfig = anthropic.OutputConfigParam{}
		msg, err = a.call(ctx, params)
	}
	if err != nil {
		return ScoreResult{}, err
	}
	raw := textOf(msg)
	score, reason, perr := ParseScore(raw)
	if perr != nil {
		return ScoreResult{Raw: raw, Usage: usageOf(msg.Usage)}, fmt.Errorf("parse score: %w (raw: %.200s)", perr, raw)
	}
	return ScoreResult{Score: score, Reason: reason, Raw: raw, Usage: usageOf(msg.Usage)}, nil
}

func textOf(m *anthropic.Message) string {
	var b strings.Builder
	for _, blk := range m.Content {
		if t, ok := blk.AsAny().(anthropic.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// call performs the request with backoff on transient failures.
func (a *Anthropic) call(ctx context.Context, params anthropic.MessageNewParams) (*anthropic.Message, error) {
	attempts := a.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		msg, err := a.client.Messages.New(ctx, params)
		if err == nil {
			if msg.StopReason == anthropic.StopReasonRefusal {
				return nil, fmt.Errorf("model refused the request (%s)", msg.StopDetails.Category)
			}
			return msg, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isTransient(err) {
			return nil, err
		}
		delay := time.Duration(float64(2*time.Second) * float64(int(1)<<uint(i)))
		if delay > 60*time.Second {
			delay = 60 * time.Second
		}
		delay += time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, fmt.Errorf("giving up after %d attempts: %w", attempts, lastErr)
}

func isBadRequest(err error) bool {
	var apiErr *anthropic.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == 400
}

// isTransient reports whether the error is worth retrying.
func isTransient(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 408, 409, 429, 500, 502, 503, 504, 529:
			return true
		}
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "connection reset") || strings.Contains(msg, "EOF") || strings.Contains(msg, "timeout")
}
