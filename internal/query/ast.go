// Package query parses user query strings into an abstract syntax tree (AST) and
// executes that tree against an inverted index.
//
// Supported syntax:
//
//	golang                 term query (analyzed like indexed text)
//	golang kubernetes      implicit AND (both must match)
//	kafka OR rabbitmq      boolean OR (either may match)
//	golang AND kubernetes  explicit AND
//	golang NOT python      AND golang, exclude python
//	"distributed systems"  phrase query (terms must be adjacent)
//	title:kubernetes       field query (match only in the 'title' field)
//	deploy*                prefix query (deploy, deployment, deployed, ...)
//	(a OR b) AND c         grouping with parentheses
//
// The AST is a small set of node types, each implementing Query. Execution
// returns a map of docID -> accumulated BM25 score, which Search then sorts.
package query

import (
	"fmt"
	"strings"

	"github.com/1morshed1/seekr/internal/index"
)

// Query is one node of the parsed query tree. eval runs the node against the
// index and returns matching documents with their scores. It must be called with
// the index read lock held (Search wraps the whole tree in idx.WithRead).
type Query interface {
	eval(idx *index.InvertedIndex) map[string]float64
	String() string
}

// TermQuery matches documents containing a single word. The word is analyzed
// (lowercased, stopped, stemmed) exactly like indexed text, so "Running" matches
// a document that contained "runs". An empty Field searches all fields.
type TermQuery struct {
	Term  string
	Field string
}

func (q *TermQuery) String() string {
	if q.Field != "" {
		return fmt.Sprintf("%s:%s", q.Field, q.Term)
	}
	return q.Term
}

// PhraseQuery matches documents where the phrase's words appear in order and
// adjacent (respecting the same stop-word gaps the analyzer produced). An empty
// Field searches all fields.
type PhraseQuery struct {
	Phrase string
	Field  string
}

func (q *PhraseQuery) String() string {
	if q.Field != "" {
		return fmt.Sprintf("%s:%q", q.Field, q.Phrase)
	}
	return fmt.Sprintf("%q", q.Phrase)
}

// PrefixQuery matches documents containing any term that starts with Prefix. The
// prefix is lowercased but NOT stemmed (stemming a partial word is meaningless).
type PrefixQuery struct {
	Prefix string
	Field  string
}

func (q *PrefixQuery) String() string {
	if q.Field != "" {
		return fmt.Sprintf("%s:%s*", q.Field, q.Prefix)
	}
	return q.Prefix + "*"
}

// BoolQuery combines sub-queries with boolean logic, following Lucene semantics:
//
//	Must    – every clause must match (AND). Their scores add up.
//	Should  – optional clauses (OR). If there are no Must clauses, at least one
//	          Should must match and Should defines the candidate set. If there
//	          are Must clauses, Should only *boosts* the score of docs already
//	          selected by Must.
//	MustNot – excluded clauses (NOT). Any doc matching these is removed.
type BoolQuery struct {
	Must    []Query
	Should  []Query
	MustNot []Query
}

func (q *BoolQuery) String() string {
	parts := []string{}
	for _, m := range q.Must {
		parts = append(parts, "+"+m.String())
	}
	for _, s := range q.Should {
		parts = append(parts, s.String())
	}
	for _, n := range q.MustNot {
		parts = append(parts, "-"+n.String())
	}
	return "(" + strings.Join(parts, " ") + ")"
}
