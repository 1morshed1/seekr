package analysis

import (
	"strings"
	"unicode"
)

// UnicodeTokenizer splits text on any character that is not a letter or a
// digit. This is a simple but robust rule that handles most languages' word
// boundaries: whitespace, punctuation, brackets, and symbols all become
// separators.
//
// Example:
//
//	"C++ is great, really!" -> ["C", "is", "great", "really"]
//
// Note the "++" is discarded because '+' is neither a letter nor a digit. A
// production engine would keep some symbols for code search; we keep it simple.
type UnicodeTokenizer struct{}

// Tokenize walks the string rune by rune, accumulating runs of letters/digits
// into terms and emitting a Token (with an incrementing position) at each
// boundary.
func (t *UnicodeTokenizer) Tokenize(text string) []Token {
	var tokens []Token
	var sb strings.Builder
	position := 0

	flush := func() {
		if sb.Len() > 0 {
			tokens = append(tokens, Token{Term: sb.String(), Position: position})
			position++
			sb.Reset()
		}
	}

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
		} else {
			flush()
		}
	}
	flush() // emit the final token if the text didn't end on a separator

	return tokens
}
