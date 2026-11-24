package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestStaticFSContainsConsoleAssets(t *testing.T) {
	t.Parallel()

	static, err := StaticFS()
	if err != nil {
		t.Fatalf("StaticFS() error = %v", err)
	}

	assets := map[string][]string{
		"index.html": {`href="/assets/styles.css"`, `src="/assets/app.js"`},
		"styles.css": nil,
		"app.js":     {"textContent", "X-Account-ID"},
	}

	for name, required := range assets {
		data, err := fs.ReadFile(static, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%s is empty", name)
		}
		for _, fragment := range required {
			if !strings.Contains(string(data), fragment) {
				t.Errorf("%s does not contain %q", name, fragment)
			}
		}
	}
}

func TestStaticAssetsAvoidHTMLInjectionHelpers(t *testing.T) {
	t.Parallel()

	static, err := StaticFS()
	if err != nil {
		t.Fatalf("StaticFS() error = %v", err)
	}

	script, err := fs.ReadFile(static, "app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}

	for _, forbidden := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(string(script), forbidden) {
			t.Errorf("app.js must not use %s for API data", forbidden)
		}
	}
}
