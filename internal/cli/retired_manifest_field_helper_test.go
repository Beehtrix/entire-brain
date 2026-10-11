package cli

import "fmt"

func wrapTestError(prefix string, err error) error {
	return fmt.Errorf("%s: %w", prefix, err)
}
