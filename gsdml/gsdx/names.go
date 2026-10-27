// SPDX-License-Identifier: MIT

package gsdx

import (
	"path"
	"regexp"
	"strings"
)

// checkItemName rejects the ZIP item names that are dangerous to whoever
// extracts or resolves them, before anything is read. Ecma-376 Part 2,
// "Part names" (the part name grammar) and "Mapping part names to ZIP item
// names": a part name is "/" followed by the ZIP item name, it consists of
// non-empty segments, no segment is "." or "..", and it shall not contain
// a percent-encoded "/" or "\" nor percent-encoded unreserved characters
// (so "%2e%2e" is no way around the rule).
func checkItemName(name string) error {
	if name == "" {
		return errMalformed("empty ZIP item name")
	}
	if strings.HasPrefix(name, "/") {
		return errMalformed("ZIP item name %q is absolute", name)
	}
	for _, c := range []byte(name) {
		if c < 0x20 || c == 0x7f {
			return errMalformed("ZIP item name %q contains a control character", name)
		}
		if c == '\\' {
			return errMalformed("ZIP item name %q contains a backslash", name)
		}
	}
	segs := strings.Split(strings.TrimSuffix(name, "/"), "/")
	if len(segs) > 0 && len(segs[0]) == 2 && segs[0][1] == ':' && isAlpha(segs[0][0]) {
		return errMalformed("ZIP item name %q starts with a drive letter", name)
	}
	for _, s := range segs {
		dec := asciiLower(s)
		dec = strings.ReplaceAll(dec, "%2e", ".")
		if s == "" || dec == "." || dec == ".." {
			return errMalformed("ZIP item name %q has an empty, \".\" or \"..\" segment", name)
		}
		if strings.Contains(dec, "%2f") || strings.Contains(dec, "%5c") {
			return errMalformed("ZIP item name %q contains a percent-encoded \"/\" or \"\\\"", name)
		}
	}
	return nil
}

// pieceName matches the ZIP item names of interleaved parts (Ecma-376
// Part 2, "Mapping part names to ZIP item names", pieces).
var pieceName = regexp.MustCompile(`/\[[0-9]+\](\.last)?\.piece$`)

// partNameIssues returns the rules of the part name grammar a part name
// breaks without being dangerous: Ecma-376 Part 2, "Part names" (segments
// shall not end with "."; only pchar characters of RFC 3986; percent
// encoding well-formed and not used for unreserved characters).
func partNameIssues(name string) []string {
	var out []string
	if pieceName.MatchString(name) {
		out = append(out, "interleaved part piece: pieces are not reassembled, the entry is kept as it is")
	}
	for _, s := range strings.Split(name[1:], "/") {
		if strings.HasSuffix(s, ".") {
			out = append(out, "part name segment ends with \".\"")
		}
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c == '%':
				if i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
					out = append(out, "malformed percent-encoding in part name")
				} else if isUnreserved(unhex(s[i+1])<<4 | unhex(s[i+2])) {
					out = append(out, "percent-encoded unreserved character in part name")
				}
			case isUnreserved(c) || strings.IndexByte("!$&'()*+,;=:@", c) >= 0:
			default:
				out = append(out, "part name contains a character outside the RFC 3986 pchar set (non-ASCII characters must be percent-encoded)")
			}
		}
	}
	return dedupe(out)
}

func dedupe(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func isAlpha(c byte) bool { return c|0x20 >= 'a' && c|0x20 <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isHex(c byte) bool   { return isDigit(c) || (c|0x20 >= 'a' && c|0x20 <= 'f') }

func unhex(c byte) byte {
	if isDigit(c) {
		return c - '0'
	}
	return c | 0x20 - 'a' + 10
}

func isUnreserved(c byte) bool {
	return isAlpha(c) || isDigit(c) || c == '-' || c == '.' || c == '_' || c == '~'
}

// asciiLower folds ASCII letters only: OPC part name equivalence is
// ASCII case-insensitive.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// relsPartFor returns the relationships part name of a source: "/" (the
// package) has "/_rels/.rels", "/a/b.xml" has "/a/_rels/b.xml.rels"
// (Ecma-376 Part 2, "Relationships part" naming).
func relsPartFor(source string) string {
	if source == "/" {
		return "/_rels/.rels"
	}
	dir, file := path.Split(source)
	return dir + "_rels/" + file + ".rels"
}

// sourceOfRelsPart is the inverse of relsPartFor; ok is false when name
// is not the name of a relationships part.
func sourceOfRelsPart(name string) (source string, ok bool) {
	dir, file := path.Split(name)
	if !strings.HasSuffix(asciiLower(dir), "/_rels/") || !strings.HasSuffix(asciiLower(file), ".rels") {
		return "", false
	}
	parent := dir[:len(dir)-len("_rels/")]
	base := file[:len(file)-len(".rels")]
	if base == "" {
		if parent == "/" {
			return "/", true
		}
		return "", false
	}
	return parent + base, true
}

func isRelsPart(name string) bool {
	_, ok := sourceOfRelsPart(name)
	return ok
}

// extension returns the extension of a part name without the dot, as
// Default elements name it.
func extension(name string) string {
	file := path.Base(name)
	i := strings.LastIndexByte(file, '.')
	if i < 0 {
		return ""
	}
	return file[i+1:]
}
