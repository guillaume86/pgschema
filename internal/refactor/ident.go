package refactor

import "strings"

// replaceIdent rewrites the identifier old to new in a SQL expression the way
// PostgreSQL's own column rename rewrites a stored one: bare or double-quoted
// occurrences, never inside a single-quoted literal and never as part of a
// longer word. An expression that does not name the column is returned as is.
func replaceIdent(expr, old, new string) string {
	var b strings.Builder
	for i := 0; i < len(expr); {
		switch c := expr[i]; {
		case c == '\'':
			j := i + 1
			for j < len(expr) {
				if expr[j] == '\'' {
					if j+1 < len(expr) && expr[j+1] == '\'' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j < len(expr) {
				j++
			}
			b.WriteString(expr[i:j])
			i = j
		case c == '"':
			j := strings.IndexByte(expr[i+1:], '"')
			if j < 0 {
				b.WriteString(expr[i:])
				return b.String()
			}
			end := i + 1 + j + 1
			if expr[i+1:end-1] == old {
				b.WriteString(`"` + new + `"`)
			} else {
				b.WriteString(expr[i:end])
			}
			i = end
		case isWordByte(c):
			j := i
			for j < len(expr) && isWordByte(expr[j]) {
				j++
			}
			if w := expr[i:j]; w == old {
				b.WriteString(new)
			} else {
				b.WriteString(w)
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
