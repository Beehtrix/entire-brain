package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/entireio/entire-brain/internal/cli/internal/base"
	"github.com/spf13/cobra"
)

const graphTraceFile = "semantic/traces/observations.json"
const graphTraceMaxBytes = 64 << 20

type graphTraceObservation struct {
	ImportedAt   string `json:"imported_at"`
	SourcePath   string `json:"source_path"`
	FromID       string `json:"from_id"`
	ToID         string `json:"to_id"`
	ObservedType string `json:"observed_type"`
	Matched      int    `json:"matched_static_edge"`
}

func readGraphTraces(brainDir string) ([]graphTraceObservation, error) {
	if brainDir == "" {
		return nil, nil
	}
	if err := rejectExistingSymlinkPathComponents(brainDir, graphTraceFile); err != nil {
		return nil, err
	}
	data, err := base.SafeReadFile(filepath.Join(brainDir, graphTraceFile), graphTraceMaxBytes)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []graphTraceObservation
	if err = json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
func writeGraphTraces(brainDir string, rows []graphTraceObservation) error {
	if err := rejectExistingSymlinkPathComponents(brainDir, graphTraceFile); err != nil {
		return err
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	if len(data)+1 > graphTraceMaxBytes {
		return errors.New("runtime trace observations exceed 64 MiB; existing observations retained")
	}
	return writeFileAtomic(filepath.Join(brainDir, graphTraceFile), append(data, '\n'), 0600)
}

func loadGraphTraceTable(db *sql.DB, brainDir string) error {
	_, err := db.Exec("CREATE TEMP TABLE runtime_traces (id INTEGER PRIMARY KEY, imported_at TEXT NOT NULL, source_path TEXT NOT NULL, from_id TEXT NOT NULL, to_id TEXT NOT NULL, observed_type TEXT, matched_static_edge INTEGER NOT NULL)")
	if err != nil {
		return err
	}
	rows, err := readGraphTraces(brainDir)
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare("INSERT INTO runtime_traces(imported_at,source_path,from_id,to_id,observed_type,matched_static_edge) VALUES(?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, o := range rows {
		if _, err = stmt.Exec(o.ImportedAt, o.SourcePath, o.FromID, o.ToID, o.ObservedType, o.Matched); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ingestGraphTraces(cmd *cobra.Command, opts Options, ingestOpts semanticTraceIngestOptions, path string) error {
	traces, err := readRuntimeTraces(path)
	if err != nil {
		return err
	}
	env, err := loadSemanticEnv(cmd, opts)
	if err != nil {
		return err
	}
	unlock, err := acquireSemanticIndexLock(env.BrainDir)
	if err != nil {
		return err
	}
	defer unlock()
	if err = requireCurrentBrainLayout(env.BrainDir); err != nil {
		return err
	}
	existing, err := readGraphTraces(env.BrainDir)
	if err != nil {
		return err
	}
	handle, err := semanticStorePath(env)
	if err != nil {
		return err
	}
	db, err := openSemanticReadDB(handle)
	if err != nil {
		return err
	}
	defer db.Close()
	report := semanticTraceIngestReport{ImportedAt: opts.Now().UTC(), Path: path, Total: len(traces)}
	for _, t := range traces {
		matched, err := runtimeTraceMatchesStaticEdge(db, t)
		if err != nil {
			return err
		}
		n := 0
		if matched {
			n = 1
			report.Matched++
		}
		existing = append(existing, graphTraceObservation{ImportedAt: report.ImportedAt.Format(time.RFC3339Nano), SourcePath: graphTraceFile, FromID: t.From, ToID: t.To, ObservedType: t.Type, Matched: n})
	}
	report.Unmatched = report.Total - report.Matched
	if err = writeGraphTraces(env.BrainDir, existing); err != nil {
		return err
	}
	report.Path = graphTraceFile
	if ingestOpts.json {
		return writeJSON(cmd, report)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "ingested runtime traces: %d\nmatched_static_edges: %d\nartifact: %s\n", report.Total, report.Matched, report.Path)
	return nil
}
