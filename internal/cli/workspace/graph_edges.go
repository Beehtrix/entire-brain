package workspace

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func GraphContracts(index map[string]*ExternalContractAggregate, limit int) []Contract {
	var contracts []Contract
	for _, aggregate := range index {
		if len(aggregate.RepoCounts) < 2 {
			continue
		}
		repos := make([]string, 0, len(aggregate.RepoCounts))
		count := 0
		for repo, n := range aggregate.RepoCounts {
			repos = append(repos, repo)
			count += n
		}
		sort.Strings(repos)
		contracts = append(contracts, Contract{Endpoint: aggregate.Endpoint, Type: aggregate.Type, Repos: repos, Count: count})
	}
	sort.Slice(contracts, func(i, j int) bool {
		if contracts[i].Count == contracts[j].Count {
			if contracts[i].Type == contracts[j].Type {
				return contracts[i].Endpoint < contracts[j].Endpoint
			}
			return contracts[i].Type < contracts[j].Type
		}
		return contracts[i].Count > contracts[j].Count
	})
	if len(contracts) > limit {
		contracts = contracts[:limit]
	}
	if contracts == nil {
		return []Contract{}
	}
	return contracts
}

func GraphCrossEdges(index map[string]*ExternalContractAggregate, limit int) []CrossEdge {
	var edges []CrossEdge
	for _, aggregate := range index {
		if len(aggregate.RepoCounts) < 2 || len(aggregate.Participants) < 2 {
			continue
		}
		participants := append([]SymbolRef(nil), aggregate.Participants...)
		sort.Slice(participants, func(i, j int) bool {
			if participants[i].RepoKey == participants[j].RepoKey {
				return participants[i].ID < participants[j].ID
			}
			return participants[i].RepoKey < participants[j].RepoKey
		})
		for i := 0; i < len(participants); i++ {
			for j := i + 1; j < len(participants); j++ {
				if participants[i].RepoKey == participants[j].RepoKey {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     aggregate.Endpoint,
					Type:         aggregate.Type,
					FromRepo:     participants[i].RepoKey,
					ToRepo:       participants[j].RepoKey,
					FromSymbol:   participants[i],
					ToSymbol:     participants[j],
					SharedCount:  participants[i].Count + participants[j].Count,
					RelationKind: "shared_external_contract",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func GraphImportCrossEdges(indexes []RepoGraphIndex, limit int) []CrossEdge {
	var edges []CrossEdge
	// Import prefixes depend only on a repo key, so compute them once per repo
	// instead of re-deriving them for every (fromRepo, import) pair in the loop.
	prefixesByRepo := make([][]string, len(indexes))
	for i, repo := range indexes {
		prefixesByRepo[i] = repoImportPrefixes(repo.RepoKey)
	}
	for _, fromRepo := range indexes {
		for _, imp := range fromRepo.Imports {
			normalizedSpec := normalizeImportSpec(imp.Spec)
			for j, toRepo := range indexes {
				if fromRepo.RepoKey == toRepo.RepoKey {
					continue
				}
				subpath, ok := importMatchesPrefixes(normalizedSpec, prefixesByRepo[j])
				if !ok {
					continue
				}
				target, ok := ImportTargetCandidate(toRepo.Candidates, subpath)
				if !ok {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     "external:import:" + imp.Spec,
					Type:         "IMPORTS",
					FromRepo:     fromRepo.RepoKey,
					ToRepo:       toRepo.RepoKey,
					FromSymbol:   imp.Source,
					ToSymbol:     target,
					SharedCount:  imp.Count,
					RelationKind: "cross_repo_import_candidate",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func GraphRouteCallCrossEdges(index map[string]*ExternalContractAggregate, limit int) []CrossEdge {
	handlersByEndpoint := map[string][]SymbolRef{}
	callersByEndpoint := map[string][]SymbolRef{}
	for _, aggregate := range index {
		if !strings.HasPrefix(aggregate.Endpoint, "external:route:") {
			continue
		}
		endpoint := canonicalRouteEndpoint(aggregate.Endpoint)
		switch aggregate.Type {
		case "HANDLES_ROUTE":
			handlersByEndpoint[endpoint] = append(handlersByEndpoint[endpoint], aggregate.Participants...)
		case "HTTP_CALLS":
			callersByEndpoint[endpoint] = append(callersByEndpoint[endpoint], aggregate.Participants...)
		}
	}
	var edges []CrossEdge
	for endpoint, callers := range callersByEndpoint {
		handlers := handlersByEndpoint[endpoint]
		if len(callers) == 0 || len(handlers) == 0 {
			continue
		}
		sortGraphSymbolRefs(callers)
		sortGraphSymbolRefs(handlers)
		for _, caller := range callers {
			for _, handler := range handlers {
				if caller.RepoKey == handler.RepoKey {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     endpoint,
					Type:         "CALLS",
					FromRepo:     caller.RepoKey,
					ToRepo:       handler.RepoKey,
					FromSymbol:   caller,
					ToSymbol:     handler,
					SharedCount:  caller.Count + handler.Count,
					RelationKind: "cross_repo_route_call",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func canonicalRouteEndpoint(endpoint string) string {
	const prefix = "external:route:"
	if !strings.HasPrefix(endpoint, prefix) {
		return endpoint
	}
	route := strings.TrimPrefix(endpoint, prefix)
	if route != "/" {
		route = strings.TrimRight(route, "/")
		if route == "" {
			route = "/"
		}
	}
	route = regexp.MustCompile(`\{[^}/]+\}`).ReplaceAllString(route, `{param}`)
	route = regexp.MustCompile(`<(?:(?:[A-Za-z_][A-Za-z0-9_]*):)?[A-Za-z_][A-Za-z0-9_]*>`).ReplaceAllString(route, `{param}`)
	route = regexp.MustCompile(`\[\[\.{0,3}[A-Za-z_][A-Za-z0-9_]*\]\]`).ReplaceAllString(route, `{param}`)
	route = regexp.MustCompile(`\[\.{0,3}[A-Za-z_][A-Za-z0-9_]*\]`).ReplaceAllString(route, `{param}`)
	route = regexp.MustCompile(`\*[A-Za-z_][A-Za-z0-9_]*`).ReplaceAllString(route, `{param}`)
	route = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`).ReplaceAllString(route, `{param}`)
	return prefix + route
}

func GraphGraphQLCrossEdges(index map[string]*ExternalContractAggregate, limit int) []CrossEdge {
	var edges []CrossEdge
	for _, aggregate := range index {
		if aggregate.Type != "HANDLES_GRAPHQL" || !strings.HasPrefix(aggregate.Endpoint, "external:graphql:") {
			continue
		}
		var targets []SymbolRef
		var operations []SymbolRef
		var schemaFields []SymbolRef
		var resolvers []SymbolRef
		for _, participant := range aggregate.Participants {
			if participant.Kind == "graphql_resolver" || participant.Kind == "graphql_schema_field" {
				targets = append(targets, participant)
				if participant.Kind == "graphql_schema_field" {
					schemaFields = append(schemaFields, participant)
				} else {
					resolvers = append(resolvers, participant)
				}
			} else {
				operations = append(operations, participant)
			}
		}
		sortGraphSymbolRefs(operations)
		sortGraphSymbolRefs(targets)
		sortGraphSymbolRefs(schemaFields)
		sortGraphSymbolRefs(resolvers)
		for _, operation := range operations {
			for _, target := range targets {
				if operation.RepoKey == target.RepoKey {
					continue
				}
				relationKind := "cross_repo_graphql_call"
				if target.Kind == "graphql_schema_field" {
					relationKind = "cross_repo_graphql_schema"
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     aggregate.Endpoint,
					Type:         "CALLS",
					FromRepo:     operation.RepoKey,
					ToRepo:       target.RepoKey,
					FromSymbol:   operation,
					ToSymbol:     target,
					SharedCount:  operation.Count + target.Count,
					RelationKind: relationKind,
				})
			}
		}
		for _, schemaField := range schemaFields {
			for _, resolver := range resolvers {
				if schemaField.RepoKey == resolver.RepoKey {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     aggregate.Endpoint,
					Type:         "CALLS",
					FromRepo:     schemaField.RepoKey,
					ToRepo:       resolver.RepoKey,
					FromSymbol:   schemaField,
					ToSymbol:     resolver,
					SharedCount:  schemaField.Count + resolver.Count,
					RelationKind: "cross_repo_graphql_schema_resolver",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func GraphChannelCrossEdges(index map[string]*ExternalContractAggregate, limit int) []CrossEdge {
	emittersByEndpoint := map[string][]SymbolRef{}
	listenersByEndpoint := map[string][]SymbolRef{}
	for _, aggregate := range index {
		if !strings.HasPrefix(aggregate.Endpoint, "external:channel:") {
			continue
		}
		switch aggregate.Type {
		case "EMITS":
			emittersByEndpoint[aggregate.Endpoint] = append(emittersByEndpoint[aggregate.Endpoint], aggregate.Participants...)
		case "LISTENS_ON":
			listenersByEndpoint[aggregate.Endpoint] = append(listenersByEndpoint[aggregate.Endpoint], aggregate.Participants...)
		}
	}
	var edges []CrossEdge
	for endpoint, emitters := range emittersByEndpoint {
		listeners := listenersByEndpoint[endpoint]
		if len(emitters) == 0 || len(listeners) == 0 {
			continue
		}
		sortGraphSymbolRefs(emitters)
		sortGraphSymbolRefs(listeners)
		for _, emitter := range emitters {
			for _, listener := range listeners {
				if emitter.RepoKey == listener.RepoKey {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     endpoint,
					Type:         "EMITS",
					FromRepo:     emitter.RepoKey,
					ToRepo:       listener.RepoKey,
					FromSymbol:   emitter,
					ToSymbol:     listener,
					SharedCount:  emitter.Count + listener.Count,
					RelationKind: "cross_repo_channel_flow",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func GraphResourceCrossEdges(index map[string]*ExternalContractAggregate, indexes []RepoGraphIndex, limit int) []CrossEdge {
	candidatesByRepo := map[string][]SymbolRef{}
	for _, repoIndex := range indexes {
		candidatesByRepo[repoIndex.RepoKey] = repoIndex.Candidates
	}
	var edges []CrossEdge
	for _, aggregate := range index {
		if aggregate.Type != "RESOURCE_DEPENDS_ON" {
			continue
		}
		kind, name, ok := kubernetesExternalResource(aggregate.Endpoint)
		if !ok {
			continue
		}
		for _, participant := range aggregate.Participants {
			for repoKey, candidates := range candidatesByRepo {
				if repoKey == participant.RepoKey {
					continue
				}
				target, ok := resourceTargetCandidate(candidates, kind, name)
				if !ok {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     aggregate.Endpoint,
					Type:         aggregate.Type,
					FromRepo:     participant.RepoKey,
					ToRepo:       repoKey,
					FromSymbol:   participant,
					ToSymbol:     target,
					SharedCount:  participant.Count,
					RelationKind: "cross_repo_resource_candidate",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func GraphExternalSymbolCrossEdges(indexes []RepoGraphIndex, limit int) []CrossEdge {
	var edges []CrossEdge
	for _, fromRepo := range indexes {
		for _, ref := range fromRepo.ExternalSymbols {
			for _, toRepo := range indexes {
				if fromRepo.RepoKey == toRepo.RepoKey {
					continue
				}
				target, ok := ExternalSymbolTarget(toRepo.Candidates, ref.Spec)
				if !ok {
					continue
				}
				edges = keepGraphEdge(edges, limit, CrossEdge{
					Endpoint:     "external:symbol:" + ref.Spec,
					Type:         ref.Type,
					FromRepo:     fromRepo.RepoKey,
					ToRepo:       toRepo.RepoKey,
					FromSymbol:   ref.Source,
					ToSymbol:     target,
					SharedCount:  ref.Count,
					RelationKind: "cross_repo_external_symbol",
				})
			}
		}
	}
	SortGraphCrossEdges(edges)
	if len(edges) > limit {
		edges = edges[:limit]
	}
	if edges == nil {
		return []CrossEdge{}
	}
	return edges
}

func sortGraphSymbolRefs(refs []SymbolRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].RepoKey != refs[j].RepoKey {
			return refs[i].RepoKey < refs[j].RepoKey
		}
		if refs[i].FilePath != refs[j].FilePath {
			return refs[i].FilePath < refs[j].FilePath
		}
		if refs[i].QualifiedName != refs[j].QualifiedName {
			return refs[i].QualifiedName < refs[j].QualifiedName
		}
		if refs[i].Name != refs[j].Name {
			return refs[i].Name < refs[j].Name
		}
		return refs[i].ID < refs[j].ID
	})
}

func importMatchesRepo(spec, repoKey string) (string, bool) {
	return importMatchesPrefixes(normalizeImportSpec(spec), repoImportPrefixes(repoKey))
}

// normalizeImportSpec canonicalizes an import spec (slash separators,
// trimmed, `::` flattened to `/`) so it can be matched against repo prefixes.
func normalizeImportSpec(spec string) string {
	spec = strings.Trim(strings.TrimSpace(filepath.ToSlash(spec)), "/")
	return strings.ReplaceAll(spec, "::", "/")
}

// importMatchesPrefixes reports whether a normalized import spec
// resolves under one of a repo's precomputed import prefixes, returning the
// matched subpath. Keeping this separate from repoImportPrefixes lets
// callers hoist the per-repo prefix computation out of hot nested loops.
func importMatchesPrefixes(normalizedSpec string, prefixes []string) (string, bool) {
	for _, prefix := range prefixes {
		if normalizedSpec == prefix {
			return "", true
		}
		if strings.HasPrefix(normalizedSpec, prefix+"/") {
			return strings.TrimPrefix(normalizedSpec, prefix+"/"), true
		}
		if strings.HasPrefix(normalizedSpec, prefix+".") {
			return strings.ReplaceAll(strings.TrimPrefix(normalizedSpec, prefix+"."), ".", "/"), true
		}
	}
	return "", false
}

func repoImportPrefixes(repoKey string) []string {
	repoKey = strings.Trim(strings.TrimSpace(filepath.ToSlash(repoKey)), "/")
	parts := strings.Split(repoKey, "/")
	prefixes := []string{repoKey}
	if len(parts) >= 3 && parts[0] == "gh" {
		ownerRepo := strings.Join(parts[1:3], "/")
		prefixes = append(prefixes, "github.com/"+ownerRepo, ownerRepo, "@"+ownerRepo)
		if len(parts) >= 5 && parts[3] == "packages" {
			pkg := strings.Join(parts[4:], "/")
			prefixes = append(prefixes, "github.com/"+ownerRepo+"/packages/"+pkg, ownerRepo+"/packages/"+pkg, "@"+parts[1]+"/"+pkg)
			for _, alias := range packageImportAliases("gh", pkg) {
				prefixes = append(prefixes, "github.com/"+ownerRepo+"/packages/"+alias, ownerRepo+"/packages/"+alias, "@"+parts[1]+"/"+alias)
			}
		}
	}
	if len(parts) >= 2 {
		switch parts[0] {
		case "cargo", "composer", "gem", "gomod", "npm", "nuget", "pypi":
			pkg := strings.Join(parts[1:], "/")
			prefixes = append(prefixes, pkg)
			prefixes = append(prefixes, packageImportAliases(parts[0], pkg)...)
		case "maven":
			groupArtifact := strings.Join(parts[1:], "/")
			prefixes = append(prefixes, groupArtifact, strings.ReplaceAll(groupArtifact, "/", "."))
		}
	}
	sort.Slice(prefixes, func(i, j int) bool {
		if len(prefixes[i]) != len(prefixes[j]) {
			return len(prefixes[i]) > len(prefixes[j])
		}
		return prefixes[i] < prefixes[j]
	})
	deduped := prefixes[:0]
	seen := map[string]bool{}
	for _, prefix := range prefixes {
		if prefix == "" || seen[prefix] {
			continue
		}
		deduped = append(deduped, prefix)
		seen[prefix] = true
	}
	return deduped
}

func packageImportAliases(ecosystem, pkg string) []string {
	pkg = strings.Trim(strings.TrimSpace(filepath.ToSlash(pkg)), "/")
	if pkg == "" {
		return nil
	}
	var aliases []string
	addHyphenUnderscore := func(value string) {
		if strings.Contains(value, "-") {
			aliases = append(aliases, strings.ReplaceAll(value, "-", "_"))
		}
		if strings.Contains(value, "_") {
			aliases = append(aliases, strings.ReplaceAll(value, "_", "-"))
		}
	}
	switch ecosystem {
	case "cargo", "gh", "pypi":
		addHyphenUnderscore(pkg)
	case "gem":
		// RubyGems often use hyphenated package names and underscored require
		// paths, while existing exact gem prefixes remain preferred.
		if strings.Contains(pkg, "-") {
			aliases = append(aliases, strings.ReplaceAll(pkg, "-", "_"))
		}
	}
	return aliases
}

func kubernetesExternalResource(endpoint string) (string, string, bool) {
	endpoint = strings.TrimSpace(endpoint)
	for _, parser := range []struct {
		prefix     string
		kindPrefix string
	}{
		{prefix: "external:config:kubernetes/"},
		{prefix: "external:config:compose/", kindPrefix: "compose."},
	} {
		if !strings.HasPrefix(endpoint, parser.prefix) {
			continue
		}
		rest := strings.Trim(strings.TrimPrefix(endpoint, parser.prefix), "/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 && len(parts) != 3 {
			return "", "", false
		}
		kind := strings.TrimSpace(parts[0])
		name := strings.TrimSpace(parts[len(parts)-1])
		if kind == "" || name == "" {
			return "", "", false
		}
		if len(parts) == 3 {
			namespace := strings.TrimSpace(parts[1])
			if namespace == "" {
				return "", "", false
			}
			name = namespace + "/" + name
		}
		return parser.kindPrefix + kind, name, true
	}
	return "", "", false
}

func resourceTargetCandidate(candidates []SymbolRef, kind, name string) (SymbolRef, bool) {
	expected := resourceTargetNames(kind, name)
	for _, candidate := range candidates {
		if !strings.EqualFold(candidate.Kind, "resource") {
			continue
		}
		for _, value := range []string{candidate.Name, candidate.QualifiedName} {
			if !expected[strings.ToLower(strings.TrimSpace(value))] {
				continue
			}
			candidate.Direction = "external_resource_target"
			return candidate, true
		}
	}
	return SymbolRef{}, false
}

func resourceTargetNames(kind, name string) map[string]bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	name = strings.ToLower(strings.Trim(strings.TrimSpace(name), "/"))
	out := map[string]bool{}
	if kind == "" || name == "" {
		return out
	}
	out[kind+"."+strings.ReplaceAll(name, "/", ".")] = true
	if strings.Contains(name, "/") {
		_, shortName, ok := strings.Cut(name, "/")
		if ok && shortName != "" {
			out[kind+"."+shortName] = true
		}
	}
	return out
}

func ExternalSymbolTarget(candidates []SymbolRef, spec string) (SymbolRef, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return SymbolRef{}, false
	}
	var matches []SymbolRef
	for _, candidate := range candidates {
		if candidate.ID == "" || strings.EqualFold(candidate.Kind, "file") {
			continue
		}
		if externalSymbolMatchesCandidate(spec, candidate) {
			candidate.Direction = "external_symbol_target"
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 0 {
		return SymbolRef{}, false
	}
	sort.Slice(matches, func(i, j int) bool {
		leftRank := externalSymbolCandidateRank(spec, matches[i])
		rightRank := externalSymbolCandidateRank(spec, matches[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if matches[i].FilePath != matches[j].FilePath {
			return matches[i].FilePath < matches[j].FilePath
		}
		if matches[i].QualifiedName != matches[j].QualifiedName {
			return matches[i].QualifiedName < matches[j].QualifiedName
		}
		if matches[i].Name != matches[j].Name {
			return matches[i].Name < matches[j].Name
		}
		return matches[i].ID < matches[j].ID
	})
	return matches[0], true
}

func externalSymbolMatchesCandidate(spec string, candidate SymbolRef) bool {
	aliases := externalSymbolCandidateAliases(candidate)
	for _, normalized := range externalSymbolCandidateSpecs(spec, candidate.RepoKey) {
		for _, alias := range aliases {
			if alias == normalized {
				return true
			}
		}
	}
	return false
}

func externalSymbolCandidateRank(spec string, candidate SymbolRef) int {
	normalized := map[string]bool{}
	for _, value := range externalSymbolCandidateSpecs(spec, candidate.RepoKey) {
		normalized[value] = true
	}
	switch {
	case candidate.QualifiedName == spec:
		return 0
	case candidate.Name == spec:
		return 1
	case normalized[candidate.QualifiedName]:
		return 2
	case normalized[candidate.Name]:
		return 3
	case externalSymbolMatchesCandidate(spec, candidate):
		return 4
	default:
		return 5
	}
}

func externalSymbolCandidateAliases(candidate SymbolRef) []string {
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(filepath.ToSlash(value)), "/")
		if value == "" {
			return
		}
		seen[value] = true
		seen[strings.ReplaceAll(value, "/", ".")] = true
	}
	add(candidate.QualifiedName)
	add(candidate.Name)
	if candidate.FilePath != "" && candidate.Name != "" {
		path := strings.TrimSuffix(strings.Trim(filepath.ToSlash(candidate.FilePath), "/"), filepath.Ext(candidate.FilePath))
		if path != "" {
			add(path + "/" + candidate.Name)
			add(path + "." + candidate.Name)
			parts := strings.Split(path, "/")
			for i := 1; i < len(parts); i++ {
				add(strings.Join(parts[i:], "/") + "/" + candidate.Name)
				add(strings.Join(parts[i:], ".") + "." + candidate.Name)
			}
		}
	}
	out := make([]string, 0, len(seen))
	for alias := range seen {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

func externalSymbolCandidateSpecs(spec, repoKey string) []string {
	spec = strings.Trim(strings.TrimSpace(filepath.ToSlash(spec)), "/")
	colonSpec := strings.ReplaceAll(spec, "::", "/")
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(filepath.ToSlash(value)), "/")
		if value != "" {
			seen[value] = true
			seen[strings.ReplaceAll(value, "/", ".")] = true
		}
	}
	add(spec)
	add(colonSpec)
	for _, prefix := range repoImportPrefixes(repoKey) {
		prefix = strings.Trim(strings.TrimSpace(filepath.ToSlash(prefix)), "/")
		if prefix == "" {
			continue
		}
		for _, candidateSpec := range []string{spec, colonSpec} {
			for _, sep := range []string{"/", "."} {
				if strings.HasPrefix(candidateSpec, prefix+sep) {
					add(strings.TrimPrefix(candidateSpec, prefix+sep))
				}
			}
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func ImportTargetCandidate(candidates []SymbolRef, subpath string) (SymbolRef, bool) {
	subpath = strings.Trim(strings.TrimSpace(filepath.ToSlash(subpath)), "/")
	matchesSubpath := func(path string) bool {
		return importPathMatchesSubpath(path, subpath)
	}
	var symbolMatches []SymbolRef
	for _, candidate := range candidates {
		if importSymbolMatchesSubpath(candidate, subpath) {
			candidate.Direction = "import_symbol_target"
			symbolMatches = append(symbolMatches, candidate)
		}
	}
	if len(symbolMatches) > 0 {
		sort.Slice(symbolMatches, func(i, j int) bool {
			leftRank := importSymbolCandidateRank(symbolMatches[i], subpath)
			rightRank := importSymbolCandidateRank(symbolMatches[j], subpath)
			if leftRank != rightRank {
				return leftRank < rightRank
			}
			if symbolMatches[i].FilePath != symbolMatches[j].FilePath {
				return symbolMatches[i].FilePath < symbolMatches[j].FilePath
			}
			if symbolMatches[i].QualifiedName != symbolMatches[j].QualifiedName {
				return symbolMatches[i].QualifiedName < symbolMatches[j].QualifiedName
			}
			if symbolMatches[i].Name != symbolMatches[j].Name {
				return symbolMatches[i].Name < symbolMatches[j].Name
			}
			return symbolMatches[i].ID < symbolMatches[j].ID
		})
		return symbolMatches[0], true
	}
	var filtered []SymbolRef
	for _, candidate := range candidates {
		if matchesSubpath(candidate.FilePath) {
			candidate.Direction = "import_path_target"
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 && subpath != "" {
		filtered = append(filtered, candidates...)
	}
	if len(filtered) == 0 {
		return SymbolRef{}, false
	}
	sort.Slice(filtered, func(i, j int) bool {
		leftRank := importCandidateRank(filtered[i])
		rightRank := importCandidateRank(filtered[j])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if filtered[i].FilePath != filtered[j].FilePath {
			return filtered[i].FilePath < filtered[j].FilePath
		}
		if filtered[i].QualifiedName != filtered[j].QualifiedName {
			return filtered[i].QualifiedName < filtered[j].QualifiedName
		}
		if filtered[i].Name != filtered[j].Name {
			return filtered[i].Name < filtered[j].Name
		}
		return filtered[i].ID < filtered[j].ID
	})
	return filtered[0], true
}

func importPathMatchesSubpath(path, subpath string) bool {
	path = strings.Trim(filepath.ToSlash(path), "/")
	subpath = strings.Trim(filepath.ToSlash(strings.ReplaceAll(subpath, "::", "/")), "/")
	path = strings.TrimSuffix(path, filepath.Ext(path))
	for _, variant := range importSubpathVariants(subpath) {
		if variant == "" {
			return true
		}
		if path == variant || strings.HasPrefix(path, variant+"/") {
			return true
		}
		parts := strings.Split(path, "/")
		for i := 1; i < len(parts); i++ {
			suffix := strings.Join(parts[i:], "/")
			if suffix == variant || strings.HasPrefix(suffix, variant+"/") {
				return true
			}
		}
	}
	return false
}

func importSubpathVariants(subpath string) []string {
	subpath = strings.Trim(filepath.ToSlash(strings.ReplaceAll(subpath, "::", "/")), "/")
	variants := []string{subpath}
	parts := strings.Split(subpath, "/")
	if len(parts) > 0 && regexp.MustCompile(`^v[2-9][0-9]*$`).MatchString(parts[0]) {
		variants = append(variants, strings.Join(parts[1:], "/"))
	}
	return variants
}

func importSymbolMatchesSubpath(candidate SymbolRef, subpath string) bool {
	if candidate.ID == "" || strings.EqualFold(candidate.Kind, "file") || subpath == "" {
		return false
	}
	for _, name := range importSymbolNames(subpath) {
		if candidate.Name == name || candidate.QualifiedName == name {
			return true
		}
		if strings.HasSuffix(candidate.QualifiedName, "."+name) || strings.HasSuffix(candidate.QualifiedName, "/"+name) {
			return true
		}
	}
	return false
}

func importSymbolCandidateRank(candidate SymbolRef, subpath string) int {
	names := importSymbolNames(subpath)
	for _, name := range names {
		if candidate.QualifiedName == name {
			return 0
		}
	}
	for _, name := range names {
		if candidate.Name == name {
			return 1
		}
	}
	for _, name := range names {
		if strings.HasSuffix(candidate.QualifiedName, "."+name) || strings.HasSuffix(candidate.QualifiedName, "/"+name) {
			return 2
		}
	}
	return 3
}

func importSymbolNames(subpath string) []string {
	subpath = strings.Trim(strings.TrimSpace(filepath.ToSlash(strings.ReplaceAll(subpath, "::", "/"))), "/")
	if subpath == "" {
		return nil
	}
	seen := map[string]bool{}
	add := func(value string) {
		value = strings.Trim(strings.TrimSpace(value), ". /")
		if value != "" {
			seen[value] = true
		}
	}
	add(subpath)
	add(strings.ReplaceAll(subpath, "/", "."))
	parts := strings.FieldsFunc(subpath, func(r rune) bool { return r == '/' || r == '.' })
	if len(parts) > 0 {
		add(parts[len(parts)-1])
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// importCandidateRank orders the symbols a cross-repo import edge may
// point at, best first.
//
// Prose ranks BELOW every file. A repository's symbol table carries its
// Markdown headings as `section` symbols, `section` fell into the default arm
// alongside every real code symbol, and the tie was then broken by file path --
// where `README.md` sorts ahead of `client/client.go`, `money/money.go` and
// everything else. So a root-package import (`@owner/pkg`, `github.com/o/p`,
// with no subpath to match a symbol name against) resolved to the target
// repository's README heading, every time, in preference to its actual API:
//
//	web-dash imports @fleetk7q3z9/api-client
//	  -> api-client README.md section:api-client     (was)
//	  -> api-client client/client.go type:Client     (now)
//
// The edge is what tells an agent where a cross-repo dependency LANDS, so
// pointing it at documentation is worse than pointing it at nothing: the target
// is real, plausible, and never the code that would have to change.
func importCandidateRank(candidate SymbolRef) int {
	if documentationSymbolKinds[strings.ToLower(strings.TrimSpace(candidate.Kind))] {
		return 3
	}
	switch strings.ToLower(candidate.Kind) {
	case "package", "module", "namespace", "project":
		return 0
	case "file":
		return 2
	default:
		return 1
	}
}

func graphEdgeLess(a, b CrossEdge) bool {
	if a.SharedCount != b.SharedCount {
		return a.SharedCount > b.SharedCount
	}
	for _, pair := range [][2]string{{a.RelationKind, b.RelationKind}, {a.Type, b.Type}, {a.Endpoint, b.Endpoint}, {a.FromRepo, b.FromRepo}, {a.ToRepo, b.ToRepo}, {a.FromSymbol.ID, b.FromSymbol.ID}, {a.ToSymbol.ID, b.ToSymbol.ID}} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

func SortGraphCrossEdges(edges []CrossEdge) {
	sort.Slice(edges, func(i, j int) bool { return graphEdgeLess(edges[i], edges[j]) })
}

// keepGraphEdge retains the best limit edges while candidates stream in.
// It allocates O(limit) storage instead of retaining every cross-repository pair.
func keepGraphEdge(edges []CrossEdge, limit int, edge CrossEdge) []CrossEdge {
	if limit <= 0 {
		return edges
	}
	if len(edges) == limit && !graphEdgeLess(edge, edges[len(edges)-1]) {
		return edges
	}
	// Ranking includes the occurrence count; identity does not. Canonical routes
	// and channels can contribute the same pair with different counts. Keep its
	// strongest candidate without allowing duplicates to consume the limit.
	for i, existing := range edges {
		if existing.RelationKind == edge.RelationKind && existing.Type == edge.Type && existing.Endpoint == edge.Endpoint &&
			existing.FromRepo == edge.FromRepo && existing.ToRepo == edge.ToRepo &&
			existing.FromSymbol.ID == edge.FromSymbol.ID && existing.ToSymbol.ID == edge.ToSymbol.ID {
			if !graphEdgeLess(edge, existing) {
				return edges
			}
			copy(edges[i:], edges[i+1:])
			edges = edges[:len(edges)-1]
			break
		}
	}
	at := sort.Search(len(edges), func(i int) bool { return !graphEdgeLess(edges[i], edge) })
	if at < len(edges) && !graphEdgeLess(edge, edges[at]) {
		return edges
	}
	if len(edges) < limit {
		edges = append(edges, CrossEdge{})
	}
	copy(edges[at+1:], edges[at:len(edges)-1])
	edges[at] = edge
	return edges
}

// documentationSymbolKinds are the symbol kinds that describe prose
// rather than code. They are still legitimate import targets when nothing else
// matches -- a docs-only repository has nothing better to offer -- so they are
// ranked last rather than filtered out.
var documentationSymbolKinds = map[string]bool{
	"section":   true,
	"heading":   true,
	"chapter":   true,
	"doc":       true,
	"document":  true,
	"paragraph": true,
}
