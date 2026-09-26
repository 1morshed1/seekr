package analysis

import (
	"reflect"
	"testing"
)

func TestUnicodeTokenizer(t *testing.T) {
	tok := &UnicodeTokenizer{}
	got := tok.Tokenize("C++ is great, really!")
	want := []Token{
		{Term: "C", Position: 0},
		{Term: "is", Position: 1},
		{Term: "great", Position: 2},
		{Term: "really", Position: 3},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Tokenize() = %+v, want %+v", got, want)
	}
}

func TestLowercaseFilter(t *testing.T) {
	f := &LowercaseFilter{}
	got := f.Apply([]Token{{Term: "GoLang", Position: 0}})
	if got[0].Term != "golang" {
		t.Fatalf("lowercase = %q, want %q", got[0].Term, "golang")
	}
}

func TestStopWordFilterPreservesPositions(t *testing.T) {
	f := NewStopWordFilter(DefaultStopWords())
	// "the running fox" -> the is a stop word; running/fox keep positions 1,2.
	in := []Token{
		{Term: "the", Position: 0},
		{Term: "running", Position: 1},
		{Term: "fox", Position: 2},
	}
	got := f.Apply(in)
	want := []Token{
		{Term: "running", Position: 1},
		{Term: "fox", Position: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stopword filter = %+v, want %+v", got, want)
	}
}

// TestPorterStemmer checks a spread of cases from Porter's own published sample
// vocabulary so we know the algorithm behaves like the reference.
func TestPorterStemmer(t *testing.T) {
	cases := map[string]string{
		"caresses":       "caress",
		"ponies":         "poni",
		"ties":           "ti",
		"cats":           "cat",
		"agreed":         "agre",
		"plastered":      "plaster",
		"motoring":       "motor",
		"happy":          "happi",
		"relational":     "relat",
		"conditional":    "condit",
		"rational":       "ration",
		"vietnamization": "vietnam",
		"predication":    "predic",
		"operator":       "oper",
		"feudalism":      "feudal",
		"decisiveness":   "decis",
		"hopefulness":    "hope",
		"callousness":    "callous",
		"formaliti":      "formal",
		"sensitiviti":    "sensit",
		"triplicate":     "triplic",
		"formative":      "form",
		"electriciti":    "electr",
		"electrical":     "electr",
		"hopeful":        "hope",
		"goodness":       "good",
		"revival":        "reviv",
		"allowance":      "allow",
		"inference":      "infer",
		"airliner":       "airlin",
		"gyroscopic":     "gyroscop",
		"adjustable":     "adjust",
		"defensible":     "defens",
		"irritant":       "irrit",
		"replacement":    "replac",
		"adjustment":     "adjust",
		"dependent":      "depend",
		"adoption":       "adopt",
		"communism":      "commun",
		"activate":       "activ",
		"angulariti":     "angular",
		"homologous":     "homolog",
		"effective":      "effect",
		"bowdlerize":     "bowdler",
		"probate":        "probat",
		"rate":           "rate",
		"controll":       "control",
		"roll":           "roll",
	}
	for in, want := range cases {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStandardAnalyzerEndToEnd(t *testing.T) {
	a := NewStandardAnalyzer()
	// "The Running Foxes are Fast" -> stop "the","are" removed, rest stemmed.
	terms := a.AnalyzeToTerms("The Running Foxes are Fast")
	want := []string{"run", "fox", "fast"}
	if !reflect.DeepEqual(terms, want) {
		t.Fatalf("AnalyzeToTerms() = %v, want %v", terms, want)
	}
}

// TestIndexQueryConsistency is the most important guarantee: whatever we do to a
// word at index time we must do identically at query time.
func TestIndexQueryConsistency(t *testing.T) {
	a := NewStandardAnalyzer()
	indexed := a.AnalyzeToTerms("distributed computing systems")
	queried := a.AnalyzeToTerms("Distributed Computer System")
	// "computing" and "computer" both stem to "comput"; "systems"/"system" to
	// "system". So the analyzed forms should overlap.
	if indexed[0] != queried[0] {
		t.Errorf("distributed mismatch: %q vs %q", indexed[0], queried[0])
	}
	if indexed[1] != queried[1] {
		t.Errorf("comput mismatch: %q vs %q", indexed[1], queried[1])
	}
}
