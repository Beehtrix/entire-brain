# Radar Tool Evidence Audit

- Status: **PASS**
- Claim scope: **mcp_radar_tool_contract**
- Required tests: **30**
- Artifact: `go-test-internal-cli-radar.jsonl`
- Artifact hash: `sha256:6d80dc07c8a5bc27282aefe9fd3f5744b06100f99a69a01a87bd572f80bf0fc0`
- Package pass event: **True**

## Limitations
- Tool-contract proof only; this is not agent lift proof.
- Radar agent-lift proof/no-claim status is retained separately under benchmarks/agent-brain/evidence/release and checked by mise run radar:agent-evidence.
- This artifact proves local deterministic Radar tool behavior for the current internal/cli test fixtures.
