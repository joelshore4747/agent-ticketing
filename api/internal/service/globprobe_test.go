package service

import "testing"

func TestGlobSemanticsMatchGlobset(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"api/**", "api/src/service.rs", true},
		{"api/**", "web/src/main.tsx", false},
		{"web/**", "web/src/routes/Board.tsx", true},
		{"api/src/auth/**", "api/src/auth/token.rs", true},
		{"api/src/auth/**", "api/src/service.rs", false},
		{"README.md", "README.md", true},
		{"README.md", "docs/README.md", false},
		{"docs/**", "docs/api.md", true},
		{".github/**", ".github/workflows/ci.yml", true},
		{"*.md", "README.md", true},
		{"*.md", "docs/api.md", false},
	}
	for _, c := range cases {
		g, err := compileGlob(c.pattern)
		if err != nil {
			t.Fatalf("compile %q: %v", c.pattern, err)
		}
		if got := g.Match(c.path); got != c.want {
			t.Errorf("%q against %q = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
