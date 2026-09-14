package expression

import "strings"

// jsonPointerEscaper is built once: strings.Replacer compiles its match trie lazily and is safe
// for concurrent use, so rebuilding it per call would repeat that work on every escaped token.
var jsonPointerEscaper = strings.NewReplacer("~", "~0", "/", "~1")

// JSONPointerEscape escapes one RFC 6901 JSON Pointer path token.
func JSONPointerEscape(value string) string {
	return jsonPointerEscaper.Replace(value)
}
