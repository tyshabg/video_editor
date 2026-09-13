package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ErrNoJSON is returned when no JSON object can be located in the text.
var ErrNoJSON = errors.New("no JSON object in model output")

// ParseScore extracts {"score": int, "reason": string} from model output.
// It tolerates markdown fences, leading/trailing prose, numeric strings and
// floats, and validates the 1-10 range.
func ParseScore(text string) (int, string, error) {
	obj, err := ExtractJSONObject(text)
	if err != nil {
		return 0, "", err
	}
	var raw struct {
		Score  json.RawMessage `json:"score"`
		Reason json.RawMessage `json:"reason"`
	}
	if err := json.Unmarshal([]byte(obj), &raw); err != nil {
		return 0, "", fmt.Errorf("invalid JSON: %w", err)
	}
	if len(raw.Score) == 0 {
		return 0, "", errors.New(`missing "score"`)
	}
	f, err := parseNumber(raw.Score)
	if err != nil {
		return 0, "", fmt.Errorf(`bad "score" %s: %w`, string(raw.Score), err)
	}
	if math.IsNaN(f) || f < 0.5 || f > 10.5 {
		return 0, "", fmt.Errorf(`"score" %v out of range 1-10`, f)
	}
	score := int(math.Round(f))
	if score < 1 {
		score = 1
	}
	if score > 10 {
		score = 10
	}
	reason := ""
	if len(raw.Reason) > 0 {
		var s string
		if err := json.Unmarshal(raw.Reason, &s); err == nil {
			reason = strings.TrimSpace(s)
		} else {
			reason = strings.TrimSpace(string(raw.Reason))
		}
	}
	return score, reason, nil
}

func parseNumber(rm json.RawMessage) (float64, error) {
	var f float64
	if err := json.Unmarshal(rm, &f); err == nil {
		return f, nil
	}
	var s string
	if err := json.Unmarshal(rm, &s); err == nil {
		s = strings.TrimSpace(s)
		// "8/10" -> 8
		if i := strings.IndexByte(s, '/'); i > 0 {
			s = s[:i]
		}
		return strconv.ParseFloat(strings.TrimSpace(s), 64)
	}
	return 0, errors.New("not a number")
}

// ExtractJSONObject returns the first balanced top-level {...} in text,
// skipping code fences and prose. String literals are respected so braces
// inside values do not confuse the scan.
func ExtractJSONObject(text string) (string, error) {
	s := strings.TrimSpace(text)
	// Strip ```json ... ``` fences if the whole thing is fenced.
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if nl := strings.IndexByte(s, '\n'); nl >= 0 {
			s = s[nl+1:]
		}
		if end := strings.LastIndex(s, "```"); end >= 0 {
			s = s[:end]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", ErrNoJSON
	}
	depth := 0
	inStr := false
	esc := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("%w: unbalanced braces", ErrNoJSON)
}
