package clitest

import (
	"io"
	"os"
	"testing"
)

func CaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	writerClosed := false
	closeWriter := func() error {
		if writerClosed {
			return nil
		}
		writerClosed = true
		return w.Close()
	}
	defer func() { _ = closeWriter() }()
	os.Stderr = w
	defer func() { os.Stderr = old }()

	fn()
	if err := closeWriter(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
