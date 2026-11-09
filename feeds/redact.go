package feeds

import "regexp"

var (
	urlCreds = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s@'"]*@`)
	urlQuery = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s?'"]*)\?[^\s'"]*`)
)

// Redact hides user names, passwords and query strings (which often carry
// tokens) in any URLs in s, so messages can be shown and logged safely.
func Redact(s string) string {
	s = urlCreds.ReplaceAllString(s, "${1}***@")
	return urlQuery.ReplaceAllString(s, "${1}?***")
}
