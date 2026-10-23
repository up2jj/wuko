//go:build fuzz

package fswatch

import "testing"

// FuzzPatternMatching checks watch-pattern normalization, matching, and pruning against arbitrary
// patterns and paths.
func FuzzPatternMatching(f *testing.F) {
	f.Add("**/*.go", "node_modules", "src/a/b.go")
	f.Add("{a,b}/[x-z]*", "**/.git", ".git/x")
	f.Add("*", "", "../outside")
	f.Fuzz(func(t *testing.T, pattern, ignore, name string) {
		config, err := Normalize(Config{Root: ".", Patterns: []string{pattern}, Ignore: []string{ignore}}, true, false, true)
		if err != nil {
			return
		}
		patterns := patternComponents(config.Patterns)
		ignores := patternComponents(config.Ignore)
		_, _ = matchingPath("/root", patterns, ignores, "/root/"+name)
		_ = descendable(patterns, ignores, "/root", "/root/"+name)
	})
}
