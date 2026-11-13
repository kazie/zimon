// Package shell quotes command lines for a shell: one to paste into a
// terminal, or one for ssh to hand to the remote login shell, which may be
// sh, bash, zsh or fish.
package shell

import "strings"

// safe are the bytes a word may hold and still need no quotes.
const safe = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:,@%+"

// quoted escapes inside single quotes. Quotes and backslashes go outside
// them, escaped: inside, fish reads \' and \\ as escapes where sh does not.
var quoted = strings.NewReplacer(`'`, `'\''`, `\`, `'\\'`)

// Quote joins args into one command line that sh, bash, zsh and fish all
// split back into exactly args.
func Quote(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		// A leading = is zsh's command lookup (=ls), so such a word is quoted.
		if a != "" && a[0] != '=' && strings.Trim(a, safe) == "" {
			b.WriteString(a)
		} else {
			b.WriteString("'" + quoted.Replace(a) + "'")
		}
	}
	return b.String()
}
