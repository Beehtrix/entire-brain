package base

import (
	"regexp"
	"strings"
)

var CheckpointTrailerRegex = regexp.MustCompile(
	CheckpointTrailerKey + `:\s*([a-f0-9]{12}|[0-7][0-9A-HJKMNP-TV-Z]{25})(?:\s|$)`,
)

func CheckpointIDsFromCommitMessageText(message string) []string {
	matches := CheckpointTrailerRegex.FindAllStringSubmatch(message, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		checkpointID := strings.TrimSpace(match[1])
		if _, ok := seen[checkpointID]; ok {
			continue
		}
		seen[checkpointID] = struct{}{}
		ids = append(ids, checkpointID)
	}
	return ids
}

const CheckpointTrailerKey = "Entire-Checkpoint"
