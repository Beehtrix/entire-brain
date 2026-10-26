package brainstore

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type versionedTestDoc struct {
	SchemaVersion int    `json:"schema_version"`
	Known         string `json:"known"`
}

// A document written by a different build of our own writer at a supported
// schema version carries fields this build does not declare. For a reader that
// is routine skew, not corruption.
func TestDecodeVersionedJSONBody_TolerantModeAcceptsUnknownFields(t *testing.T) {
	t.Parallel()

	var got versionedTestDoc
	if _, err := DecodeVersionedJSONBody([]byte(`{"schema_version":3,"known":"kept","retired_field":0}`), &got, true); err != nil {
		t.Fatalf("unknown field must not fail a reader: %v", err)
	}
	if got.Known != "kept" {
		t.Fatalf("known field lost during tolerant decode: %+v", got)
	}
}

// Writers keep the opposite answer: re-encoding from a struct that never saw a
// field erases it, so an unrecognised field must stop the write.
func TestDecodeVersionedJSONBody_StrictModeStillGuardsWriters(t *testing.T) {
	t.Parallel()

	var got versionedTestDoc
	_, err := DecodeVersionedJSONBody([]byte(`{"schema_version":3,"known":"kept","retired_field":0}`), &got, false)
	if !IsUnknownFieldError(err) {
		t.Fatalf("a writer must refuse a field it cannot round-trip, got: %v", err)
	}
}

// Tolerating unknown fields may not widen into tolerating malformed bytes or a
// second document, in either mode.
func TestDecodeStrictVersionedJSON_RejectsRealCorruption(t *testing.T) {
	t.Parallel()

	for name, data := range map[string]string{
		"malformed":     `{"schema_version":3,`,
		"trailing":      `{"schema_version":3}{"schema_version":3}`,
		"wrong type":    `{"schema_version":3,"known":123}`,
		"not an object": `["schema_version"]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got versionedTestDoc
			if _, err := DecodeStrictVersionedJSON([]byte(data), &got, 3, "test document"); err == nil {
				t.Fatalf("%s must be rejected", name)
			} else if !strings.Contains(err.Error(), StateCorruptCode) {
				t.Fatalf("%s must classify as %s, got: %v", name, StateCorruptCode, err)
			}

			var tolerant versionedTestDoc
			if _, err := DecodeVersionedJSONBody([]byte(data), &tolerant, true); err == nil {
				t.Fatalf("%s must be rejected by the tolerant reader too", name)
			}
		})
	}
}

// A newer schema version keeps its own distinct, non-corrupt classification.
func TestDecodeStrictVersionedJSON_NewerVersionStillUnsupported(t *testing.T) {
	t.Parallel()

	var got versionedTestDoc
	_, err := DecodeStrictVersionedJSON([]byte(`{"schema_version":9}`), &got, 3, "test document")
	if err == nil || !strings.Contains(err.Error(), UnsupportedVersionCode) {
		t.Fatalf("newer schema must stay %s, got: %v", UnsupportedVersionCode, err)
	}
}

// The tolerant retry re-decodes into a target the aborted strict pass already
// touched. Composite fields must come back exactly as a single clean decode
// would leave them, with nothing merged in from the first attempt.
func TestDecodeVersionedJSONBody_RetryDoesNotMergeIntoPartialTarget(t *testing.T) {
	t.Parallel()

	type doc struct {
		SchemaVersion int               `json:"schema_version"`
		Items         []string          `json:"items"`
		Labels        map[string]string `json:"labels"`
	}

	var got, want doc
	if _, err := DecodeVersionedJSONBody([]byte(`{"schema_version":3,"items":["a","b"],"labels":{"k":"v"},"retired_field":1}`), &got, true); err != nil {
		t.Fatalf("unknown field must not fail: %v", err)
	}
	if _, err := DecodeVersionedJSONBody([]byte(`{"schema_version":3,"items":["a","b"],"labels":{"k":"v"}}`), &want, true); err != nil {
		t.Fatalf("clean document must not fail: %v", err)
	}
	if len(got.Items) != len(want.Items) || len(got.Labels) != len(want.Labels) {
		t.Fatalf("retry merged into a partial target: got %+v, want %+v", got, want)
	}
	for i := range want.Items {
		if got.Items[i] != want.Items[i] {
			t.Fatalf("retry merged into a partial target: got %+v, want %+v", got, want)
		}
	}
}

// Trailing data keeps its own classification rather than being folded into the
// generic parse failure, because callers report it differently.
func TestDecodeVersionedJSONBody_TrailingDataIsDistinct(t *testing.T) {
	t.Parallel()

	var got versionedTestDoc
	if _, err := DecodeVersionedJSONBody([]byte(`{"schema_version":3} {"schema_version":3}`), &got, true); !errors.Is(err, ErrTrailingJSONData) {
		t.Fatalf("want ErrTrailingJSONData, got: %v", err)
	}
}

// encoding/json exposes no typed error for an unknown field, so the tolerant
// path discriminates on the stdlib's message. Pin it: if the stdlib ever rewords
// it, this fails loudly here instead of silently re-bricking every brain.
func TestUnknownFieldErrorTextIsStillStdlibContract(t *testing.T) {
	t.Parallel()

	// Nested targets too: the fallback must recognise the error wherever in the
	// document the unknown field sits, or a manifest like ours (the retired
	// field lives under sources.seed.history_coverage) is still called corrupt.
	for name, body := range map[string]string{
		"top level": `{"nope":1}`,
		"nested":    `{"nested":{"nope":1}}`,
		"deep":      `{"nested":{"nope":{"a":[1,2]}}}`,
	} {
		var target struct {
			Nested struct {
				Known string `json:"known"`
			} `json:"nested"`
		}
		decoder := json.NewDecoder(strings.NewReader(body))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&target)
		if err == nil {
			t.Fatalf("%s: expected an unknown-field error from the stdlib decoder", name)
		}
		if !IsUnknownFieldError(err) {
			t.Fatalf("%s: stdlib unknown-field error text changed; IsUnknownFieldError no longer matches it: %v", name, err)
		}
	}
}

// The tolerant reader must say when it dropped fields, so status and doctor can
// recommend a refresh rather than quietly narrowing another build's manifest.
func TestDecodeVersionedJSONBody_ReportsDroppedUnknownFields(t *testing.T) {
	t.Parallel()

	var got versionedTestDoc
	dropped, err := DecodeVersionedJSONBody([]byte(`{"schema_version":3,"known":"kept","retired_field":0}`), &got, true)
	if err != nil || !dropped {
		t.Fatalf("dropped=%v err=%v, want dropped with no error", dropped, err)
	}

	var clean versionedTestDoc
	dropped, err = DecodeVersionedJSONBody([]byte(`{"schema_version":3,"known":"kept"}`), &clean, true)
	if err != nil || dropped {
		t.Fatalf("dropped=%v err=%v, want no drop and no error", dropped, err)
	}
}
