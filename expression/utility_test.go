package expression

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/expr-lang/expr"
	"github.com/google/uuid"
)

func TestEncodingHelpers(t *testing.T) {
	tests := []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{name: "base64", run: func() (string, error) { return Base64Encode("wuko ✓", nil) }, want: "d3VrbyDinJM="},
		{name: "base64 URL raw", run: func() (string, error) {
			return Base64Encode("?ÿ", map[string]any{"alphabet": "url", "padding": false})
		}, want: "P8O_"},
		{name: "base64 decode", run: func() (string, error) { return Base64Decode("d3VrbyDinJM=", nil) }, want: "wuko ✓"},
		{name: "hex lower", run: func() (string, error) { return HexEncode("Wuko", false) }, want: "57756b6f"},
		{name: "hex upper", run: func() (string, error) { return HexEncode("Wuko", true) }, want: "57756B6F"},
		{name: "hex decode", run: func() (string, error) { return HexDecode("F09F9A80") }, want: "🚀"},
		{name: "URL", run: func() (string, error) { return URLEncode("a+b /✓") }, want: "a%2Bb%20%2F%E2%9C%93"},
		{name: "URL decode preserves plus", run: func() (string, error) { return URLDecode("a+b%20c") }, want: "a+b c"},
		{name: "HTML", run: func() (string, error) { return HTMLEncode(`<a title="x">&</a>`) }, want: "&lt;a title=&#34;x&#34;&gt;&amp;&lt;/a&gt;"},
		{name: "HTML decode", run: func() (string, error) { return HTMLDecode("&lt;wuko&gt; &#128640;") }, want: "<wuko> 🚀"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.run()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestEncodingHelpersRejectInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{name: "base64 option", run: func() error { _, err := Base64Encode("x", map[string]any{"alphabet": "mime"}); return err }, want: "alphabet"},
		{name: "base64 padding", run: func() error { _, err := Base64Decode("eA", nil); return err }, want: "decoding base64"},
		{name: "base64 UTF-8", run: func() error { _, err := Base64Decode("/w==", nil); return err }, want: "valid UTF-8"},
		{name: "hex odd", run: func() error { _, err := HexDecode("abc"); return err }, want: "decoding hex"},
		{name: "hex UTF-8", run: func() error { _, err := HexDecode("ff"); return err }, want: "valid UTF-8"},
		{name: "URL escape", run: func() error { _, err := URLDecode("%GG"); return err }, want: "invalid escape"},
		{name: "URL UTF-8", run: func() error { _, err := URLDecode("%FF"); return err }, want: "valid UTF-8"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestHashHelpersKnownVectorsAndFormats(t *testing.T) {
	tests := []struct {
		name string
		run  func() (string, error)
		want string
	}{
		{name: "MD5", run: func() (string, error) { return MD5("hello", nil) }, want: "5d41402abc4b2a76b9719d911017c592"},
		{name: "SHA1", run: func() (string, error) { return SHA1("hello", nil) }, want: "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d"},
		{name: "SHA256", run: func() (string, error) { return SHA256("hello", nil) }, want: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{name: "SHA512 base64", run: func() (string, error) { return SHA512("hello", map[string]any{"encoding": "base64"}) }, want: "m3HSJL1i83hdltRq0+o9czGb+8KJDKra4t/3JRlnPKcjI8PZm6XBHXx6zG4UuMXaDEZjR1wuXDre9G9zvN7AQw=="},
		{name: "HMAC SHA256", run: func() (string, error) { return HMACSHA256("payload", "secret", nil) }, want: "b82fcb791acec57859b989b430a826488ce2e479fdf92326bd0a2e8375a42ba4"},
		{name: "HMAC SHA512", run: func() (string, error) {
			return HMACSHA512("Hi There", strings.Repeat("\x0b", 20), nil)
		}, want: "87aa7cdea5ef619d4ff0b4241a1d6cb02379f4e2ce4ec2787ad0b30545e17cdedaa833b7d6b8a702038b274eaea3f4e4be9d914eeb61f1702e696c203a126854"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.run()
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
	upper, err := SHA256("hello", map[string]any{"uppercase": true})
	if err != nil || upper != strings.ToUpper("2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824") {
		t.Fatalf("uppercase digest = %q, %v", upper, err)
	}
	if _, err := SHA256("hello", map[string]any{"encoding": "base64", "uppercase": true}); err == nil {
		t.Fatal("base64 uppercase should fail")
	}
}

func TestNumberAndInspectionHelpers(t *testing.T) {
	large := "1234567890123456789012345678901234567890"
	hex, err := BaseConvert(large, 10, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	back, err := BaseConvert(hex, 16, 10, false)
	if err != nil || back != large {
		t.Fatalf("round trip = %q, %v", back, err)
	}
	if got, _ := RomanEncode(2024); got != "MMXXIV" {
		t.Fatalf("RomanEncode = %q", got)
	}
	if got, err := RomanDecode("mmxxiv"); err != nil || got != 2024 {
		t.Fatalf("RomanDecode = %d, %v", got, err)
	}
	if _, err := RomanDecode("IIII"); err == nil {
		t.Fatal("non-canonical Roman numeral should fail")
	}
	for value, want := range map[int64]string{1: "1st", 2: "2nd", 3: "3rd", 11: "11th", 22: "22nd", -23: "-23rd"} {
		if got := Ordinal(value); got != want {
			t.Errorf("Ordinal(%d) = %q, want %q", value, got, want)
		}
	}
	value := "e\u0301 👩‍💻\nsecond line\n"
	if CountBytes(value) != len(value) || CountRunes(value) != 19 || CountGraphemes(value) != 16 || CountWords(value) != 4 || CountLines(value) != 2 {
		t.Fatalf("counts = bytes:%d runes:%d graphemes:%d words:%d lines:%d", CountBytes(value), CountRunes(value), CountGraphemes(value), CountWords(value), CountLines(value))
	}
	if CountLines("") != 0 || CountLines("one\r\ntwo") != 2 || CountLines("\n") != 1 {
		t.Fatal("line boundary semantics changed")
	}
}

func TestGeneratorHelpers(t *testing.T) {
	for _, version := range []int{4, 7} {
		value, err := generateUUID(map[string]any{"version": version}, bytes.NewReader(make([]byte, 32)))
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := uuid.Parse(value)
		if err != nil || int(parsed.Version()) != version || parsed.Variant() != uuid.RFC4122 {
			t.Fatalf("UUID %q = version %d variant %v, %v", value, parsed.Version(), parsed.Variant(), err)
		}
	}
	compact, err := generateUUID(map[string]any{"version": 4, "compact": true, "uppercase": true}, bytes.NewReader(make([]byte, 16)))
	if err != nil || len(compact) != 32 || compact != strings.ToUpper(compact) {
		t.Fatalf("compact UUID = %q, %v", compact, err)
	}
	if got, err := randomString(bytes.NewReader(make([]byte, 64)), 6, "ab"); err != nil || got != "aaaaaa" {
		t.Fatalf("random string = %q, %v", got, err)
	}
	if got, err := randomInt(bytes.NewReader(make([]byte, 8)), -5, 5); err != nil || got != -5 {
		t.Fatalf("random int = %d, %v", got, err)
	}
	if got, err := randomToken(bytes.NewReader([]byte{0, 1, 255}), 3, "base64url"); err != nil || got != "AAH_" {
		t.Fatalf("random token = %q, %v", got, err)
	}
	generated, err := password(bytes.NewReader(make([]byte, 128)), 8, nil)
	if err != nil || len(generated) != 8 {
		t.Fatalf("password = %q, %v", generated, err)
	}
	for _, pattern := range []string{`[a-z]`, `[A-Z]`, `[0-9]`, `[^A-Za-z0-9]`} {
		if !regexp.MustCompile(pattern).MatchString(generated) {
			t.Errorf("password %q does not match %s", generated, pattern)
		}
	}
	fixed := func() time.Time { return time.Date(2026, 9, 5, 12, 34, 56, 123456789, time.FixedZone("test", 7200)) }
	if got := currentTime(fixed); got != "2026-09-05T10:34:56.123456789Z" {
		t.Fatalf("currentTime = %q", got)
	}
	if got, _ := unixTimestamp(fixed, "milliseconds"); got != 1788604496123 {
		t.Fatalf("unixTimestamp = %d", got)
	}
}

func TestGeneratorHelpersRejectInvalidConfiguration(t *testing.T) {
	tests := []func() error{
		func() error { _, err := UUID(map[string]any{"version": 5}); return err },
		func() error { _, err := RandomString(1, ""); return err },
		func() error { _, err := RandomString(-1, "a"); return err },
		func() error { _, err := RandomString(maxTextResultBytes+1, "a"); return err },
		func() error { _, err := RandomInt(2, 1); return err },
		func() error { _, err := RandomToken(1, "raw"); return err },
		func() error { _, err := RandomToken(maxTextResultBytes/2+1, "hex"); return err },
		func() error { _, err := Password(3, nil); return err },
		func() error {
			_, err := Password(8, map[string]any{"lower": false, "upper": false, "digits": false, "symbols": false})
			return err
		},
		func() error { _, err := UnixTimestamp("minutes"); return err },
	}
	for index, run := range tests {
		if err := run(); err == nil {
			t.Errorf("case %d unexpectedly succeeded", index)
		}
	}
}

func TestUtilityHelpersAcrossTemplateAndExpr(t *testing.T) {
	templateSource := `{{ "wuko ✓" | base64Encode }}|{{ "ff" | baseConvert 16 10 }}|{{ "MMXXIV" | romanDecode }}|{{ "é" | countGraphemes }}|{{ "payload" | hmacSHA256 "secret" }}`
	parsed, err := template.New("test").Funcs(TemplateFuncs()).Parse(templateSource)
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, nil); err != nil {
		t.Fatal(err)
	}
	program, err := Compile(`base64Encode("wuko ✓") + "|" + baseConvert("ff", 16, 10) + "|" + string(romanDecode("MMXXIV")) + "|" + string(countGraphemes("é")) + "|" + hmacSHA256("payload", "secret")`, expr.Env(map[string]any{}))
	if err != nil {
		t.Fatal(err)
	}
	value, err := expr.Run(program, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if value != rendered.String() {
		t.Fatalf("Expr = %q, template = %q", value, rendered.String())
	}
}

func TestExprNumberHelpersAcceptRuntimeIntegerTypes(t *testing.T) {
	value, err := Eval(`romanEncode(value) + ":" + ordinal(value)`, map[string]any{"value": int64(22)})
	if err != nil {
		t.Fatal(err)
	}
	if value != "XXII:22nd" {
		t.Fatalf("value = %#v", value)
	}
}

func TestTemplateNumberHelpersAcceptRuntimeIntegerTypes(t *testing.T) {
	parsed, err := template.New("test").Funcs(TemplateFuncs()).Parse(`{{ 22 | romanEncode }}:{{ 22 | ordinal }}:{{ .value | romanEncode }}:{{ .value | ordinal }}`)
	if err != nil {
		t.Fatal(err)
	}
	// JSON and YAML decoding hand templates float64, and step outputs hand them int64, so
	// the helpers must coerce every integral numeric type the way Expr and Lua do.
	for _, value := range []any{22, int64(22), float64(22)} {
		var rendered strings.Builder
		if err := parsed.Execute(&rendered, map[string]any{"value": value}); err != nil {
			t.Fatalf("%T: %v", value, err)
		}
		if rendered.String() != "XXII:22nd:XXII:22nd" {
			t.Fatalf("%T rendered %q", value, rendered.String())
		}
	}
	var rejected strings.Builder
	if err := parsed.Execute(&rejected, map[string]any{"value": 1.5}); err == nil ||
		!strings.Contains(err.Error(), "must be an integer") {
		t.Fatalf("fractional value error = %v", err)
	}
}

func TestUtilityHelperErrorsNameTheirOwnArgument(t *testing.T) {
	for _, test := range []struct{ source, want string }{
		{source: `randomInt(1.5, 10)`, want: `random integer minimum must be an integer`},
		{source: `ordinal(1.5)`, want: `ordinal value must be an integer`},
		{source: `baseConvert("ff", 1.5, 10)`, want: `source base must be an integer`},
		{source: `uuid({"version": 1.5})`, want: `UUID option "version" must be an integer`},
	} {
		_, err := Eval(test.source, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s error = %v, want %q", test.source, err, test.want)
		}
	}
	// A dynamically typed root such as vars.<name> passes the checker, so the string
	// helpers must report the argument by name instead of panicking on the assertion.
	for _, source := range []string{`countBytes(value)`, `hexDecode(value)`, `romanDecode(value)`} {
		program, err := Compile(source, expr.AllowUndefinedVariables())
		if err != nil {
			t.Fatal(err)
		}
		_, err = expr.Run(program, map[string]any{"value": 1})
		if err == nil || !strings.Contains(err.Error(), "value must be a string, got int") {
			t.Errorf("%s error = %v", source, err)
		}
	}
	if _, err := AddTime("2026-01-01T00:00:00Z", map[string]any{"days": 1.5}); err == nil ||
		!strings.Contains(err.Error(), `time adjustment "days" must be an integer`) {
		t.Errorf("time adjustment error = %v", err)
	}
}

func TestUtilityOptionsAcrossTemplateAndExpr(t *testing.T) {
	parsed, err := template.New("test").Funcs(TemplateFuncs()).Parse(`{{ "?" | base64Encode (dict "alphabet" "url" "padding" false) }}|{{ "hello" | sha256 (dict "uppercase" true) }}`)
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, nil); err != nil {
		t.Fatal(err)
	}
	value, err := Eval(`base64Encode("?", {"alphabet": "url", "padding": false}) + "|" + sha256("hello", {"uppercase": true})`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if value != rendered.String() {
		t.Fatalf("Expr = %q, template = %q", value, rendered.String())
	}
}

func TestExprGeneratorRegistration(t *testing.T) {
	value, err := Eval(`countBytes(randomToken(4, "hex")) == 8 && countBytes(uuid()) == 36 && randomInt(7, 7) == 7 && countBytes(currentTime()) > 0 && unixTimestamp() > 0`, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if value != true {
		t.Fatalf("value = %#v", value)
	}
	if _, err := Eval(`now()`, map[string]any{}); err == nil {
		t.Fatal("Expr now builtin must remain disabled")
	}
}

func TestBase64URLReference(t *testing.T) {
	value, err := Base64Encode("?ÿ", map[string]any{"alphabet": "url", "padding": false})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || string(decoded) != "?ÿ" {
		t.Fatalf("decoded = %q, %v", decoded, err)
	}
}
