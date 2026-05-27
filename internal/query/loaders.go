package query

import (
	"context"
	"net/http"

	"github.com/graph-gophers/dataloader/v7"

	"github.com/dong-qiu/agent-lens/internal/store"
)

// Per-request DataLoaders (issue #20). Event.links is a lazy field resolver,
// so a query like `events(limit:200){ links }` would otherwise fire 200
// independent link lookups. The loader collects the ids resolved within one
// request and dispatches a single batched LinksForEvents query.
//
// Scope is strictly per-request: LoaderMiddleware builds a fresh Loaders into
// the request context, so cached results never leak across requests.

type ctxKey int

const loadersKey ctxKey = iota

type Loaders struct {
	LinksByEvent *dataloader.Loader[string, []*store.Link]
}

func newLoaders(s store.Store) *Loaders {
	return &Loaders{
		LinksByEvent: dataloader.NewBatchedLoader(linksBatchFn(s)),
	}
}

// linksBatchFn turns N event ids into one LinksForEvents call, then fans the
// grouped result back out in the order dataloader requested the keys.
func linksBatchFn(s store.Store) dataloader.BatchFunc[string, []*store.Link] {
	return func(ctx context.Context, ids []string) []*dataloader.Result[[]*store.Link] {
		byID, err := s.LinksForEvents(ctx, ids)
		results := make([]*dataloader.Result[[]*store.Link], len(ids))
		for i, id := range ids {
			if err != nil {
				results[i] = &dataloader.Result[[]*store.Link]{Error: err}
				continue
			}
			results[i] = &dataloader.Result[[]*store.Link]{Data: byID[id]}
		}
		return results
	}
}

// LoaderMiddleware injects a fresh per-request Loaders into the context. Wrap
// the GraphQL handler with it so resolvers can reach the loaders via ctx.
func LoaderMiddleware(s store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), loadersKey, newLoaders(s))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// loadersFrom returns the per-request Loaders, or nil if the request didn't
// pass through LoaderMiddleware (e.g. a resolver invoked directly in a test).
func loadersFrom(ctx context.Context) *Loaders {
	l, _ := ctx.Value(loadersKey).(*Loaders)
	return l
}
