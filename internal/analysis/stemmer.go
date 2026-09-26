package analysis

// PorterStemmerFilter reduces words to their morphological root ("stem") so that
// related words match each other. It applies the classic Porter stemming
// algorithm (M.F. Porter, 1980), the most widely used English stemmer.
//
// Examples:
//
//	"running"   -> "run"
//	"connection"-> "connect"
//	"happily"   -> "happili"   (stems are not always real words — that's fine,
//	                            they only need to be *consistent*)
//
// Why stemming matters: a user searching for "compute" almost certainly wants
// documents mentioning "computing", "computed", and "computers" too. Stemming
// both the indexed text and the query maps all of those to a shared term.
type PorterStemmerFilter struct{}

// Apply stems every token in place.
func (f *PorterStemmerFilter) Apply(tokens []Token) []Token {
	for i := range tokens {
		tokens[i].Term = Stem(tokens[i].Term)
	}
	return tokens
}

// Stem returns the Porter stem of a single lowercase word. Words of length <= 2
// are returned unchanged (stemming tiny words is not worth it and often wrong).
func Stem(word string) string {
	if len(word) <= 2 {
		return word
	}
	s := &stemmer{b: []byte(word)}
	s.step1a()
	s.step1b()
	s.step1c()
	s.step2()
	s.step3()
	s.step4()
	s.step5a()
	s.step5b()
	return string(s.b)
}

// stemmer holds the working buffer for the algorithm. The classic algorithm is
// described in terms of indices into the word; we keep a byte slice and mutate
// it step by step.
type stemmer struct {
	b []byte
}

// isConsonant reports whether the letter at index i is a consonant. 'y' is a
// consonant except when preceded by a consonant (so "y" in "toy" is a vowel but
// "y" in "syzygy" alternates). This mirrors the Porter definition exactly.
func (s *stemmer) isConsonant(i int) bool {
	switch s.b[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		if i == 0 {
			return true
		}
		return !s.isConsonant(i - 1)
	default:
		return true
	}
}

// measure counts the number of vowel-consonant sequences (VC) between the start
// of the word and position j (exclusive), the value Porter calls "m". Intuitively
// m is a rough syllable count and gates whether a suffix may be removed.
//
//	m=0: "tr", "ee", "tree", "y", "by"
//	m=1: "trouble", "oats", "trees", "ivy"
//	m=2: "troubles", "private", "oaten"
func (s *stemmer) measure(j int) int {
	n := 0
	i := 0
	// skip leading consonants
	for i < j && s.isConsonant(i) {
		i++
	}
	for i < j {
		// skip vowels
		for i < j && !s.isConsonant(i) {
			i++
		}
		if i >= j {
			break
		}
		n++ // we just completed a VC transition
		// skip consonants
		for i < j && s.isConsonant(i) {
			i++
		}
	}
	return n
}

// hasVowel reports whether the first j letters contain a vowel.
func (s *stemmer) hasVowel(j int) bool {
	for i := 0; i < j; i++ {
		if !s.isConsonant(i) {
			return true
		}
	}
	return false
}

// doubleConsonant reports whether the two letters ending at index i are the same
// consonant (e.g. the "tt" in "hitt").
func (s *stemmer) doubleConsonant(i int) bool {
	if i < 1 {
		return false
	}
	if s.b[i] != s.b[i-1] {
		return false
	}
	return s.isConsonant(i)
}

// cvc reports whether the three letters ending at index i are
// consonant-vowel-consonant AND the final consonant is not w, x, or y. This
// pattern ("-cvc") identifies short words like "hop" where a trailing 'e' should
// be restored ("hop" -> "hope").
func (s *stemmer) cvc(i int) bool {
	if i < 2 {
		return false
	}
	if !s.isConsonant(i) || s.isConsonant(i-1) || !s.isConsonant(i-2) {
		return false
	}
	switch s.b[i] {
	case 'w', 'x', 'y':
		return false
	}
	return true
}

// ends reports whether the buffer ends with suffix. If so it records the length
// of the stem (buffer minus suffix) in the returned index for convenience.
func (s *stemmer) ends(suffix string) (bool, int) {
	sl := len(suffix)
	if sl > len(s.b) {
		return false, 0
	}
	stemLen := len(s.b) - sl
	if string(s.b[stemLen:]) == suffix {
		return true, stemLen
	}
	return false, 0
}

// setTo replaces the current suffix region (starting at stemLen) with s2.
func (s *stemmer) setTo(stemLen int, s2 string) {
	s.b = append(s.b[:stemLen], []byte(s2)...)
}

// step1a strips plural / possessive suffixes.
//
//	caresses -> caress   ponies -> poni   cats -> cat
func (s *stemmer) step1a() {
	if ok, st := s.ends("sses"); ok {
		s.setTo(st, "ss")
	} else if ok, st := s.ends("ies"); ok {
		s.setTo(st, "i")
	} else if ok, _ := s.ends("ss"); ok {
		// leave "ss" alone
	} else if ok, st := s.ends("s"); ok {
		s.setTo(st, "")
	}
}

