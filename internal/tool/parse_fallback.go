package tool

import (
	"errors"
	"regexp"

	"late/internal/tool/ast"
)

// cdPattern matches a standalone `cd` word anywhere in a raw command string.
// It is compiled once at package level. The match is intentionally lexical
// (no quoting or AST awareness), so it can over-match — e.g. a path segment
// like "foo.cd/bar" or a quoted "cd" argument. Over-matching is acceptable:
// this scan only ever runs on input the shell AST parser could not read,
// where failing closed is the safe choice.
var cdPattern = regexp.MustCompile(`\bcd\b`)

// parseErrorHardBlock reports whether an unparseable command still matches one
// of the hard-block signatures (cd usage, unsafe output redirection) and, if
// so, returns the same block error the AST policy would produce for a
// parseable command.
//
// The AST parser (mvdan.cc/sh) fails on truncated or malformed input (e.g. a
// trailing "&&"), which previously downgraded such commands to plain "needs
// confirmation" — letting commands that visibly contain an output redirection
// or `cd` reach the force-revaluate OTP flow (and, after OTP approval, fail
// open into the shell). The scan below is intentionally conservative and may
// over-block edge cases (quoted operators, paths containing "cd"): it only
// ever runs on input the parser could not read, where failing closed is the
// safe choice.
func parseErrorHardBlock(command string) error {
	// 1. cd usage → hard block. Checked first, mirroring the policy order
	// (ast.PolicyEngine.Decide evaluates cd before redirects).
	if cdPattern.MatchString(command) {
		return errors.New(ast.BlockReasonCD)
	}

	// 2. Unsafe output redirect → hard block. Scan every '>' in the raw
	// command. Classification is driven entirely by the text after the
	// operator, so fd prefixes (2>, 1>, 3>) need no special handling —
	// they are just a '>' preceded by digits.
	for i := 0; i < len(command); i++ {
		if command[i] != '>' {
			continue
		}

		// Operator: ">>" (append), ">|" (clobber) or ">" (truncate).
		opLen := 1
		if i+1 < len(command) && (command[i+1] == '>' || command[i+1] == '|') {
			opLen = 2
		}

		if !isSafeRedirectTarget(redirectTargetAfter(command, i+opLen)) {
			return errors.New(ast.BlockReasonRedirect)
		}

		// Skip past the full operator so ">>"/">|" is not re-examined.
		i += opLen - 1
	}

	return nil
}

// redirectTargetAfter extracts the redirect target starting at index start
// (immediately after the '>' operator): leading spaces and tabs are skipped
// and the target runs until the next whitespace character or the end of the
// string. An operator at the end of the command yields an empty target.
func redirectTargetAfter(command string, start int) string {
	i := start
	for i < len(command) && isSpaceByte(command[i]) {
		i++
	}
	j := i
	for j < len(command) && !isSpaceByte(command[j]) {
		j++
	}
	return command[i:j]
}

// isSafeRedirectTarget reports whether a lexically extracted redirect target
// is safe to allow without a parsed AST. Safe targets are the static device
// paths the AST policy accepts (mirroring the semantics of the unexported
// ast.unixIsSafeRedirectTarget) and numeric fd duplications such as "2>&1",
// ">&2" or "1>&2" ("&" followed by one or more digits). An empty target
// (operator at end of string) or any dynamic/quoted/plain-path target is
// never safe.
func isSafeRedirectTarget(target string) bool {
	switch target {
	case "/dev/null", "/dev/stdout", "/dev/stderr":
		return true
	}
	if len(target) > 1 && target[0] == '&' {
		for i := 1; i < len(target); i++ {
			if target[i] < '0' || target[i] > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// isSpaceByte reports whether c is one of the whitespace bytes recognized by
// the lexical redirect scan (spaces and tabs).
func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t'
}
