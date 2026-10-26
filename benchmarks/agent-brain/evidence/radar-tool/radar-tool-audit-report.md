# Radar Tool Evidence Audit

- Status: **PASS**
- Claim scope: **mcp_radar_tool_contract**
- Required tests: **30**
- Artifact: `go-test-internal-cli-radar.jsonl`
- Artifact hash: `sha256:51a6452ed80e009be8e5a1cc1a32ce0edd55c1b382fc56ecff91f1d08ffd27e0`
- Package pass event: **True**

## Limitations
- Tool-contract proof only; this is not agent lift proof.
- Radar agent-lift proof/no-claim status is retained separately under benchmarks/agent-brain/evidence/release and checked by mise run radar:agent-evidence.
- This artifact proves local deterministic Radar tool behavior for the current internal/cli test fixtures.
