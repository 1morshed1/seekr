// Package analysis turns raw text into a stream of normalized tokens that the
// inverted index can store and that queries can match against.
//
// The pipeline mirrors what production search engines (Elasticsearch, Lucene,
// Solr) do:
//
//	raw text  -->  Tokenizer  -->  TokenFilter chain  -->  []Token
//
//	"The Running Foxes"
//	   |  tokenize (split on non-letters/digits)
//	   v
//	["The", "Running", "Foxes"]
//	   |  lowercase filter
//	   v
//	["the", "running", "foxes"]
//	   |  stop-word filter ("the" is a stop word)
//	   v
//	["running", "foxes"]
//	   |  Porter stemmer (reduce words to their root)
//	   v
//	["run", "fox"]
//
// The same Analyzer must be used at index time AND at query time. If we index
// "run" (stemmed) but search for "running" (unstemmed) we would never find the
// document. Consistency between the two paths is the whole point.
package analysis

// Token is a single unit of text after analysis, together with the metadata we
// need later for ranking and phrase matching.
type Token struct {
	// Term is the normalized text (lowercased, stemmed, etc.).
	Term string
	// Position is the ordinal position of this token in its field, starting at
	// 0. Positions let us answer phrase queries ("distributed systems" means
	// "distributed" at position p and "systems" at position p+1).
	Position int
	// Field records which document field the token came from (e.g. "title" vs
	// "body"). Empty until the caller assigns it.
	Field string
}

// Tokenizer splits raw text into raw (un-normalized) tokens.
type Tokenizer interface {
	Tokenize(text string) []Token
}

// TokenFilter transforms a token stream: lowercasing, removing stop words,
// stemming, etc. Filters may drop tokens (returning fewer than they received)
// but must never reorder them, because Position ordering must be preserved.
type TokenFilter interface {
	Apply(tokens []Token) []Token
}

// Analyzer is a tokenizer followed by an ordered chain of filters.
type Analyzer struct {
	Tokenizer Tokenizer
	Filters   []TokenFilter
}

// Analyze runs text through the tokenizer and every filter in order.
func (a *Analyzer) Analyze(text string) []Token {
	tokens := a.Tokenizer.Tokenize(text)
	for _, f := range a.Filters {
		tokens = f.Apply(tokens)
	}
	return tokens
}

// NewStandardAnalyzer builds the default analysis pipeline used across the
// engine: unicode tokenizer -> lowercase -> stop words -> Porter stemmer.
//
// This is deliberately the single source of truth for "how do we normalize
// text". The shard nodes and the query parser both call it so index-time and
// query-time analysis can never drift apart.
func NewStandardAnalyzer() *Analyzer {
	return &Analyzer{
		Tokenizer: &UnicodeTokenizer{},
		Filters: []TokenFilter{
			&LowercaseFilter{},
			NewStopWordFilter(DefaultStopWords()),
			&PorterStemmerFilter{},
		},
	}
}

// AnalyzeToTerms is a convenience helper that returns just the term strings.
// Handy for the query parser, which cares about the words but not positions.
func (a *Analyzer) AnalyzeToTerms(text string) []string {
	tokens := a.Analyze(text)
	terms := make([]string, len(tokens))
	for i, t := range tokens {
		terms[i] = t.Term
	}
	return terms
}
