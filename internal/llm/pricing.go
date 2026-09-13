package llm

import "strings"

// Pricing is USD per million tokens.
type Pricing struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// Usage is token accounting for one call.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

// Add accumulates usage.
func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.Output += o.Output
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
}

// Anthropic first-party API prices (cached 2026-06). Prefix-matched so dated
// snapshots resolve too. Order matters: more specific prefixes first.
var priceTable = []struct {
	prefix string
	p      Pricing
}{
	{"claude-fable-5", Pricing{10, 50, 1.0, 12.5}},
	{"claude-mythos-5", Pricing{10, 50, 1.0, 12.5}},
	{"claude-opus-5", Pricing{5, 25, 0.5, 6.25}},
	{"claude-opus-4-8", Pricing{5, 25, 0.5, 6.25}},
	{"claude-opus-4-7", Pricing{5, 25, 0.5, 6.25}},
	{"claude-opus-4-6", Pricing{5, 25, 0.5, 6.25}},
	{"claude-sonnet-5", Pricing{2, 10, 0.2, 2.5}},
	{"claude-sonnet-4-6", Pricing{3, 15, 0.3, 3.75}},
	{"claude-sonnet-4-5", Pricing{3, 15, 0.3, 3.75}},
	{"claude-haiku-4-5", Pricing{1, 5, 0.1, 1.25}},
}

// PriceFor returns the pricing for a model; ok=false when unknown (a
// conservative Sonnet 4.6 price is returned in that case so estimates err high).
func PriceFor(model string) (Pricing, bool) {
	for _, e := range priceTable {
		if strings.HasPrefix(model, e.prefix) {
			return e.p, true
		}
	}
	return Pricing{3, 15, 0.3, 3.75}, false
}

// Cost in USD for the usage under the model's pricing.
func Cost(model string, u Usage) float64 {
	p, _ := PriceFor(model)
	return (float64(u.Input)*p.Input + float64(u.Output)*p.Output +
		float64(u.CacheRead)*p.CacheRead + float64(u.CacheWrite)*p.CacheWrite) / 1e6
}

// ImageTokens estimates vision tokens for one image.
func ImageTokens(w, h int) int64 { return int64((w*h + 749) / 750) }

// Rough per-call token envelopes used for up-front estimates.
const (
	describePromptTokens = 320 // system + labels
	describeOutputTokens = 90
	scorePromptTokens    = 420 // system + framing
	scoreCtxTokensEach   = 70  // one previous-segment line
	scoreOutputTokens    = 45
)

// EstimateDescribe returns the expected usage of one description call.
func EstimateDescribe(nFrames, w, h int) Usage {
	return Usage{Input: int64(nFrames)*ImageTokens(w, h) + describePromptTokens, Output: describeOutputTokens}
}

// EstimateScore returns the expected usage of one scoring call.
func EstimateScore(contextN int) Usage {
	return Usage{Input: scorePromptTokens + int64(contextN)*scoreCtxTokensEach + 80, Output: scoreOutputTokens}
}
