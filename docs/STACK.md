# Tech Stack — Agent Lens

Concise record of the stack chosen for v1, along with the alternatives we evaluated. The headline decisions are also captured in `SPEC.md` §16; this file expands the rationale.

| Layer | Choice | Alternatives considered | Why |
|---|---|---|---|
| Backend language | Go | Rust, TypeScript/Node, Python | in-toto-golang / sigstore-go / cosign / slsa-verifier are native Go. Single static binary fits self-hosted + air-gap. Iteration speed beats Rust at our scale. |
| Web framework | `chi` (stdlib-ish) | Echo, Fiber, gin | Minimal, idiomatic, no magic. |
| Schema | Protobuf via `buf` | JSON Schema, Avro | Strong typing + cross-language codegen; can also marshal as JSON for the wire. |
| Ingest protocol | HTTP + NDJSON | gRPC | `curl`-friendly, debuggable; our event volume per developer is far below gRPC's payoff threshold. Same protobuf schema is reusable when gRPC is added. |
| Query protocol | GraphQL (`gqlgen`) | REST, gRPC | Trace exploration is graph-shaped; GraphQL is the natural fit. Minimal REST is added for programmatic clients. |
| Event store | Postgres (`pgx` + `sqlc` + `golang-migrate`) | ClickHouse, Kafka + EventStoreDB | Hash chain wants strict ordering + ACID; Postgres is the simplest self-hosted choice. ClickHouse will be added in M4+ as a CDC-fed analytics replica. |
| Object storage | MinIO (S3 protocol) | Local FS, custom | Standard self-hosted choice; swappable for AWS S3 / GCS / R2 in HA. |
| Signing | `sigstore-go` library + local ed25519 keys (default) | cosign + Fulcio + Rekor | Air-gap is the default; sigstore-go supports both local and Fulcio-backed keys, so opting into Sigstore later is a config change, not a rewrite. |
| Hook binary | Single Go binary `agent-lens-hook` with subcommands | Bash + curl, Node CLI | Sub-10 ms cold start, static binary, easy distribution, schema-aware. |
| Frontend framework | React + TS + Vite | Svelte/SvelteKit, Solid | ReactFlow (causal graph) and Monaco (diff) ecosystems are decisive. |
| Frontend styles | Tailwind + shadcn/ui | CSS modules, Chakra | Boring & fast. |
| Frontend data | TanStack Query + GraphQL Codegen | SWR, Apollo | Lighter than Apollo, type-safe via codegen. |
| Container build | Docker buildx (multi-arch) | ko, nixpacks | Standard, supports amd64 + arm64. |
| Local orchestration | Docker Compose (M1) | k3d / kind | Lowest friction for the M1 demo. Helm chart is added in M3+. |
| Self-observability | OpenTelemetry SDK + Prometheus + slog | None | The audit system itself must be observable. |

## Decisions deliberately deferred

- **gRPC ingest**: protobuf schema makes adding it cheap; revisit when a single tenant exceeds ~50K events/sec.
- **ClickHouse**: not for the canonical store. Add as analytics replica when "Agent usage patterns" reports become a real workload.
- **MCP server**: an Agent Lens MCP server (so Lens queries can be reflected back to the Agent) is an M4 nice-to-have, not on the M1 critical path.
- **PII redaction model**: rule-based redaction first; ML-assisted only after the rule list has stabilized.
