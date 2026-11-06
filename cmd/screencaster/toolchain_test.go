package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot is the module root as seen from cmd/screencaster.
const repoRoot = "../.."

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The Go version lives in go.mod, the Dockerfile base images and ci.yml. They
// drift silently otherwise, and a reachable stdlib advisory is fixed only by a
// newer toolchain (security audit H1).
func TestToolchain_goVersionMatchesDockerfileAndCI(t *testing.T) {
	m := regexp.MustCompile(`(?m)^go (\S+)$`).FindStringSubmatch(repoFile(t, "go.mod"))
	if m == nil {
		t.Fatal("no go directive in go.mod")
	}
	want := m[1]

	froms := regexp.MustCompile(`(?m)^FROM golang:(\S+?)-bookworm\b`).FindAllStringSubmatch(repoFile(t, "Dockerfile"), -1)
	if len(froms) == 0 {
		t.Fatal("no FROM golang:<v>-bookworm in Dockerfile")
	}
	for _, f := range froms {
		if f[1] != want {
			t.Errorf("Dockerfile has golang:%s, go.mod has %s", f[1], want)
		}
	}

	ci := regexp.MustCompile(`(?m)^\s*go-version: "(\S+)"$`).FindAllStringSubmatch(repoFile(t, ".github/workflows/ci.yml"), -1)
	if len(ci) == 0 {
		t.Fatal("no go-version in ci.yml")
	}
	for _, c := range ci {
		if c[1] != want {
			t.Errorf("ci.yml has go-version %s, go.mod has %s", c[1], want)
		}
	}
}

var fromRE = regexp.MustCompile(`(?m)^FROM\s+(\S+)(?:\s+AS\s+(\S+))?`)

// dockerfileFromRefs returns every FROM image reference in a Dockerfile,
// skipping references that are a build ARG (${...}) or that name an earlier
// stage in the same file (FROM <stage> builds on it, not a registry image).
func dockerfileFromRefs(src string) []string {
	stages := map[string]bool{}
	var refs []string
	for _, m := range fromRE.FindAllStringSubmatch(src, -1) {
		ref := m[1]
		if m[2] != "" {
			stages[m[2]] = true
		}
		switch {
		case strings.HasPrefix(ref, "${"):
		case stages[ref]:
		default:
			refs = append(refs, ref)
		}
	}
	return refs
}

// M6: a mutable tag can be repointed after review; a digest cannot (security
// audit M6). dockerfileFromRefs already exempts build-ARG and stage-name
// references (providers/piper/Dockerfile's ${DEV_BASE}/${RUNTIME_BASE}).
func TestToolchain_baseImagesPinnedByDigest(t *testing.T) {
	for _, path := range []string{"Dockerfile", "providers/piper/Dockerfile"} {
		for _, ref := range dockerfileFromRefs(repoFile(t, path)) {
			if !strings.Contains(ref, "@sha256:") {
				t.Errorf("%s: FROM %s is not pinned by digest", path, ref)
			}
		}
	}
}

var usesRE = regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*(\S+)@(\S+)\s*(?:#.*)?$`)

// M6: a tag (v4) can move; only a 40-hex commit SHA is immutable.
func TestToolchain_actionsPinnedToSHA(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, path := range []string{".github/workflows/ci.yml", ".github/workflows/release.yml"} {
		uses := usesRE.FindAllStringSubmatch(repoFile(t, path), -1)
		if len(uses) == 0 {
			t.Fatalf("%s: no uses: lines found", path)
		}
		for _, u := range uses {
			if !sha.MatchString(u[2]) {
				t.Errorf("%s: %s@%s is not pinned to a 40-hex SHA", path, u[1], u[2])
			}
		}
	}
}

// M8: the runtime image must not default to root.
func TestToolchain_runtimeImageNotRoot(t *testing.T) {
	src := repoFile(t, "Dockerfile")
	i := strings.Index(src, "AS runtime-base")
	if i < 0 {
		t.Fatal("no runtime-base stage in Dockerfile")
	}
	stage := src[i:]
	if j := strings.Index(stage[1:], "\nFROM "); j >= 0 {
		stage = stage[:j+1]
	}
	if !regexp.MustCompile(`(?m)^USER\s+\S+`).MatchString(stage) {
		t.Error("runtime-base has no USER instruction; the image defaults to root")
	}
}

// M5/M7: the release workflow must refuse any ref but main, and must publish
// a checksum file alongside the binaries.
func TestRelease_gatedOnMainAndPublishesChecksums(t *testing.T) {
	src := repoFile(t, ".github/workflows/release.yml")
	if !strings.Contains(src, `refs/heads/main`) {
		t.Error("release.yml has no refs/heads/main ref gate")
	}
	if !strings.Contains(src, "sha256sum") || !strings.Contains(src, "SHA256SUMS") {
		t.Error("release.yml does not build a SHA256SUMS checksum file")
	}
	for _, cmd := range []string{"gh release upload", "gh release create"} {
		i := strings.Index(src, cmd)
		if i < 0 {
			t.Fatalf("release.yml has no %q", cmd)
		}
		// The checksum file must be one of the uploaded assets, not just
		// present somewhere else in the file.
		line := src[i:]
		if j := strings.IndexByte(line, '\n'); j >= 0 {
			line = line[:j]
		}
		if !strings.Contains(line, "SHA256SUMS") {
			t.Errorf("release.yml: %q does not include dist/SHA256SUMS", cmd)
		}
	}
}
