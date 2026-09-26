package query

// A hand-written recursive descent parser. Each grammar rule is one method that
// consumes tokens and returns a Query node. The grammar, by precedence (lowest to
// highest):
//
//	orExpr   := andExpr ( OR andExpr )*
//	andExpr  := unary ( AND? unary )*        // adjacency = implicit AND
//	unary    := NOT? primary
//	primary  := '(' orExpr ')'
//	          | PHRASE
//	          | WORD ':' ( PHRASE | WORD '*'? )   // field query
//	          | WORD '*'?                          // term or prefix query
//
// OR binds loosest, then AND, then NOT, then the atoms — the conventional
// precedence, so "a OR b AND c" parses as "a OR (b AND c)".

// Parse turns a query string into a Query tree. An empty or whitespace-only query
// returns (nil, nil): no error, no results.
func Parse(input string) (Query, error) {
	p := &parser{toks: lex(input)}
	q := p.parseOr()
	return q, nil
}

type parser struct {
	toks []token
	pos  int
}

func (p *parser) cur() token { return p.toks[p.pos] }

// advance moves to the next token but never past the terminal tEOF, so malformed
// input (stray operators, unbalanced parens) can never index out of range.
func (p *parser) advance() {
	if p.toks[p.pos].kind != tEOF {
		p.pos++
	}
}
func (p *parser) is(k tokenKind) bool { return p.cur().kind == k }

// parseOr collects OR-separated sub-expressions into a Should (OR) group.
func (p *parser) parseOr() Query {
	first := p.parseAnd()
	if !p.is(tOr) {
		return first // no OR at this level; pass through
	}
	should := []Query{}
	if first != nil {
		should = append(should, first)
	}
	for p.is(tOr) {
		p.advance()
		if next := p.parseAnd(); next != nil {
			should = append(should, next)
		}
	}
	if len(should) == 0 {
		return nil
	}
	if len(should) == 1 {
		return should[0]
	}
	return &BoolQuery{Should: should}
}

// parseAnd collects a run of adjacent clauses (implicit AND), routing NOT clauses
// into MustNot. It stops at OR, a closing paren, or EOF.
func (p *parser) parseAnd() Query {
	var must, mustNot []Query
	for {
		switch p.cur().kind {
		case tOr, tRParen, tEOF:
			return finishAnd(must, mustNot)
		case tAnd:
			p.advance() // explicit AND is just a separator between clauses
			continue
		}

		neg := false
		if p.is(tNot) {
			neg = true
			p.advance()
		}
		clause := p.parsePrimary()
		if clause == nil {
			// Could not form a clause (e.g. a stray operator). Avoid an infinite
			// loop by bailing out of this AND group.
			return finishAnd(must, mustNot)
		}
		if neg {
			mustNot = append(mustNot, clause)
		} else {
			must = append(must, clause)
		}
	}
}

// finishAnd collapses a single positive clause with no negatives down to that
// clause; otherwise it builds a BoolQuery.
func finishAnd(must, mustNot []Query) Query {
	if len(must) == 0 && len(mustNot) == 0 {
		return nil
	}
	if len(must) == 1 && len(mustNot) == 0 {
		return must[0]
	}
	return &BoolQuery{Must: must, MustNot: mustNot}
}

// parsePrimary parses an atom: a group, a phrase, a field query, a prefix query,
// or a term query.
func (p *parser) parsePrimary() Query {
	switch p.cur().kind {
	case tLParen:
		p.advance()
		inner := p.parseOr()
		if p.is(tRParen) {
			p.advance()
		}
		return inner

	case tPhrase:
		phrase := p.cur().val
		p.advance()
		return &PhraseQuery{Phrase: phrase}

	case tWord:
		word := p.cur().val
		p.advance()
		if p.is(tColon) {
			p.advance()
			return p.parseFieldValue(word)
		}
		if p.is(tStar) {
			p.advance()
			return &PrefixQuery{Prefix: word}
		}
		return &TermQuery{Term: word}

	default:
		// Unexpected token (e.g. a leading ':' or '*'); skip it so parsing can
		// make progress.
		p.advance()
		return nil
	}
}

// parseFieldValue parses the right-hand side of a "field:" query.
func (p *parser) parseFieldValue(field string) Query {
	switch p.cur().kind {
	case tPhrase:
		phrase := p.cur().val
		p.advance()
		return &PhraseQuery{Phrase: phrase, Field: field}
	case tWord:
		word := p.cur().val
		p.advance()
		if p.is(tStar) {
			p.advance()
			return &PrefixQuery{Prefix: word, Field: field}
		}
		return &TermQuery{Term: word, Field: field}
	default:
		// "field:" with nothing valid after it — treat the field name as a plain
		// term so the query still does something sensible.
		return &TermQuery{Term: field}
	}
}
