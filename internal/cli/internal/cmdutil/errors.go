package cmdutil

import (
	"regexp"
	"strings"
)

var commandErrorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

func CommandErrorCode(err error) string {
	message := err.Error()
	code, _, ok := strings.Cut(message, ":")
	if ok {
		code = strings.TrimSpace(code)
		if commandErrorCodePattern.MatchString(code) {
			return code
		}
	}
	return "command_failed"
}
