package lua

import (
	"regexp"
	"sync"

	glua "github.com/yuin/gopher-lua"
)

// regexCacheLimit bounds the compiled patterns kept across Lua steps. Scripts usually reuse a
// handful of patterns inside loops; a script that builds patterns dynamically only resets it.
const regexCacheLimit = 256

var regexCache = struct {
	sync.Mutex
	patterns map[string]*regexp.Regexp
}{patterns: make(map[string]*regexp.Regexp)}

func compileRegex(state *glua.LState, helper, pattern string) *regexp.Regexp {
	regexCache.Lock()
	compiled, ok := regexCache.patterns[pattern]
	regexCache.Unlock()
	if ok {
		return compiled
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		state.RaiseError("helpers.%s: invalid pattern: %v", helper, err)
		return nil
	}
	regexCache.Lock()
	if len(regexCache.patterns) >= regexCacheLimit {
		clear(regexCache.patterns)
	}
	regexCache.patterns[pattern] = compiled
	regexCache.Unlock()
	return compiled
}

func helperRegexMatch(state *glua.LState) int {
	text := state.CheckString(1)
	state.Push(glua.LBool(compileRegex(state, "regex_match", state.CheckString(2)).MatchString(text)))
	return 1
}

// helperRegexFind returns the leftmost match followed by its capture groups, or nil without a
// match. A group that did not participate is nil, so it differs from one that matched "".
func helperRegexFind(state *glua.LState) int {
	text := state.CheckString(1)
	compiled := compileRegex(state, "regex_find", state.CheckString(2))
	indexes := compiled.FindStringSubmatchIndex(text)
	if indexes == nil {
		state.Push(glua.LNil)
		return 1
	}
	for i := 0; i < len(indexes); i += 2 {
		if indexes[i] < 0 {
			state.Push(glua.LNil)
			continue
		}
		state.Push(glua.LString(text[indexes[i]:indexes[i+1]]))
	}
	return len(indexes) / 2
}

func helperRegexFindAll(state *glua.LState) int {
	text := state.CheckString(1)
	compiled := compileRegex(state, "regex_find_all", state.CheckString(2))
	result := state.NewTable()
	for _, match := range compiled.FindAllString(text, state.OptInt(3, -1)) {
		result.Append(glua.LString(match))
	}
	state.Push(result)
	return 1
}

// helperRegexCaptures returns the first match's groups by index and by name, with index 0 as
// the whole match, or nil without a match. Groups that did not participate are absent.
func helperRegexCaptures(state *glua.LState) int {
	text := state.CheckString(1)
	compiled := compileRegex(state, "regex_captures", state.CheckString(2))
	indexes := compiled.FindStringSubmatchIndex(text)
	if indexes == nil {
		state.Push(glua.LNil)
		return 1
	}
	result := state.NewTable()
	for group, name := range compiled.SubexpNames() {
		if indexes[2*group] < 0 {
			continue
		}
		value := glua.LString(text[indexes[2*group]:indexes[2*group+1]])
		result.RawSetInt(group, value)
		if name != "" {
			result.RawSetString(name, value)
		}
	}
	state.Push(result)
	return 1
}

func helperRegexReplace(state *glua.LState) int {
	text := state.CheckString(1)
	compiled := compileRegex(state, "regex_replace", state.CheckString(2))
	state.Push(glua.LString(compiled.ReplaceAllString(text, state.CheckString(3))))
	return 1
}

func helperRegexSplit(state *glua.LState) int {
	text := state.CheckString(1)
	compiled := compileRegex(state, "regex_split", state.CheckString(2))
	result := state.NewTable()
	for _, part := range compiled.Split(text, state.OptInt(3, -1)) {
		result.Append(glua.LString(part))
	}
	state.Push(result)
	return 1
}
