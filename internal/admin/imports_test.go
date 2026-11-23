package admin_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/kibanbuild/dojo"

func deps(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pkg)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		t.Fatalf("go list -deps %s: %v\n%s", pkg, err, stderr)
	}
	set := map[string]bool{}
	for _, line := range strings.Fields(string(out)) {
		set[line] = true
	}
	return set
}

// ADR 0002: no operator code is compiled into the public API binary.
func TestAPIDoesNotImportAdmin(t *testing.T) {
	for dep := range deps(t, "./cmd/dojo-api") {
		if dep == module+"/internal/admin" || strings.HasPrefix(dep, module+"/internal/admin/") ||
			dep == module+"/cmd/dojo-admin" {
			t.Errorf("cmd/dojo-api pulls in %s", dep)
		}
	}
}

// The admin binary shares domain code (internal/server/service) but none of the public surface:
// the API's routes and session handling, the CLI, the lab or the MCP server.
func TestAdminDoesNotImportPublicOnlyPackages(t *testing.T) {
	forbidden := []string{
		module + "/internal/server",
		module + "/internal/cli",
		module + "/internal/lab",
		module + "/internal/mcp",
		module + "/cmd/dojo-api",
		module + "/cmd/dojo",
	}
	d := deps(t, "./cmd/dojo-admin")
	if !d[module+"/internal/admin"] {
		t.Fatal("go list did not report cmd/dojo-admin's own dependencies: the guard checks nothing")
	}
	for _, f := range forbidden {
		if d[f] {
			t.Errorf("cmd/dojo-admin pulls in %s", f)
		}
	}
}
