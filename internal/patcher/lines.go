package patcher

import "regexp"

// The line grammars the patch drivers and the clean/smudge filter
// match, defined once so the apply side and the restore side can't
// drift. Whitespace around `=` is horizontal only: `\s` also matches a
// newline, which let an empty `key =` swallow the line below it.

// assignLine matches a `key = value` line of an INI or TOML section:
// optional indent, the key, spacing around `=`, then the value to end
// of line. Group 1 is everything before the value, group 2 the value.
func assignLine(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^([ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=[ \t]*)(.*)$`)
}

func dotenvLinePattern(key string) string {
	return `(?m)^` + regexp.QuoteMeta(key) + `[ \t]*=(.*)`
}

// dotenvLine matches a `KEY=value` line of a dotenv file. Group 1 is
// the value.
func dotenvLine(key string) *regexp.Regexp {
	return regexp.MustCompile(dotenvLinePattern(key) + `$`)
}

// dotenvLineWithNewline is dotenvLine plus the line's trailing newline
// when there is one, for removing the line outright.
func dotenvLineWithNewline(key string) *regexp.Regexp {
	return regexp.MustCompile(dotenvLinePattern(key) + `\n?`)
}

// phpunitEnvPattern matches a self-closing `<env name="KEY" …/>`
// element as a run of double-quoted attributes, so a value holding `/`
// or `>` (a URL) still matches.
func phpunitEnvPattern(key string) string {
	return `<env\s+name="` + regexp.QuoteMeta(xmlAttrEscape(key)) + `"(?:\s+[\w:.-]+="[^"]*")*\s*/>`
}

// phpunitEnv matches the `<env name="KEY" …/>` element.
func phpunitEnv(key string) *regexp.Regexp {
	return regexp.MustCompile(phpunitEnvPattern(key))
}

// phpunitEnvLine is phpunitEnv with the line's leading indent and
// trailing newline, for removing the element outright.
func phpunitEnvLine(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*` + phpunitEnvPattern(key) + `\n?`)
}

var phpunitValueAttr = regexp.MustCompile(`\svalue="([^"]*)"`)
