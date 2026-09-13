package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DescribeCacheKey identifies a description request by everything that can
// change the answer: model, prompt version, game, language and the exact
// frame contents.
func DescribeCacheKey(model string, req DescribeRequest, frameHashes []string) string {
	h := sha256.New()
	h.Write([]byte("describe|" + model + "|" + DescribePromptVersion + "|" + req.Game + "|" + languageName(req.Lang) + "|"))
	h.Write([]byte(strings.Join(frameHashes, ",")))
	return hex.EncodeToString(h.Sum(nil))
}

// ScoreCacheKey identifies a scoring request by model, prompt version and the
// rendered user prompt (which embeds the description and the context).
func ScoreCacheKey(model string, req ScoreRequest) string {
	h := sha256.New()
	h.Write([]byte("score|" + model + "|" + ScorePromptVersion + "|" + req.Game + "|" + languageName(req.Lang) + "|"))
	h.Write([]byte(scoreUser(req.Current, req.Duration, req.Context)))
	return hex.EncodeToString(h.Sum(nil))
}
