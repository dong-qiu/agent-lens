# query — GraphQL API

Read API for Agent Lens. Schema in `schema.graphql`; Go bindings are generated
by [gqlgen](https://gqlgen.com).

## Workflow

```bash
make gqlgen
```

After codegen runs, the directory will additionally contain:

- `generated.go` — `NewExecutableSchema(...)`, internal plumbing
- `models_gen.go` — Go types for GraphQL types not bound in `gqlgen.yml`
- `query.resolvers.go` — resolver method stubs that delegate to `Resolver`

Fill the stubs in `query.resolvers.go` to call `r.Store` and return generated
model types.

## HTTP wire-up

`router.go` (will be added post-codegen) mounts the GraphQL handler and the
gqlgen Playground onto the chi router. Until codegen runs, `cmd/agent-lens`
does not import this package.
