package cli

import (
	"archive/tar"
	"context"
	"time"
)

// Bundle materialization is written straight to the artifact, never back into
// the persistent brain. A read failure prevents publishing the temporary tar.
func addTranscriptBundlePath(ctx context.Context, tw *tar.Writer, store *transcriptStore, rel string, now time.Time) error {
	data, err := store.Read(ctx, rel)
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: 0600, Size: int64(len(data)), ModTime: now}); err != nil {
		return err
	}
	_, err = tw.Write(data)
	return err
}
