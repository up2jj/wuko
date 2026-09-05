package expression

import (
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestTextHelpers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(t *testing.T) string
		want string
	}{
		{name: "reverse text by grapheme and line", run: func(*testing.T) string {
			return ReverseText("A👩‍❤️‍💋‍👩B\ncafe\u0301\n")
		}, want: "B👩‍❤️‍💋‍👩A\ne\u0301fac\n"},
		{name: "reverse words by line", run: func(*testing.T) string {
			return ReverseWords("one  two\nthree four\n")
		}, want: "two one\nfour three\n"},
		{name: "repeat with separator", run: func(t *testing.T) string {
			got, err := Repeat("ha", 3, "-")
			if err != nil {
				t.Fatal(err)
			}
			return got
		}, want: "ha-ha-ha"},
		{name: "truncate by grapheme", run: func(t *testing.T) string {
			got, err := Truncate("A👩‍❤️‍💋‍👩BC", 3, "…")
			if err != nil {
				t.Fatal(err)
			}
			return got
		}, want: "A👩‍❤️‍💋‍👩…"},
		{name: "squeeze each line", run: func(*testing.T) string {
			return Squeeze("  too   many \n spaces\there  \n")
		}, want: "too many\nspaces here\n"},
		{name: "remove whitespace", run: func(*testing.T) string {
			return RemoveWhitespace(" a\u00a0b\nc ")
		}, want: "abc"},
		{name: "remove punctuation", run: func(*testing.T) string {
			return RemovePunctuation("Hi, κόσμε! #2")
		}, want: "Hi κόσμε 2"},
		{name: "remove accents", run: func(*testing.T) string {
			return RemoveAccents("crème brûlée, nai\u0308ve")
		}, want: "creme brulee, naive"},
		{name: "remove non ASCII", run: func(*testing.T) string {
			return RemoveNonASCII("café 東京")
		}, want: "caf "},
		{name: "strip HTML and decode entities", run: func(*testing.T) string {
			return StripHTML("<p>Hi <b>there</b> &amp; bye</p>")
		}, want: "Hi there & bye"},
		{name: "strip HTML keeps literal comparisons", run: func(*testing.T) string {
			return StripHTML("5 > 3 and 2 < 4 <b>ok</b>")
		}, want: "5 > 3 and 2 < 4 ok"},
		{name: "strip HTML skips quoted attribute values", run: func(*testing.T) string {
			return StripHTML(`<a title="a>b" href='c>d'>hi</a>`)
		}, want: "hi"},
		{name: "tabs to spaces", run: func(t *testing.T) string {
			got, err := TabsToSpaces("a\tb", 2)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}, want: "a  b"},
		{name: "spaces to tabs", run: func(t *testing.T) string {
			got, err := SpacesToTabs("a    b", 2)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}, want: "a\t\tb"},
		{name: "newlines to spaces", run: func(*testing.T) string {
			return NewlinesToSpaces("a\r\nb\n")
		}, want: "a b"},
		{name: "spaces to newlines", run: func(*testing.T) string {
			return SpacesToNewlines(" a\tb \n c ")
		}, want: "a\nb\nc"},
		{name: "rotate left", run: func(*testing.T) string {
			return Rotate("A👩‍❤️‍💋‍👩BC\n", 1)
		}, want: "👩‍❤️‍💋‍👩BCA\n"},
		{name: "rotate right", run: func(*testing.T) string {
			return Rotate("abcd", -1)
		}, want: "dabc"},
		{name: "quote lines", run: func(t *testing.T) string {
			got, err := Quote("one\r\ntwo\n", "'")
			if err != nil {
				t.Fatal(err)
			}
			return got
		}, want: "'one'\r\n'two'\n"},
		{name: "escape regex", run: func(*testing.T) string {
			return EscapeRegex("a.b[0]")
		}, want: `a\.b\[0\]`},
		{name: "normalize Unicode", run: func(t *testing.T) string {
			got, err := NormalizeUnicode("é", "NFD")
			if err != nil {
				t.Fatal(err)
			}
			if !norm.NFD.IsNormalString(got) {
				t.Fatalf("value %q is not NFD", got)
			}
			return got
		}, want: "e\u0301"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.run(t); got != tt.want {
				t.Fatalf("value = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTextHelperErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "negative repeat", run: func() error { _, err := Repeat("x", -1, ""); return err }, want: "must not be negative"},
		{name: "repeat budget", run: func() error { _, err := Repeat("x", maxTextRepeatCount+1, ""); return err }, want: "memory budget"},
		{name: "repeat result budget", run: func() error {
			_, err := Repeat(strings.Repeat("x", 1024), maxTextRepeatCount, "")
			return err
		}, want: "exceeds memory budget"},
		{name: "tabs to spaces result budget", run: func() error {
			_, err := TabsToSpaces(strings.Repeat("\t", 1024), maxTextRepeatCount)
			return err
		}, want: "exceeds memory budget"},
		{name: "negative truncate", run: func() error { _, err := Truncate("x", -1, ""); return err }, want: "must not be negative"},
		{name: "truncate suffix", run: func() error { _, err := Truncate("value", 1, "..."); return err }, want: "suffix"},
		{name: "tabs width", run: func() error { _, err := TabsToSpaces("x", 0); return err }, want: "at least 1"},
		{name: "spaces width", run: func() error { _, err := SpacesToTabs("x", 0); return err }, want: "at least 1"},
		{name: "empty quote", run: func() error { _, err := Quote("x", ""); return err }, want: "must not be empty"},
		{name: "normalization form", run: func() error { _, err := NormalizeUnicode("x", "other"); return err }, want: "unknown Unicode normalization form"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestTextHelpersTemplateAndExprParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		template   string
		expression string
		want       string
	}{
		{name: "reverse text", template: `{{ "A👩‍❤️‍💋‍👩B" | reverseText }}`, expression: `reverseText("A👩‍❤️‍💋‍👩B")`, want: "B👩‍❤️‍💋‍👩A"},
		{name: "reverse words", template: `{{ "one two" | reverseWords }}`, expression: `reverseWords("one two")`, want: "two one"},
		{name: "repeat default", template: `{{ "ha" | repeat }}`, expression: `repeat("ha")`, want: "haha"},
		{name: "repeat options", template: `{{ "ha" | repeat 3 "-" }}`, expression: `repeat("ha", 3, "-")`, want: "ha-ha-ha"},
		{name: "truncate default", template: `{{ "12345678901234567890123456789012345678901234567890123456789012345678901234567890x" | truncate }}`, expression: `truncate("12345678901234567890123456789012345678901234567890123456789012345678901234567890x")`, want: "12345678901234567890123456789012345678901234567890123456789012345678901234567890"},
		{name: "truncate", template: `{{ "abcdefgh" | truncate 5 "..." }}`, expression: `truncate("abcdefgh", 5, "...")`, want: "ab..."},
		{name: "squeeze", template: `{{ " too   many " | squeeze }}`, expression: `squeeze(" too   many ")`, want: "too many"},
		{name: "remove whitespace", template: `{{ " a b " | removeWhitespace }}`, expression: `removeWhitespace(" a b ")`, want: "ab"},
		{name: "remove punctuation", template: `{{ "hi, there!" | removePunctuation }}`, expression: `removePunctuation("hi, there!")`, want: "hi there"},
		{name: "remove accents", template: `{{ "crème" | removeAccents }}`, expression: `removeAccents("crème")`, want: "creme"},
		{name: "remove non ASCII", template: `{{ "café" | removeNonASCII }}`, expression: `removeNonASCII("café")`, want: "caf"},
		{name: "strip HTML", template: `{{ "<b>hi</b> &amp; bye" | stripHTML }}`, expression: `stripHTML("<b>hi</b> &amp; bye")`, want: "hi & bye"},
		{name: "tabs to spaces", template: "{{ \"a\\tb\" | tabsToSpaces 2 }}", expression: `tabsToSpaces("a\tb", 2)`, want: "a  b"},
		{name: "tabs to spaces default", template: "{{ \"a\\tb\" | tabsToSpaces }}", expression: `tabsToSpaces("a\tb")`, want: "a    b"},
		{name: "spaces to tabs", template: `{{ "a  b" | spacesToTabs 2 }}`, expression: `spacesToTabs("a  b", 2)`, want: "a\tb"},
		{name: "spaces to tabs default", template: `{{ "a    b" | spacesToTabs }}`, expression: `spacesToTabs("a    b")`, want: "a\tb"},
		{name: "newlines to spaces", template: "{{ \"a\\nb\" | newlinesToSpaces }}", expression: `newlinesToSpaces("a\nb")`, want: "a b"},
		{name: "spaces to newlines", template: `{{ "a b" | spacesToNewlines }}`, expression: `spacesToNewlines("a b")`, want: "a\nb"},
		{name: "rotate", template: `{{ "abcd" | rotate -1 }}`, expression: `rotate("abcd", -1)`, want: "dabc"},
		{name: "rotate default", template: `{{ "abcd" | rotate }}`, expression: `rotate("abcd")`, want: "bcda"},
		{name: "quote default", template: `{{ "hi" | quote }}`, expression: `quote("hi")`, want: `"hi"`},
		{name: "escape regex", template: `{{ "a.b" | escapeRegex }}`, expression: `escapeRegex("a.b")`, want: `a\.b`},
		{name: "normalize Unicode", template: `{{ "é" | normalizeUnicode "nfc" }}`, expression: `normalizeUnicode("é", "nfc")`, want: "é"},
		{name: "normalize Unicode default", template: `{{ "é" | normalizeUnicode }}`, expression: `normalizeUnicode("é")`, want: "é"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTemplate, err := renderTemplate(tt.template)
			if err != nil {
				t.Fatalf("template: %v", err)
			}
			gotExpr, err := Eval(tt.expression, map[string]any{})
			if err != nil {
				t.Fatalf("Expr: %v", err)
			}
			if gotTemplate != tt.want || gotExpr != tt.want {
				t.Fatalf("template = %q, Expr = %#v, want %q", gotTemplate, gotExpr, tt.want)
			}
		})
	}
}

func TestTextHelpersAcceptSizedIntegerArguments(t *testing.T) {
	t.Parallel()
	// parseJSON normalizes whole numbers to int64, and Expr environments carry sized integers, so
	// counts and lengths that arrive as data must work exactly like literals.
	environment := map[string]any{"count": int64(3), "length": int64(5), "value": "abcdefgh"}
	tests := []struct {
		name       string
		expression string
		want       any
	}{
		{name: "repeat", expression: `repeat("ha", count)`, want: "hahaha"},
		{name: "truncate", expression: `truncate(value, length)`, want: "abcde"},
		{name: "rotate", expression: `rotate("abcd", count)`, want: "dabc"},
		{name: "tabs to spaces", expression: `tabsToSpaces("a\tb", count)`, want: "a   b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Eval(tt.expression, environment)
			if err != nil {
				t.Fatalf("Eval(%q): %v", tt.expression, err)
			}
			if got != tt.want {
				t.Fatalf("value = %#v, want %#v", got, tt.want)
			}
		})
	}
	rendered, err := renderTemplate(`{{ repeat (parseJSON "3") "-" "ha" }}`)
	if err != nil {
		t.Fatalf("template: %v", err)
	}
	if rendered != "ha-ha-ha" {
		t.Fatalf("template value = %q, want %q", rendered, "ha-ha-ha")
	}
}

func TestTextHelpersPreserveExprBuiltins(t *testing.T) {
	t.Parallel()
	repeated, err := Eval(`repeat("go", 2)`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if repeated != "gogo" {
		t.Fatalf("repeat value = %#v", repeated)
	}
	reversed, err := Eval(`reverse([1, 2, 3])`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reversed, []any{3, 2, 1}) {
		t.Fatalf("reverse value = %#v", reversed)
	}
}

func TestTextAdapterErrorsStopEvaluation(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		`repeat("x", -1)`,
		`repeat("x", 1000001)`,
		`truncate("x", 1, "...")`,
		`tabsToSpaces("x", 0)`,
		`spacesToTabs("x", 0)`,
		`quote("x", "")`,
		`normalizeUnicode("x", "other")`,
	} {
		if _, err := Eval(source, map[string]any{}); err == nil {
			t.Fatalf("Eval(%q) succeeded", source)
		}
	}
	for _, source := range []string{
		`{{ "x" | repeat -1 }}`,
		`{{ "x" | truncate 1 "..." }}`,
		`{{ "x" | tabsToSpaces 0 }}`,
		`{{ "x" | spacesToTabs 0 }}`,
		`{{ "x" | quote "" }}`,
		`{{ "x" | normalizeUnicode "other" }}`,
	} {
		if _, err := renderTemplate(source); err == nil {
			t.Fatalf("template %q succeeded", source)
		}
	}
}
