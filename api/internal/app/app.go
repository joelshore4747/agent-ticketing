// Package app is the whole HTTP surface: GraphQL (/graphql), REST (/api/*) and MCP (/mcp).
package app

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/99designs/gqlgen/graphql/playground"

	"github.com/joelshore/ticketing/api/internal/graph"
	"github.com/joelshore/ticketing/api/internal/mcpserver"
	"github.com/joelshore/ticketing/api/internal/rest"
	"github.com/joelshore/ticketing/api/internal/service"
)

// InvalidCorsOrigin means CORS_ORIGIN did not name an origin we can serve. Fatal at startup:
// silently widening to "any origin" would be a worse outcome than refusing to boot.
type InvalidCorsOrigin struct {
	Value  string
	Reason string
}

func (e *InvalidCorsOrigin) Error() string {
	return fmt.Sprintf("CORS_ORIGIN=%q is not usable: %s. Expected an origin like "+
		"http://localhost:5173, or `*` to allow any origin.", e.Value, e.Reason)
}

// New builds the whole surface, ready to serve or to drive in a test. tokensPath is what
// TICKETING_TOKENS named; empty is local mode, where the actor still comes from X-Actor.
func New(db service.DB, corsOrigin, tokensPath string) (http.Handler, error) {
	origin, err := checkOrigin(corsOrigin)
	if err != nil {
		return nil, err
	}
	creds, err := loadCredentials(tokensPath)
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	rest.Register(mux, db)

	gql := handler.New(graph.NewExecutableSchema(graph.Config{
		Resolvers: &graph.Resolver{DB: db},
	}))
	gql.AddTransport(transport.POST{})
	gql.AddTransport(transport.GET{})
	gql.AddTransport(transport.Options{})
	gql.Use(extension.Introspection{})

	mux.Handle("POST /graphql", gql)
	mux.Handle("GET /graphql", playground.Handler("ticketing", "/graphql"))
	// One handler bound to both patterns, not two. Calling Handler twice built two independent
	// *mcp.Server values, which is harmless while every tool is stateless but silently breaks the
	// moment anything is per-server — resources, subscriptions, or registration after startup.
	mcp := mcpserver.Handler(db)
	mux.Handle("/mcp", mcp)
	mux.Handle("/mcp/", mcp)

	// CORS stays outermost: a 401 the browser cannot read is a 401 the web app cannot offer a login
	// for, and the preflight is answered here rather than by a credential check.
	return corsMiddleware(origin)(withActor(creds)(mux)), nil
}

// checkOrigin validates CORS_ORIGIN. A literal `*` means "any origin"; anything else must be a
// real origin.
func checkOrigin(origin string) (string, error) {
	value := strings.TrimSpace(origin)
	if value == "*" {
		return value, nil
	}
	invalid := func(reason string) error {
		return &InvalidCorsOrigin{Value: origin, Reason: reason}
	}
	var rest string
	switch {
	case strings.HasPrefix(value, "https://"):
		rest = strings.TrimPrefix(value, "https://")
	case strings.HasPrefix(value, "http://"):
		rest = strings.TrimPrefix(value, "http://")
	default:
		return "", invalid("must start with http:// or https://")
	}
	if rest == "" {
		return "", invalid("missing host")
	}
	if strings.Contains(rest, "/") {
		return "", invalid("must not contain a path, only scheme://host[:port]")
	}
	// A space is a legal header byte but never a legal origin.
	if strings.ContainsAny(value, " \t\r\n") {
		return "", invalid("must not contain whitespace")
	}
	return value, nil
}

func corsMiddleware(origin string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "*")
			// MCP clients read the session id off the response.
			h.Set("Access-Control-Expose-Headers", "mcp-session-id")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
