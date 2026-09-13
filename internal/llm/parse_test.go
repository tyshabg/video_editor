package llm

import (
	"errors"
	"testing"
)

func TestParseScore(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		score  int
		reason string
		err    bool
	}{
		{"plain", `{"score": 7, "reason": "Boss fight begins."}`, 7, "Boss fight begins.", false},
		{"fenced", "```json\n{\"score\": 3, \"reason\": \"Inventory management.\"}\n```", 3, "Inventory management.", false},
		{"prose around", "Here is my rating:\n{\"score\": 9, \"reason\": \"Key cutscene.\"}\nHope this helps.", 9, "Key cutscene.", false},
		{"float score", `{"score": 6.0, "reason": "ok"}`, 6, "ok", false},
		{"float rounds", `{"score": 7.6, "reason": "ok"}`, 8, "ok", false},
		{"string score", `{"score": "8", "reason": "ok"}`, 8, "ok", false},
		{"string fraction", `{"score": "8/10", "reason": "ok"}`, 8, "ok", false},
		{"missing reason", `{"score": 2}`, 2, "", false},
		{"reason with braces", `{"score": 5, "reason": "Crafting {armour} in the {workshop}"}`, 5, "Crafting {armour} in the {workshop}", false},
		{"reason with escaped quote", `{"score": 5, "reason": "He said \"run\""}`, 5, `He said "run"`, false},
		{"multiple objects", `{"score": 4, "reason": "a"} {"score": 9, "reason": "b"}`, 4, "a", false},
		{"key order", `{"reason": "x", "score": 10}`, 10, "x", false},
		{"whitespace and newlines", "\n\n  {\n  \"score\" : 1 ,\n  \"reason\" : \"idle\"\n }\n", 1, "idle", false},
		{"zero out of range", `{"score": 0, "reason": "x"}`, 0, "", true},
		{"eleven out of range", `{"score": 11, "reason": "x"}`, 0, "", true},
		{"negative", `{"score": -3}`, 0, "", true},
		{"missing score", `{"reason": "x"}`, 0, "", true},
		{"non numeric", `{"score": "high", "reason": "x"}`, 0, "", true},
		{"no json", `I would rate this segment highly.`, 0, "", true},
		{"unbalanced", `{"score": 5, "reason": "oops`, 0, "", true},
		{"empty", ``, 0, "", true},
		{"nested object first", `{"meta": {"a": 1}, "score": 6, "reason": "r"}`, 6, "r", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			score, reason, err := ParseScore(c.in)
			if c.err {
				if err == nil {
					t.Fatalf("expected error, got score=%d reason=%q", score, reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if score != c.score || reason != c.reason {
				t.Fatalf("got (%d, %q), want (%d, %q)", score, reason, c.score, c.reason)
			}
		})
	}
}

func TestExtractJSONObjectNoJSON(t *testing.T) {
	_, err := ExtractJSONObject("nothing here")
	if !errors.Is(err, ErrNoJSON) {
		t.Fatalf("want ErrNoJSON, got %v", err)
	}
}

func TestCost(t *testing.T) {
	u := Usage{Input: 1_000_000, Output: 100_000}
	if got := Cost("claude-sonnet-4-6", u); got != 3+1.5 {
		t.Fatalf("sonnet 4.6 cost = %v", got)
	}
	if got := Cost("claude-sonnet-5", u); got != 2+1.0 {
		t.Fatalf("sonnet 5 cost = %v", got)
	}
	if _, ok := PriceFor("claude-unknown-9"); ok {
		t.Fatal("unknown model should report ok=false")
	}
	if ImageTokens(1280, 720) != 1229 {
		t.Fatalf("image tokens = %d", ImageTokens(1280, 720))
	}
}

func TestTimecode(t *testing.T) {
	if Timecode(3725.4) != "1:02:05" {
		t.Fatal(Timecode(3725.4))
	}
	if Timecode(0) != "0:00:00" {
		t.Fatal(Timecode(0))
	}
}
