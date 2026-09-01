package expression

import (
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strings"
)

var uriFields = map[string]struct{}{
	"scheme": {}, "opaque": {}, "username": {}, "password": {},
	"host": {}, "path": {}, "query": {}, "fragment": {},
}

// ParseURI parses an absolute or relative URI reference into JSON-compatible components.
// Query keys and values are percent-decoded; each key maps to its ordered list of values.
func ParseURI(value string) (map[string]any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("URI must not be blank")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parsing URI %q: %w", value, err)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("parsing URI query: %w", err)
	}

	queryObject := make(map[string]any, len(query))
	for key, values := range query {
		items := make([]any, len(values))
		for i, item := range values {
			items[i] = item
		}
		queryObject[key] = items
	}
	result := map[string]any{
		"scheme": parsed.Scheme, "opaque": parsed.Opaque, "host": parsed.Host,
		"path": parsed.Path, "query": queryObject, "fragment": parsed.Fragment,
	}
	if parsed.User != nil {
		result["username"] = parsed.User.Username()
		if password, present := parsed.User.Password(); present {
			result["password"] = password
		}
	}
	return result, nil
}

// BuildURI constructs a canonical URI reference from components.
// Query values may be strings or lists or arrays containing only strings.
func BuildURI(parts map[string]any) (string, error) {
	if parts == nil {
		return "", fmt.Errorf("URI components must be an object")
	}
	for _, name := range slices.Sorted(maps.Keys(parts)) {
		if _, allowed := uriFields[name]; !allowed {
			return "", fmt.Errorf("unknown URI component %q", name)
		}
	}

	scheme, _, err := uriStringPart(parts, "scheme")
	if err != nil {
		return "", err
	}
	if err := validateURIScheme(scheme); err != nil {
		return "", err
	}
	opaque, _, err := uriStringPart(parts, "opaque")
	if err != nil {
		return "", err
	}
	username, hasUsername, err := uriStringPart(parts, "username")
	if err != nil {
		return "", err
	}
	password, hasPassword, err := uriStringPart(parts, "password")
	if err != nil {
		return "", err
	}
	host, _, err := uriStringPart(parts, "host")
	if err != nil {
		return "", err
	}
	if err := validateURIHost(host); err != nil {
		return "", err
	}
	path, _, err := uriStringPart(parts, "path")
	if err != nil {
		return "", err
	}
	fragment, _, err := uriStringPart(parts, "fragment")
	if err != nil {
		return "", err
	}
	if hasPassword && !hasUsername {
		return "", fmt.Errorf("URI component %q requires %q", "password", "username")
	}
	if opaque != "" && (hasUsername || hasPassword || host != "" || path != "") {
		return "", fmt.Errorf("URI component %q cannot be combined with username, password, host, or path", "opaque")
	}

	query, err := uriQuery(parts)
	if err != nil {
		return "", err
	}
	result := &url.URL{
		Scheme: scheme, Opaque: opaque, Host: host, Path: path,
		RawQuery: query.Encode(), Fragment: fragment,
	}
	if hasUsername {
		if hasPassword {
			result.User = url.UserPassword(username, password)
		} else {
			result.User = url.User(username)
		}
	}

	built := result.String()
	if strings.TrimSpace(built) == "" {
		return "", fmt.Errorf("URI components must produce a non-blank URI")
	}
	if _, err := url.Parse(built); err != nil {
		return "", fmt.Errorf("building URI: %w", err)
	}
	return built, nil
}

func uriStringPart(parts map[string]any, name string) (string, bool, error) {
	value, present := parts[name]
	if !present {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("URI component %q must be a string, got %T", name, value)
	}
	return text, true, nil
}

func validateURIScheme(scheme string) error {
	if scheme == "" {
		return nil
	}
	for i, character := range []byte(scheme) {
		if i == 0 && !uriASCIILetter(character) {
			return fmt.Errorf("invalid URI scheme %q", scheme)
		}
		if i > 0 && !uriASCIILetter(character) && !(character >= '0' && character <= '9') &&
			character != '+' && character != '-' && character != '.' {
			return fmt.Errorf("invalid URI scheme %q", scheme)
		}
	}
	return nil
}

func uriASCIILetter(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func validateURIHost(host string) error {
	if host == "" {
		return nil
	}
	encoded := (&url.URL{Host: host}).String()
	parsed, err := url.Parse(encoded)
	if err != nil || parsed.User != nil || parsed.Host != host || parsed.Path != "" {
		if err != nil {
			return fmt.Errorf("invalid URI host %q: %w", host, err)
		}
		return fmt.Errorf("invalid URI host %q", host)
	}
	return nil
}

func uriQuery(parts map[string]any) (url.Values, error) {
	raw, present := parts["query"]
	if !present {
		return make(url.Values), nil
	}
	value, err := stringMapValue(raw)
	if err != nil {
		return nil, fmt.Errorf("URI component %q: %w", "query", err)
	}
	query := make(url.Values, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		key := iterator.Key().String()
		values, err := uriQueryValues(iterator.Value().Interface())
		if err != nil {
			return nil, fmt.Errorf("URI query parameter %q: %w", key, err)
		}
		query[key] = values
	}
	return query, nil
}

func uriQueryValues(value any) ([]string, error) {
	if text, ok := value.(string); ok {
		return []string{text}, nil
	}
	if value == nil {
		return nil, fmt.Errorf("must be a string or list of strings, got <nil>")
	}
	items := reflect.ValueOf(value)
	if items.Kind() != reflect.Array && items.Kind() != reflect.Slice {
		return nil, fmt.Errorf("must be a string or list of strings, got %T", value)
	}
	result := make([]string, items.Len())
	for i := range items.Len() {
		item := items.Index(i)
		if item.Kind() == reflect.Interface {
			if item.IsNil() {
				return nil, fmt.Errorf("item %d is <nil>, want string", i)
			}
			item = item.Elem()
		}
		if item.Kind() != reflect.String {
			return nil, fmt.Errorf("item %d is %s, want string", i, item.Type())
		}
		result[i] = item.String()
	}
	return result, nil
}
