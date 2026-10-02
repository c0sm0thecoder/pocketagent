// Package argv expands {placeholder} templates in command lines. Arguments
// are never passed through a shell, so values cannot inject commands.
package argv

import "strings"

// Expand replaces {key} in each argument with vars[key] in a single pass,
// so placeholders inside substituted values (user text) stay literal.
func Expand(args []string, vars map[string]string) []string {
	pairs := make([]string, 0, 2*len(vars))
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	r := strings.NewReplacer(pairs...)
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = r.Replace(a)
	}
	return out
}

// Join returns wrap followed by command, both expanded with vars. It is how
// every adapter builds its process: wrap is an optional prefix such as a
// container runner.
func Join(wrap, command []string, vars map[string]string) []string {
	return append(Expand(wrap, vars), Expand(command, vars)...)
}
