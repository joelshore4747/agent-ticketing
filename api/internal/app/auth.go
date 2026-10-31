package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/joelshore/ticketing/api/internal/service"
)

// credentials maps the sha256 of a bearer token to who holds it. nil is local mode — no file was
// named, so the actor still comes from X-Actor as it always did (rule 10).
type credentials map[string]service.Actor

// howToAuthenticate is the 401 body. It names the way out, because the caller is usually an agent
// whose only clue is this string.
const howToAuthenticate = "no credential: send `Authorization: Bearer <token>`. One is issued per " +
	"actor with scripts/issue-token.sh, and TICKETING_TOKENS names the file this API reads."

// loadCredentials reads the file TICKETING_TOKENS names: `<sha256-hex> <actor> [human]` a line,
// blank lines and # comments skipped. Hashes rather than tokens, so a leaked file is not a set of
// working credentials. A named file that cannot be read, or that holds none, is fatal at startup:
// booting open would authenticate nobody and tell nobody it had stopped trying.
func loadCredentials(path string) (credentials, error) {
	if strings.TrimSpace(path) == "" {
		fmt.Println("auth off (TICKETING_TOKENS unset)")
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("TICKETING_TOKENS=%q cannot be read: %w", path, err)
	}
	creds := credentials{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		creds[fields[0]] = service.Actor{
			Name: fields[1], Human: len(fields) > 2 && fields[2] == "human", Credentialed: true,
		}
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("TICKETING_TOKENS=%q holds no `<sha256-hex> <actor> [human]` line", path)
	}
	fmt.Printf("auth on (%d credentials from %s)\n", len(creds), path)
	return creds, nil
}

// withActor settles who is acting for every transport at once. With credentials loaded the bearer
// token decides and X-Actor is ignored, because a header any caller can type is not an identity;
// without them the header is all there is.
func withActor(creds credentials) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor := service.Actor{Name: r.Header.Get("X-Actor")}
			if creds != nil {
				held, known := creds[bearerHash(r)]
				if !known && !openToAnyone(r) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": howToAuthenticate})
					return
				}
				actor = held
			}
			// The settled actor goes back onto the header as well as the context, because an MCP
			// tool call can read only the header: its context descends from the session's
			// initialize request, not from this one (TP-318). Overwriting is the point — with
			// login on, whatever the caller typed here is gone.
			r.Header.Set("X-Actor", actor.Name)
			next.ServeHTTP(w, r.WithContext(service.WithActor(r.Context(), actor)))
		})
	}
}

// openToAnyone is what needs no credential: the CORS preflight, the version probe the loop reads to
// decide whether the process is serving this checkout, and the GraphQL playground page. /healthz is
// not among them — every script that probes it starts its own API with `env -u TICKETING_TOKENS`.
func openToAnyone(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return true
	}
	return r.Method == http.MethodGet && (r.URL.Path == "/api/version" || r.URL.Path == "/graphql")
}

func bearerHash(r *http.Request) string {
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
