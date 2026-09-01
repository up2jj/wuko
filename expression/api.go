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
