package base

import (
	"time"
)

type Options struct {
	Version string
	Env     EntireEnv
	Runner  CommandRunner
	Now     func() time.Time
}
