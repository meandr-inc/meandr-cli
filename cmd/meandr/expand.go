package main

import (
	"os"
	"strings"
)

// expandArgs replaces $NAME and ${NAME} in each argument with the variable's
// value. A name lookup does not know is left as written, and $$ is a literal
// dollar. A substituted value is never expanded again.
func expandArgs(args []string, lookup func(string) (string, bool)) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = expand(arg, lookup)
	}
	return out
}

func expand(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "$") {
		return s
	}

	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}

		switch next := s[i+1]; {
		case next == '$':
			b.WriteByte('$')
			i++
		case next == '{':
			end := strings.IndexByte(s[i+2:], '}')
			name := ""
			if end >= 0 {
				name = s[i+2 : i+2+end]
			}
			if value, ok := lookup(name); ok && validName(name) {
				b.WriteString(value)
				i += 2 + end
			} else {
				b.WriteByte('$')
			}
		case nameStart(next):
			end := i + 1
			for end < len(s) && nameChar(s[end]) {
				end++
			}
			if value, ok := lookup(s[i+1 : end]); ok {
				b.WriteString(value)
				i = end - 1
			} else {
				b.WriteByte('$')
			}
		default:
			b.WriteByte('$')
		}
	}
	return b.String()
}

func validName(name string) bool {
	if name == "" || !nameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !nameChar(name[i]) {
			return false
		}
	}
	return true
}

func nameStart(c byte) bool { return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') }
func nameChar(c byte) bool  { return nameStart(c) || (c >= '0' && c <= '9') }

// environLookup reads loaded variables ahead of this process's own: the
// MCP server sees the same.
func environLookup(loaded []string) func(string) (string, bool) {
	vars := make(map[string]string, len(loaded))
	for _, v := range loaded {
		if name, value, ok := strings.Cut(v, "="); ok {
			vars[name] = value
		}
	}
	return func(name string) (string, bool) {
		if value, ok := vars[name]; ok {
			return value, true
		}
		return os.LookupEnv(name)
	}
}
