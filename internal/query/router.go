package query

import (
	"net/http"
	"os"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/playground"
	"github.com/go-chi/chi/v5"

	"github.com/dong-qiu/agent-lens/internal/store"
)

// RegisterRoutes wires the GraphQL endpoint at /graphql onto r. Mount r
// under /v1 so the playground's hard-coded /v1/graphql endpoint URL is
// correct. The interactive playground is only mounted when
// AGENT_LENS_PLAYGROUND=true so a production deployment doesn't expose
// introspection by default.
func RegisterRoutes(r chi.Router, s store.Store) {
	srv := handler.NewDefaultServer(NewExecutableSchema(Config{Resolvers: NewResolver(s)}))
	r.Handle("/graphql", srv)
	if os.Getenv("AGENT_LENS_PLAYGROUND") == "true" {
		r.Handle("/playground", playground.Handler("Agent Lens", "/v1/graphql"))
	}
}

func NewRouter(s store.Store) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, s)
	return r
}
