package llm

import (
	"fmt"
	"strings"
)

// Prompt versions feed the cache key: bump when wording changes so stale
// answers are not reused.
const (
	DescribePromptVersion = "d1"
	ScorePromptVersion    = "s2"
)

func languageName(lang string) string {
	switch strings.ToLower(lang) {
	case "ru", "rus", "russian":
		return "Russian"
	case "", "en", "eng", "english":
		return "English"
	default:
		return lang
	}
}

func describeSystem(game, lang string) string {
	g := game
	if g == "" {
		g = "a video game"
	}
	return fmt.Sprintf(`You annotate raw gameplay footage of %s for an editor assembling a longplay (no commentary).
You receive several keyframes sampled in chronological order from one continuous segment.
Describe in one or two sentences what happens in the segment: where the player is, what they are doing (combat, dialogue, cutscene, menu or inventory, travel, building, trading, resting...), and any notable event.
Be concrete and factual. Use on-screen text when it helps (names, quest titles, dialogue gist). Do not speculate beyond what is visible and do not pad. If the frames show a loading screen, menu, or nothing meaningful, say so plainly.
Write in %s.`, g, languageName(lang))
}

func frameLabel(i, n int, t float64) string {
	return fmt.Sprintf("Frame %d/%d at %s", i+1, n, Timecode(t))
}

func describeUser(start, end float64) string {
	return fmt.Sprintf("Segment %s - %s (%.0f s). Describe it.", Timecode(start), Timecode(end), end-start)
}

func scoreSystem(game, lang string) string {
	g := game
	if g == "" {
		g = "the game"
	}
	return fmt.Sprintf(`You rate gameplay segments by how important they are to the story of a %s longplay, so an editor can keep the essential footage and drop filler.

Scale 1-10:
9-10  pivotal: boss fights, major cutscenes, key decisions, deaths or recruitment of important characters, story climaxes.
7-8   significant: meaningful combat, arriving in a new major location, important dialogue or quest turns, acquiring key gear.
4-6   normal progress: exploring new areas, minor fights, purposeful crafting or building, travel that reveals something new.
1-3   filler: menus, inventory shuffling, running through known areas, waiting, loading screens, idling, repeated attempts at the same thing.

Use the preceding segments as context: the first occurrence of something is worth more than its repetition; a shift to something new is worth more than continuation. Judge the current segment only.

Respond with JSON only, no prose: {"score": <integer 1-10>, "reason": "<one short sentence in %s>"}`, g, languageName(lang))
}

// ContextItem is a previously processed segment shown to the scorer.
type ContextItem struct {
	Index       int
	Start       float64
	Description string
	Score       *int
}

func scoreUser(cur ContextItem, duration float64, ctxItems []ContextItem) string {
	var b strings.Builder
	if len(ctxItems) == 0 {
		b.WriteString("Preceding segments: none (this is the first segment).\n\n")
	} else {
		b.WriteString("Preceding segments (oldest first):\n")
		for _, c := range ctxItems {
			fmt.Fprintf(&b, "#%d [%s]", c.Index, Timecode(c.Start))
			if c.Score != nil {
				fmt.Fprintf(&b, " (score %d)", *c.Score)
			}
			fmt.Fprintf(&b, ": %s\n", strings.TrimSpace(c.Description))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Current segment #%d [%s], %.0f s:\n%s", cur.Index, Timecode(cur.Start), duration, strings.TrimSpace(cur.Description))
	return b.String()
}

// Timecode formats seconds as H:MM:SS.
func Timecode(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s%3600)/60, s%60)
}
