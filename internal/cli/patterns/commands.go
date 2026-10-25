package patterns

// GramSep joins command heads inside a gram. It must be recoverable: command
// heads themselves contain spaces ("git push"), so a plain space join would be
// ambiguous and break per-command specificity (gramSpecificity splits on this).
const GramSep = " ▷ "

var ValidationCommandHeads = map[string]bool{
	"go test": true, "go vet": true, "mise test": true, "mise run": true, "npm test": true,
	"pnpm test": true, "yarn test": true, "cargo test": true, "pytest": true, "gofmt": true,
	"make test": true, "make check": true,
}
