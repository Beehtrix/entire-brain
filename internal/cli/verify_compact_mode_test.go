package cli

import (
	"strings"
	"testing"

	"github.com/entireio/entire-brain/internal/cli/internal/factstore"
	bmanifest "github.com/entireio/entire-brain/internal/cli/internal/manifest"
)

// addLocalCompactCheckpoint scripts a v1 checkpoint whose session carries BOTH
// the raw full.jsonl and the compact transcript.jsonl the CLI stores beside it.
func (f verifyFixture) addLocalCompactCheckpoint(t *testing.T, checkpointID, sessionID, turnID, rawTranscript, compactTranscript string) {
	t.Helper()
	dir := checkpointPath(checkpointID)
	rootMetadata := dir + "/metadata.json"
	sessionMetadata := dir + "/0/metadata.json"
	rawPath := dir + "/0/" + v1TranscriptFileName
	compactPath := dir + "/0/" + v1CompactTranscriptFileName
	f.runner.responses[fakeCommandKey("git", "ls-tree", "-r", "--name-only", v1MainRef)] = fakeCommandResponse{
		stdout: strings.Join([]string{rootMetadata, sessionMetadata, rawPath, compactPath}, "\n") + "\n",
	}
	f.runner.responses[fakeCommandKey("git", "cat-file", "-p", v1MainRef+":"+rootMetadata)] = fakeCommandResponse{
		stdout: `{"branch":"main","sessions":[{"metadata":"` + sessionMetadata + `","transcript":"` + rawPath + `","compact_transcript":"` + compactPath + `"}]}`,
	}
	f.runner.responses[fakeCommandKey("git", "cat-file", "-p", v1MainRef+":"+sessionMetadata)] = fakeCommandResponse{
		stdout: `{"session_id":"` + sessionID + `","branch":"main","turn_id":"` + turnID + `"}`,
	}
	f.runner.responses[fakeCommandKey("git", "cat-file", "-p", v1MainRef+":"+rawPath)] = fakeCommandResponse{stdout: rawTranscript}
	f.runner.responses[fakeCommandKey("git", "cat-file", "-p", v1MainRef+":"+compactPath)] = fakeCommandResponse{stdout: compactTranscript}
}

// A compact-mode export carries the checkpoint's compact transcript, so verify
// must compare it against the local COMPACT flavor, not raw full.jsonl.
// Regression for the customer-reported cycle (COR-2289): default (compact)
// refresh marked every session-anchored memory stale with "exported transcript
// differs from local checkpoint transcript"; only --raw exports verified.
func TestVerifyCompactExportComparesCompactCheckpointTranscript(t *testing.T) {
	f := newVerifyFixture(t)
	const checkpointID = "aaa111aaa111"
	const transcriptRel = "sessions/main/session.jsonl"
	raw := `{"type":"user","message":{"role":"user","content":"Use the local checkpoint reader."}}` + "\n"
	compact := etfTranscript()

	f.writeBrainFile(t, transcriptRel, compact)
	manifest := exportManifest{
		SchemaVersion:  bmanifest.SchemaVersion,
		GeneratedAt:    f.now,
		RepoRoot:       f.repoDir,
		RepoKey:        f.storage.Key,
		DefaultBranch:  "main",
		TranscriptMode: "compact",
		Scope:          exportScopeAll,
		Sessions: []exportSession{{
			SessionID:        "sess1",
			Branch:           "main",
			LatestCheckpoint: checkpointID,
			SessionIndex:     0,
			CreatedAt:        f.now,
			TurnID:           "turn1",
			TranscriptPath:   transcriptRel,
		}},
		CheckpointsScanned: 1,
	}
	if err := writeBrainManifestAndReadme(f.brainDir, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	f.addLocalCompactCheckpoint(t, checkpointID, "sess1", "turn1", raw, compact)
	fact := verifyFactFixture("fact:compact", "Compact exports verify against the compact transcript.", "main", factstore.OriginDistilled, factStatusActive, f.now, []factAnchor{{
		SessionID: "sess1", CheckpointID: checkpointID, TurnID: "turn1", Transcript: transcriptRel, Line: 1,
	}})
	f.writeFacts(t, "main", []factRecord{fact})

	cmd := NewRootCommand(f.opts)
	out, err := execute(t, cmd, "verify", "fact:compact", "--json")
	if err != nil {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	report := parseVerifyReport(t, out)
	if report.Summary.Verified != 1 || report.Results[0].Verdict != verifyVerdictVerified {
		t.Fatalf("expected verified report, got %+v", report)
	}
}
