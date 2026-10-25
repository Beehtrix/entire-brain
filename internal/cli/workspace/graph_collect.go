package workspace

import (
	"database/sql"
	"path/filepath"
	"strings"
)

func CollectExternalContracts(db *sql.DB, repoKey string, out map[string]*ExternalContractAggregate) error {
	rows, err := db.Query(`
SELECT CASE
    WHEN r.from_id LIKE 'external:%' THEN r.from_id
    ELSE r.to_id
  END AS endpoint,
  r.type,
  CASE
    WHEN r.from_id LIKE 'external:%' THEN r.to_id
    ELSE r.from_id
  END AS local_id,
  CASE
    WHEN r.from_id LIKE 'external:%' THEN 'from_endpoint'
    ELSE 'to_endpoint'
  END AS direction,
  COALESCE(s.kind, '') AS kind,
  COALESCE(s.name, '') AS name,
  COALESCE(s.qualified_name, '') AS qualified_name,
  COALESCE(s.file_path, '') AS file_path,
  COUNT(*) AS c
FROM relations r
LEFT JOIN symbols s ON s.id = CASE
  WHEN r.from_id LIKE 'external:%' THEN r.to_id
  ELSE r.from_id
END
WHERE r.from_id LIKE 'external:%' OR r.to_id LIKE 'external:%'
GROUP BY endpoint, r.type, local_id, direction, kind, name, qualified_name, file_path
ORDER BY endpoint, r.type, local_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var endpoint, typ, localID, direction, kind, name, qualifiedName, filePath string
		var count int
		if err := rows.Scan(&endpoint, &typ, &localID, &direction, &kind, &name, &qualifiedName, &filePath, &count); err != nil {
			return err
		}
		key := typ + "\x00" + endpoint
		aggregate := out[key]
		if aggregate == nil {
			aggregate = &ExternalContractAggregate{Endpoint: endpoint, Type: typ, RepoCounts: map[string]int{}}
			out[key] = aggregate
		}
		aggregate.RepoCounts[repoKey] += count
		if strings.HasPrefix(localID, "external:") {
			continue
		}
		aggregate.Participants = append(aggregate.Participants, SymbolRef{
			RepoKey:       repoKey,
			ID:            localID,
			Kind:          kind,
			Name:          name,
			QualifiedName: qualifiedName,
			FilePath:      filePath,
			Direction:     direction,
			Count:         count,
		})
	}
	return rows.Err()
}

func CollectImportRefs(db *sql.DB, repoKey string) ([]ImportRef, error) {
	rows, err := db.Query(`
SELECT CASE
    WHEN r.from_id LIKE 'external:import:%' THEN r.from_id
    ELSE r.to_id
  END AS endpoint,
  CASE
    WHEN r.from_id LIKE 'external:import:%' THEN r.to_id
    ELSE r.from_id
  END AS local_id,
  COALESCE(s.kind, '') AS kind,
  COALESCE(s.name, '') AS name,
  COALESCE(s.qualified_name, '') AS qualified_name,
  COALESCE(s.file_path, '') AS file_path,
  COUNT(*) AS c
FROM relations r
LEFT JOIN symbols s ON s.id = CASE
  WHEN r.from_id LIKE 'external:import:%' THEN r.to_id
  ELSE r.from_id
END
WHERE r.type = 'IMPORTS'
  AND (r.from_id LIKE 'external:import:%' OR r.to_id LIKE 'external:import:%')
GROUP BY endpoint, local_id, kind, name, qualified_name, file_path
ORDER BY endpoint, local_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []ImportRef
	for rows.Next() {
		var endpoint, localID, kind, name, qualifiedName, filePath string
		var count int
		if err := rows.Scan(&endpoint, &localID, &kind, &name, &qualifiedName, &filePath, &count); err != nil {
			return nil, err
		}
		spec := strings.TrimPrefix(endpoint, "external:import:")
		if spec == "" || strings.HasPrefix(localID, "external:") {
			continue
		}
		refs = append(refs, ImportRef{
			Spec: spec,
			Source: SymbolRef{
				RepoKey:       repoKey,
				ID:            localID,
				Kind:          kind,
				Name:          name,
				QualifiedName: qualifiedName,
				FilePath:      filePath,
				Direction:     "imports_external",
				Count:         count,
			},
			Count: count,
		})
	}
	if refs == nil {
		refs = []ImportRef{}
	}
	return refs, rows.Err()
}

func CollectExternalSymbolRefs(db *sql.DB, repoKey string) ([]ExternalSymbolRef, error) {
	rows, err := db.Query(`
SELECT CASE
    WHEN r.from_id LIKE 'external:symbol:%' THEN r.from_id
    ELSE r.to_id
  END AS endpoint,
  r.type,
  CASE
    WHEN r.from_id LIKE 'external:symbol:%' THEN r.to_id
    ELSE r.from_id
  END AS local_id,
  COALESCE(s.kind, '') AS kind,
  COALESCE(s.name, '') AS name,
  COALESCE(s.qualified_name, '') AS qualified_name,
  COALESCE(s.file_path, '') AS file_path,
  COUNT(*) AS c
FROM relations r
LEFT JOIN symbols s ON s.id = CASE
  WHEN r.from_id LIKE 'external:symbol:%' THEN r.to_id
  ELSE r.from_id
END
WHERE r.from_id LIKE 'external:symbol:%' OR r.to_id LIKE 'external:symbol:%'
GROUP BY endpoint, r.type, local_id, kind, name, qualified_name, file_path
ORDER BY endpoint, r.type, local_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []ExternalSymbolRef
	for rows.Next() {
		var endpoint, typ, localID, kind, name, qualifiedName, filePath string
		var count int
		if err := rows.Scan(&endpoint, &typ, &localID, &kind, &name, &qualifiedName, &filePath, &count); err != nil {
			return nil, err
		}
		spec := strings.TrimPrefix(endpoint, "external:symbol:")
		if spec == "" || strings.HasPrefix(localID, "external:") {
			continue
		}
		refs = append(refs, ExternalSymbolRef{
			Spec: spec,
			Type: typ,
			Source: SymbolRef{
				RepoKey:       repoKey,
				ID:            localID,
				Kind:          kind,
				Name:          name,
				QualifiedName: qualifiedName,
				FilePath:      filePath,
				Direction:     "external_symbol_source",
				Count:         count,
			},
			Count: count,
		})
	}
	if refs == nil {
		refs = []ExternalSymbolRef{}
	}
	return refs, rows.Err()
}

func CollectTargetCandidates(db *sql.DB, repoKey string) ([]SymbolRef, error) {
	rows, err := db.Query(`
SELECT id, kind, name, qualified_name, file_path
FROM symbols
ORDER BY file_path, kind, qualified_name, name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []SymbolRef
	for rows.Next() {
		var candidate SymbolRef
		candidate.RepoKey = repoKey
		if err := rows.Scan(&candidate.ID, &candidate.Kind, &candidate.Name, &candidate.QualifiedName, &candidate.FilePath); err != nil {
			return nil, err
		}
		candidate.Direction = "import_target_candidate"
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	fileRows, err := db.Query(`SELECT path FROM files WHERE trim(path) <> '' ORDER BY path`)
	if err != nil {
		return nil, err
	}
	defer fileRows.Close()
	for fileRows.Next() {
		var path string
		if err := fileRows.Scan(&path); err != nil {
			return nil, err
		}
		candidates = append(candidates, SymbolRef{
			RepoKey:   repoKey,
			ID:        repoKey + ":file:" + filepath.ToSlash(path),
			Kind:      "file",
			Name:      filepath.Base(path),
			FilePath:  filepath.ToSlash(path),
			Direction: "import_target_candidate",
		})
	}
	if candidates == nil {
		candidates = []SymbolRef{}
	}
	return candidates, fileRows.Err()
}