// step1b removes "-ed" and "-ing" when the remaining stem contains a vowel, then
// tidies up the result (e.g. restoring a dropped 'e').
//
//	agreed -> agree   plastered -> plaster   motoring -> motor
func (s *stemmer) step1b() {
	cont := false
	if ok, st := s.ends("eed"); ok {
		if s.measure(st) > 0 {
			s.setTo(st, "ee")
		}
	} else if ok, st := s.ends("ed"); ok && s.hasVowel(st) {
		s.setTo(st, "")
		cont = true
	} else if ok, st := s.ends("ing"); ok && s.hasVowel(st) {
		s.setTo(st, "")
		cont = true
	}

	if !cont {
		return
	}
	// cleanup after removing -ed / -ing
	if ok, st := s.ends("at"); ok {
		s.setTo(st, "ate")
	} else if ok, st := s.ends("bl"); ok {
		s.setTo(st, "ble")
	} else if ok, st := s.ends("iz"); ok {
		s.setTo(st, "ize")
	} else if last := len(s.b) - 1; s.doubleConsonant(last) {
		switch s.b[last] {
		case 'l', 's', 'z':
			// keep the double consonant
		default:
			s.b = s.b[:last] // drop the doubled consonant
		}
	} else if s.measure(len(s.b)) == 1 && s.cvc(len(s.b)-1) {
		s.b = append(s.b, 'e')
	}
}

// step1c turns a trailing 'y' into 'i' when the stem contains a vowel, so that
// "happy" and "happiness" share the stem "happi".
func (s *stemmer) step1c() {
	if ok, st := s.ends("y"); ok && s.hasVowel(st) {
		s.b[st] = 'i'
	}
}

// step2 maps double suffixes to single ones (e.g. -ational -> -ate) when the
// stem is long enough (measure > 0).
func (s *stemmer) step2() {
	pairs := []struct{ suf, rep string }{
		{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"},
		{"anci", "ance"}, {"izer", "ize"}, {"bli", "ble"},
		{"alli", "al"}, {"entli", "ent"}, {"eli", "e"},
		{"ousli", "ous"}, {"ization", "ize"}, {"ation", "ate"},
		{"ator", "ate"}, {"alism", "al"}, {"iveness", "ive"},
		{"fulness", "ful"}, {"ousness", "ous"}, {"aliti", "al"},
		{"iviti", "ive"}, {"biliti", "ble"}, {"logi", "log"},
	}
	for _, p := range pairs {
		if ok, st := s.ends(p.suf); ok {
			if s.measure(st) > 0 {
				s.setTo(st, p.rep)
			}
			return
		}
	}
}

// step3 removes / shortens further suffixes (-icate, -ful, -ness, ...).
func (s *stemmer) step3() {
	pairs := []struct{ suf, rep string }{
		{"icate", "ic"}, {"ative", ""}, {"alize", "al"},
		{"iciti", "ic"}, {"ical", "ic"}, {"ful", ""}, {"ness", ""},
	}
	for _, p := range pairs {
		if ok, st := s.ends(p.suf); ok {
			if s.measure(st) > 0 {
				s.setTo(st, p.rep)
			}
			return
		}
	}
}

// step4 removes residual suffixes (-al, -ance, -er, -ic, -ion, ...) but only when
// the stem is fairly long (measure > 1), to avoid over-stemming short words.
func (s *stemmer) step4() {
	suffixes := []string{
		"al", "ance", "ence", "er", "ic", "able", "ible", "ant",
		"ement", "ment", "ent", "ou", "ism", "ate", "iti", "ous",
		"ive", "ize",
	}
	for _, suf := range suffixes {
		if ok, st := s.ends(suf); ok {
			if s.measure(st) > 1 {
				s.setTo(st, "")
			}
			return
		}
	}
	// special case: -ion only removed when preceded by s or t
	if ok, st := s.ends("ion"); ok && st > 0 {
		if s.measure(st) > 1 && (s.b[st-1] == 's' || s.b[st-1] == 't') {
			s.setTo(st, "")
		}
	}
}

// step5a removes a trailing 'e' when the stem is long enough.
//
//	probate -> probat   rate -> rate (kept, too short)
func (s *stemmer) step5a() {
	if ok, st := s.ends("e"); ok {
		m := s.measure(st)
		if m > 1 || (m == 1 && !s.cvc(st-1)) {
			s.setTo(st, "")
		}
	}
}

// step5b collapses a double 'l' at the end of a long word ("controll" ->
// "control").
func (s *stemmer) step5b() {
	last := len(s.b) - 1
	if last >= 1 && s.b[last] == 'l' && s.doubleConsonant(last) && s.measure(last) > 1 {
		s.b = s.b[:last]
	}
}
