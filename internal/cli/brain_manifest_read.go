package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/entireio/entire-brain/internal/cli/internal/brainstore"
)

// readBrainManifestFile decodes the canonical manifest without a byte ceiling.
// It uses the same checked descriptor as bounded state readers, without needing
// the source repository or Git. encoding/json buffers a whole JSON value and
// the manifest is materialized in memory: this is NOT a bounded-memory reader.
// Rewinding this descriptor (never reopening the path) preserves schema-first
// checks and tolerant reads versus strict rewrites, including unknown fields.
func readBrainManifestFile(brainDir string, tolerateUnknown bool) (*exportManifest, int, bool, bool, error) {
	f, present, err := brainstore.OpenMemoryStateFileExpected(brainDir, exportManifestFileName, "brain manifest", nil)
	if err != nil {
		return nil, 0, false, present, fmt.Errorf("read brain manifest: %w", err)
	}
	if !present {
		return &exportManifest{SchemaVersion: brainManifestSchemaVersion}, 0, false, false, nil
	}
	defer f.Close()
	manifest, version, dropped, err := decodeBrainManifestFile(f, tolerateUnknown)
	return manifest, version, dropped, true, err
}

func decodeManifestPass(f *os.File, out any, strict bool) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	decoder := json.NewDecoder(f)
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return brainstore.ErrTrailingJSONData
	}
	return nil
}

func decodeBrainManifestFile(f *os.File, tolerateUnknown bool) (*exportManifest, int, bool, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := decodeManifestPass(f, &header, false); err != nil {
		return nil, 0, false, fmt.Errorf("parse brain manifest: %s: brain manifest cannot be parsed: %w", brainstore.StateCorruptCode, err)
	}
	version := header.SchemaVersion
	if version > brainManifestSchemaVersion {
		return nil, version, false, fmt.Errorf("%s: brain manifest schema version %d is newer than supported version %d; upgrade entire-brain to read it", brainstore.UnsupportedVersionCode, version, brainManifestSchemaVersion)
	}
	var manifest exportManifest
	err := decodeManifestPass(f, &manifest, true)
	dropped := false
	if tolerateUnknown && brainstore.IsUnknownFieldError(err) {
		manifest = exportManifest{}
		err = decodeManifestPass(f, &manifest, false)
		dropped = err == nil
	}
	if err != nil {
		// An unknown field is not damage, and saying "cannot be parsed" sends
		// the reader looking for corruption that is not there. The manifest is
		// entirely readable; a WRITER is refusing it, because re-encoding from
		// a struct that never saw the field would erase what another build
		// recorded. Readers pass tolerateUnknown and are unaffected.
		//
		// Observed cost of the old wording: a component sat failed for four
		// days reporting memory_state_corrupt, while the manifest was fine and
		// the condition had already gone away.
		//
		// The error CODE stays memory_state_corrupt deliberately. It is part of
		// the maintenance taxonomy with many consumers, so retyping this case
		// is a contract change for a human to make, not a side effect of
		// improving a sentence.
		if !tolerateUnknown && brainstore.IsUnknownFieldError(err) {
			return nil, version, false, fmt.Errorf(
				"parse brain manifest: %s: brain manifest carries a field this build does not know (%w); "+
					"the file is readable and is NOT damaged, but a writer will not rewrite it and silently drop that field. "+
					"Use a build that knows the field, or let a read-only command run instead",
				brainstore.StateCorruptCode, err)
		}
		return nil, version, false, fmt.Errorf("parse brain manifest: %s: brain manifest cannot be parsed: %w", brainstore.StateCorruptCode, err)
	}
	if version < brainManifestMinSchemaVersion {
		return nil, version, false, fmt.Errorf("%s: brain manifest declares schema version %d, older than the first supported version %d", brainstore.UnsupportedVersionCode, version, brainManifestMinSchemaVersion)
	}
	normalizeBrainManifest(&manifest)
	return &manifest, version, dropped, nil
}
