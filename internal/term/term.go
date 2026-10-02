// Package term cleans terminal output for display in chat.
package term

import "regexp"

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// StripANSI removes terminal color and cursor codes.
func StripANSI(s string) string { return ansi.ReplaceAllString(s, "") }
