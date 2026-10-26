package workspace

type Contract struct {
	Endpoint string   `json:"endpoint"`
	Type     string   `json:"type"`
	Repos    []string `json:"repos"`
	Count    int      `json:"count"`
}

type SymbolRef struct {
	RepoKey       string `json:"repo_key"`
	ID            string `json:"id"`
	Kind          string `json:"kind,omitempty"`
	Name          string `json:"name,omitempty"`
	QualifiedName string `json:"qualified_name,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
	Direction     string `json:"direction,omitempty"`
	Count         int    `json:"count,omitempty"`
}

type CrossEdge struct {
	Endpoint     string    `json:"endpoint"`
	Type         string    `json:"type"`
	FromRepo     string    `json:"from_repo"`
	ToRepo       string    `json:"to_repo"`
	FromSymbol   SymbolRef `json:"from_symbol"`
	ToSymbol     SymbolRef `json:"to_symbol"`
	SharedCount  int       `json:"shared_count"`
	RelationKind string    `json:"relation_kind"`
}

type ExternalContractAggregate struct {
	Endpoint     string
	Type         string
	RepoCounts   map[string]int
	Participants []SymbolRef
}

type RepoGraphIndex struct {
	RepoKey         string
	Imports         []ImportRef
	ExternalSymbols []ExternalSymbolRef
	Candidates      []SymbolRef
}

type ImportRef struct {
	Spec   string
	Source SymbolRef
	Count  int
}

type ExternalSymbolRef struct {
	Spec   string
	Type   string
	Source SymbolRef
	Count  int
}
