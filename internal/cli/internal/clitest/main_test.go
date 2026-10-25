package clitest

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	os.Exit(PinXDGRoots(m))
}
