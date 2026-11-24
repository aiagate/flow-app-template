// Package internal verifies the Clean Architecture dependency rules with the
// standard parser so the check does not depend on `go list` output.
package internal

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type dependencyRule struct {
	name             string
	allowInternal    func(string) bool
	allowExternal    func(string) bool
	forbiddenImports map[string]string
}

func TestArchitectureDependencies(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate architecture test source")
	}
	root := filepath.Dir(filepath.Dir(sourceFile))
	modulePath := readModulePath(t, filepath.Join(root, "go.mod"))
	internalRoot := filepath.Join(root, "internal")

	var violations []string
	err := filepath.WalkDir(internalRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}

		relDir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		relDir = filepath.ToSlash(relDir)
		rule, exists := ruleFor(relDir)
		if !exists {
			violations = append(violations, relDir+": package has no declared dependency rule")
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		relFile, _ := filepath.Rel(root, path)
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				violations = append(violations, filepath.ToSlash(relFile)+": invalid import literal "+spec.Path.Value)
				continue
			}
			if reason, forbidden := rule.forbiddenImports[importPath]; forbidden {
				violations = append(violations, filepath.ToSlash(relFile)+": imports forbidden package "+importPath+" ("+reason+")")
				continue
			}
			if internalPath, isInternal := moduleRelative(modulePath, importPath); isInternal {
				if !rule.allowInternal(internalPath) {
					violations = append(violations, filepath.ToSlash(relFile)+": "+rule.name+" may not import "+importPath)
				}
				continue
			}
			if isStandardLibrary(importPath) {
				continue
			}
			if !rule.allowExternal(importPath) {
				violations = append(violations, filepath.ToSlash(relFile)+": "+rule.name+" may not import external package "+importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal packages: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("architecture violations:\n  %s", strings.Join(violations, "\n  "))
	}
}

func ruleFor(relDir string) (dependencyRule, bool) {
	switch relDir {
	case "internal/domain":
		return dependencyRule{
			name:          "domain",
			allowInternal: func(string) bool { return false },
			allowExternal: func(path string) bool { return path == "github.com/oklog/ulid/v2" },
			forbiddenImports: map[string]string{
				"context":      "domain must stay context-free",
				"database/sql": "domain must not perform I/O",
				"net/http":     "domain must not depend on transport",
			},
		}, true
	case "internal/contract/port":
		return dependencyRule{
			name:          relDir,
			allowInternal: func(path string) bool { return path == "internal/domain" },
			allowExternal: func(string) bool { return false },
			forbiddenImports: map[string]string{
				"database/sql": "contract must not depend on persistence drivers",
				"net/http":     "contract must not depend on transport",
			},
		}, true
	}

	if relDir == "internal/usecase" || strings.HasPrefix(relDir, "internal/usecase/") {
		return dependencyRule{
			name: "usecase",
			allowInternal: func(path string) bool {
				return path == "internal/domain" || strings.HasPrefix(path, "internal/contract/")
			},
			allowExternal: func(string) bool { return false },
			forbiddenImports: map[string]string{
				"database/sql": "usecase must go through contract ports",
				"net/http":     "usecase must not depend on transport",
			},
		}, true
	}

	for _, component := range []string{"config", "sqlite", "hotpath", "batcher"} {
		prefix := "internal/infra/" + component
		if relDir == prefix || strings.HasPrefix(relDir, prefix+"/") {
			return dependencyRule{
				name: "infra/" + component,
				allowInternal: func(path string) bool {
					return path == "internal/domain" ||
						strings.HasPrefix(path, "internal/contract/") ||
						path == "migrations"
				},
				allowExternal: func(path string) bool {
					return path == "github.com/oklog/ulid/v2" ||
						path == "modernc.org/libc" ||
						strings.HasPrefix(path, "modernc.org/libc/") ||
						path == "modernc.org/sqlite" ||
						strings.HasPrefix(path, "modernc.org/sqlite/")
				},
				forbiddenImports: map[string]string{},
			}, true
		}
	}

	if relDir == "internal/presentation/httpapi" || strings.HasPrefix(relDir, "internal/presentation/httpapi/") {
		return dependencyRule{
			name: "presentation/httpapi",
			allowInternal: func(path string) bool {
				return path == "internal/domain" ||
					strings.HasPrefix(path, "internal/contract/") ||
					path == "internal/usecase" ||
					strings.HasPrefix(path, "internal/usecase/") ||
					path == "web" ||
					strings.HasPrefix(path, "web/")
			},
			allowExternal: func(string) bool { return false },
			forbiddenImports: map[string]string{
				"database/sql": "presentation must not depend on persistence drivers",
			},
		}, true
	}

	return dependencyRule{}, false
}

func readModulePath(t *testing.T, goModPath string) string {
	t.Helper()
	contents, err := os.ReadFile(goModPath)
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1]
		}
	}
	t.Fatalf("go.mod at %s has no module directive", goModPath)
	return ""
}

func moduleRelative(modulePath, importPath string) (string, bool) {
	if importPath == modulePath {
		return ".", true
	}
	prefix := modulePath + "/"
	if strings.HasPrefix(importPath, prefix) {
		return strings.TrimPrefix(importPath, prefix), true
	}
	return "", false
}

// Standard-library import paths never contain a dot in their first segment.
// Every module path used by this project does.
func isStandardLibrary(importPath string) bool {
	first := importPath
	if index := strings.IndexByte(first, '/'); index >= 0 {
		first = first[:index]
	}
	return !strings.Contains(first, ".")
}
