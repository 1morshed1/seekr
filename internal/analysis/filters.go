package analysis

import "strings"

// LowercaseFilter normalizes case so that "Golang", "golang", and "GOLANG" all
// collapse to the same term. Without this, a search for "kafka" would miss a
// document that wrote "Kafka".
type LowercaseFilter struct{}

// Apply lowercases every term in place.
func (f *LowercaseFilter) Apply(tokens []Token) []Token {
	for i := range tokens {
		tokens[i].Term = strings.ToLower(tokens[i].Term)
	}
	return tokens
}

// StopWordFilter removes extremely common words ("the", "a", "is", ...) that
// appear in almost every document and therefore carry little discriminating
// power. Dropping them shrinks the index and speeds up queries.
//
// Trade-off: removing stop words breaks phrases that depend on them, e.g.
// "to be or not to be" would become empty. Real engines make this configurable;
// we keep a small default list.
type StopWordFilter struct {
	stopWords map[string]struct{}
}

// NewStopWordFilter builds a filter from a set of stop words.
func NewStopWordFilter(words map[string]struct{}) *StopWordFilter {
	return &StopWordFilter{stopWords: words}
}

// Apply drops any token whose term is a stop word.
//
// Important subtlety: we do NOT renumber positions after dropping. If the text
// is "the running fox" the positions are the:0, running:1, fox:2. After removing
// "the" we keep running:1 and fox:2 (a gap at 0). Preserving the original
// positions keeps phrase matching correct relative to the source text.
func (f *StopWordFilter) Apply(tokens []Token) []Token {
	out := tokens[:0] // reuse the backing array to avoid an allocation
	for _, tok := range tokens {
		if _, isStop := f.stopWords[tok.Term]; !isStop {
			out = append(out, tok)
		}
	}
	return out
}

// DefaultStopWords returns a small, commonly used English stop-word set. It is
// intentionally modest; an over-aggressive list hurts recall.
func DefaultStopWords() map[string]struct{} {
	list := []string{
		"a", "an", "and", "are", "as", "at", "be", "but", "by",
		"for", "if", "in", "into", "is", "it", "no", "not", "of",
		"on", "or", "such", "that", "the", "their", "then", "there",
		"these", "they", "this", "to", "was", "will", "with",
	}
	set := make(map[string]struct{}, len(list))
	for _, w := range list {
		set[w] = struct{}{}
	}
	return set
}
