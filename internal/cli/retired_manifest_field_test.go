package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The failure these pin was found on a real machine, not imagined: a field
// RETIRED from the code without a schema bump leaves every manifest the older
// build wrote unwritable by this one. Writers refuse it on purpose -- rewriting
// from a struct that never saw the field would erase what another build
// recorded -- so the condition is permanent, and the worker relaunched against
// it on a 1m..30m chain for two hours per repository, logging only
// "prepass_failed".

// The exact field and shape taken from a manifest on disk.
func writeManifestWithRetiredField(t *testing.T, dir string) {
	t.Helper()
	body := map[string]any{
		"schema_version": brainManifestSchemaVersion,
		"repo_key":       "local/demo-1234",
		"generated_at":   "2026-10-11T22:00:00Z",
		"suppressed_warnings": []string{
			"checkpoint ref refs/remotes/origin/entire/checkpoints/v1 unreadable for 0a2fd94192c1",
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, exportManifestFileName), data, 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func TestAWriterRefusingARetiredFieldNamesTheRemedy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifestWithRetiredField(t, dir)

	_, err := loadBrainManifestForReplace(dir)
	if err == nil {
		t.Fatal("a manifest a writer cannot round-trip must be refused, or it silently erases another build's state")
	}
	msg := err.Error()
	if !strings.Contains(msg, "refresh --force") {
		t.Fatalf("the refusal names no way out, which is how this ran for two hours unattended: %s", msg)
	}
}

// Readers must still be able to open it. Availability is the reader's concern;
// not destroying state is the writer's -- if this regressed, a single retired
// field would take every query down with it.
func TestAReaderStillOpensAManifestWithARetiredField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifestWithRetiredField(t, dir)

	manifest, err := loadBrainManifest(dir)
	if err != nil {
		t.Fatalf("readers must tolerate a retired field: %v", err)
	}
	if manifest == nil || manifest.RepoKey != "local/demo-1234" {
		t.Fatalf("the manifest decoded but lost its contents: %+v", manifest)
	}
}

func TestRetiredManifestFieldIsRecognisedAsDurable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifestWithRetiredField(t, dir)
	_, err := loadBrainManifestForReplace(dir)
	if err == nil {
		t.Fatal("fixture did not produce the failure, so this guard would pass on any change")
	}

	if !errRetiredManifestField(err) {
		t.Fatalf("a retired field is durable and must stop the relaunch chain: %v", err)
	}
	// The exact string the worker sees, through the delta-export path that
	// wraps it -- that wrapper is why the check cannot rely on the typed error.
	wrapped := wrapTestError("delta session export", err)
	if !errRetiredManifestField(wrapped) {
		t.Fatalf("the wrapped form the worker actually receives was not recognised: %v", wrapped)
	}
}

// A transient failure must NOT be classified as durable, or one bad run stops
// the brain updating until something else happens to restart the chain.
func TestAnOrdinaryFailureStillRetries(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		wrapTestError("delta session export", os.ErrDeadlineExceeded),
		wrapTestError("delta session export", os.ErrPermission),
	} {
		if errRetiredManifestField(err) {
			t.Fatalf("a transient failure was treated as durable, so it would never be retried: %v", err)
		}
	}
	if errRetiredManifestField(nil) {
		t.Fatal("no error is not a durable failure")
	}
}

func TestTheRemedyMessageKeepsTheOriginalCause(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeManifestWithRetiredField(t, dir)
	_, err := loadBrainManifestForReplace(dir)

	msg := retiredManifestFieldMessage(err)
	if !strings.Contains(msg, "suppressed_warnings") {
		t.Fatalf("the remedy replaced the cause instead of adding to it: %s", msg)
	}
	if !strings.Contains(msg, "refresh --force") {
		t.Fatalf("the message states a problem and no action: %s", msg)
	}
	if retiredManifestFieldMessage(nil) != "" {
		t.Fatal("no error must produce no message")
	}
}
