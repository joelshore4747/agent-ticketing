package app_test

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// The web tests answer every query with a mock, so nothing there notices a field the schema no
// longer has. TP-84 dropped Ticket.checks from the SDL by accident and every web test stayed green
// while the live ticket page failed to load. This validates each document the web app sends against
// the schema the API serves.
func TestEveryWebQueryIsValidAgainstTheServedSchema(t *testing.T) {
	sdl, err := os.ReadFile("../graph/schema.graphqls")
	if err != nil {
		t.Fatal(err)
	}
	schema, gqlErr := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphqls", Input: string(sdl)})
	if gqlErr != nil {
		t.Fatal(gqlErr)
	}
	src, err := os.ReadFile("../../../web/src/api/queries.ts")
	if err != nil {
		t.Fatal(err)
	}

	consts := map[string]string{}
	var order []string
	for _, m := range regexp.MustCompile("(?s)const (\\w+) = /\\* GraphQL \\*/ `([^`]*)`").FindAllStringSubmatch(string(src), -1) {
		consts[m[1]] = m[2]
		order = append(order, m[1])
	}
	interpolation := regexp.MustCompile(`\$\{(\w+)\}`)
	checked := 0
	for _, name := range order {
		doc := interpolation.ReplaceAllStringFunc(consts[name], func(ref string) string {
			return consts[ref[2:len(ref)-1]]
		})
		trimmed := strings.TrimSpace(doc)
		if !strings.HasPrefix(trimmed, "query") && !strings.HasPrefix(trimmed, "mutation") {
			continue // a field set, checked where it is used
		}
		checked++
		if _, errs := gqlparser.LoadQuery(schema, doc); errs != nil {
			t.Errorf("%s: %v", name, errs)
		}
	}
	if checked < 20 {
		t.Fatalf("validated only %d documents; has queries.ts changed shape?", checked)
	}
}
