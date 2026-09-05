package expression

// Default returns fallback when value is empty according to Go template truth rules.
func Default(value, fallback any) any { return defaultValue(fallback, value) }

// Coalesce returns the first non-empty value, or nil when every value is empty.
func Coalesce(values ...any) any { return coalesce(values...) }

// Required returns value when it is non-empty and otherwise returns message as an error.
func Required(value any, message string) (any, error) { return required(message, value) }

// Indent prefixes every line in value with the requested number of spaces.
func Indent(value string, spaces int) (string, error) { return indent(spaces, value) }

// Nindent prepends a newline and then indents value.
func Nindent(value string, spaces int) (string, error) { return nindent(spaces, value) }

// List constructs a list from values.
func List(values ...any) []any { return list(values...) }

// Dict constructs a string-keyed map from alternating key and value arguments.
func Dict(values ...any) (map[string]any, error) { return dict(values...) }

// Get returns a string-keyed map entry or nil when the key is absent.
func Get(collection any, key string) (any, error) { return get(key, collection) }

// HasKey reports whether a string-keyed map contains key.
func HasKey(collection any, key string) (bool, error) { return hasKey(key, collection) }

// Keys returns the sorted keys from a string-keyed map.
func Keys(collection any) ([]string, error) { return keys(collection) }

// SortAlpha returns a sorted copy of a string slice or array.
func SortAlpha(collection any) ([]string, error) { return sortAlpha(collection) }

// Join joins a string slice or array with separator.
func Join(collection any, separator string) (string, error) { return join(separator, collection) }

// ToJSON returns indented JSON.
func ToJSON(value any) (string, error) { return toJSON(value) }

// ToJSONCompact returns compact JSON.
func ToJSONCompact(value any) (string, error) { return toJSONCompact(value) }

// ToYAML returns one YAML document, including its terminal newline.
func ToYAML(value any) (string, error) { return toYAML(value) }

// ParseJSON parses exactly one JSON value into Wuko's runtime data types.
func ParseJSON(value string) (any, error) { return parseJSON(value) }

// ParseYAML parses exactly one YAML document into Wuko's runtime data types.
func ParseYAML(value string) (any, error) { return parseYAML(value) }

// ReverseText reverses the grapheme clusters on each line of value.
func ReverseText(value string) string { return reverseText(value) }

// ReverseWords reverses the whitespace-separated words on each line of value.
func ReverseWords(value string) string { return reverseWords(value) }

// Repeat repeats value count times with separator between copies.
func Repeat(value string, count int, separator string) (string, error) {
	return repeat(value, count, separator)
}

// Truncate limits each line to length grapheme clusters, including suffix when truncated.
func Truncate(value string, length int, suffix string) (string, error) {
	return truncate(value, length, suffix)
}

// Squeeze collapses whitespace runs on each line to one ASCII space.
func Squeeze(value string) string { return squeeze(value) }

// RemoveWhitespace removes every Unicode whitespace character from value.
func RemoveWhitespace(value string) string { return removeWhitespace(value) }

// RemovePunctuation retains only Unicode letters, numbers, and whitespace.
func RemovePunctuation(value string) string { return removePunctuation(value) }

// RemoveAccents removes Unicode combining marks from normalized text.
func RemoveAccents(value string) string { return removeAccents(value) }

// RemoveNonASCII removes every non-ASCII character from value.
func RemoveNonASCII(value string) string { return removeNonASCII(value) }

// StripHTML removes angle-bracketed tags and decodes HTML entities.
func StripHTML(value string) string { return stripHTML(value) }

// TabsToSpaces replaces tabs with width spaces.
func TabsToSpaces(value string, width int) (string, error) { return tabsToSpaces(value, width) }

// SpacesToTabs replaces each run of width spaces with one tab.
func SpacesToTabs(value string, width int) (string, error) { return spacesToTabs(value, width) }

// NewlinesToSpaces joins lines with one ASCII space.
func NewlinesToSpaces(value string) string { return newlinesToSpaces(value) }

// SpacesToNewlines places every whitespace-separated word on its own line.
func SpacesToNewlines(value string) string { return spacesToNewlines(value) }

// Rotate rotates each line left by count grapheme clusters; negative counts rotate right.
func Rotate(value string, count int) string { return rotate(value, count) }

// Quote wraps every line in delimiter.
func Quote(value, delimiter string) (string, error) { return quote(value, delimiter) }

// EscapeRegex escapes value for literal use in a Go RE2 regular expression.
func EscapeRegex(value string) string { return escapeRegex(value) }

// NormalizeUnicode applies the requested NFC, NFD, NFKC, or NFKD normalization form.
func NormalizeUnicode(value, form string) (string, error) { return normalizeUnicode(value, form) }
