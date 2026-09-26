package query

import "strings"

// The lexer turns a raw query string into a flat slice of tokens that the parser
// consumes. Keeping lexing separate from parsing is the standard two-stage design:
// the lexer worries about characters, the parser worries about grammar.

type tokenKind int

const (
	tWord   tokenKind = iota // an ordinary word: golang, kubernetes, title
	tPhrase                  // a quoted phrase (the value excludes the quotes)
	tAnd                     // AND
	tOr                      // OR
	tNot                     // NOT
	tColon                   // :   (field separator, as in title:foo)
	tLParen                  // (
	tRParen                  // )
	tStar                    // *   (prefix marker, as in deploy*)
	tEOF                     // end of input
)

type token struct {
	kind tokenKind
	val  string
}

// lex scans the input into tokens. AND/OR/NOT are recognized case-insensitively as
// operators; every other word becomes a tWord. Unterminated quotes are treated as
// running to end of input (lenient, so a stray quote doesn't error the whole
// query).
func lex(input string) []token {
	var toks []token
	runes := []rune(input)
	i := 0
	n := len(runes)

	isSpecial := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '"', '(', ')', ':', '*':
			return true
		}
		return false
	}

	for i < n {
		r := runes[i]
		switch {
		case r == ' ' || r == '\t' || r == '\n':
			i++
		case r == '"':
			i++ // skip opening quote
			start := i
			for i < n && runes[i] != '"' {
				i++
			}
			toks = append(toks, token{kind: tPhrase, val: string(runes[start:i])})
			if i < n {
				i++ // skip closing quote
			}
		case r == '(':
			toks = append(toks, token{kind: tLParen})
			i++
		case r == ')':
			toks = append(toks, token{kind: tRParen})
			i++
		case r == ':':
			toks = append(toks, token{kind: tColon})
			i++
		case r == '*':
			toks = append(toks, token{kind: tStar})
			i++
		default:
			start := i
			for i < n && !isSpecial(runes[i]) {
				i++
			}
			word := string(runes[start:i])
			switch strings.ToUpper(word) {
			case "AND":
				toks = append(toks, token{kind: tAnd})
			case "OR":
				toks = append(toks, token{kind: tOr})
			case "NOT":
				toks = append(toks, token{kind: tNot})
			default:
				toks = append(toks, token{kind: tWord, val: word})
			}
		}
	}

	toks = append(toks, token{kind: tEOF})
	return toks
}
