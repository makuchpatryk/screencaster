//go:build e2e

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"screencaster/internal/adapters/browser"
	"screencaster/internal/app/explorer"
	"screencaster/internal/domain/script"
)

func newExplorer() explorer.Explorer {
	return explorer.Explorer{Launch: func(ctx context.Context, o explorer.LaunchOptions) (explorer.Session, error) {
		s, err := browser.Launcher{}.Launch(ctx, browser.Options{StorageState: o.StorageState})
		if err != nil {
			return nil, err
		}
		return s, nil
	}}
}

// selectorFor returns the selector explore_page printed for the line holding
// the given role and name.
func selectorFor(t *testing.T, snapshot, role, name string) string {
	t.Helper()
	re := regexp.MustCompile(`- ` + role + ` "` + regexp.QuoteMeta(name) + `"[^\n]*? -> ([^\n]+?):?(?:\n|$)`)
	m := re.FindStringSubmatch(snapshot)
	if m == nil {
		t.Fatalf("no selector for %s %q in snapshot:\n%s", role, name, snapshot)
	}
	return strings.TrimSpace(m[1])
}

// FR-017 AC1 and AC3: the snapshot carries role=button[name="New project"],
// and the selectors it returns drive a real render without edits.
func TestExplore_selectorsWorkInARender(t *testing.T) {
	base := fixtureApp(t)
	dir := project(t, base)

	out, err := newExplorer().Explore(context.Background(), explorer.Input{
		StorageState: fixtureState,
		URL:          base + "/projects.html",
		Actions:      []script.Step{{Action: script.Click{Selector: `role=button[name="New project"]`}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Title != "Projects" || out.Truncated {
		t.Errorf("Output = title %q truncated %v", out.Title, out.Truncated)
	}
	newProject := selectorFor(t, out.Snapshot, "button", "New project")
	if newProject != `role=button[name="New project"]` {
		t.Errorf("New project selector = %q", newProject)
	}
	name := selectorFor(t, out.Snapshot, "textbox", "Name")
	create := selectorFor(t, out.Snapshot, "button", "Create")

	body := "name: explored\n" + stateYAML + "\nsteps:\n" +
		"  - goto: " + base + "/projects.html\n" +
		"  - click: '" + newProject + "'\n" +
		"  - fill: {selector: '" + name + "', value: Demo}\n" +
		"  - click: '" + create + "'\n" +
		"  - wait: '#toast'\n"
	if err := os.WriteFile(filepath.Join(dir, "demos", "explored.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runCLI(t, cliBinary(t), dir, "render", "demos/explored.yaml")
	if res.code != 0 {
		t.Fatalf("render with explored selectors exit %d\n%s", res.code, res.stderr)
	}
}

// ARCHITECTURE §7, §8: a name that is a substring of an earlier one ("Save"
// after "Save all") and a repeated name ("Delete") each get a selector that
// clicks exactly the element on its line. role= compares whole names, so only
// the repeated one needs nth.
func TestExplore_selectorsPickTheirOwnElement(t *testing.T) {
	base := fixtureApp(t)
	ex := newExplorer()
	in := explorer.Input{
		StorageState: fixtureState,
		URL:          base + "/names.html",
	}
	out, err := ex.Explore(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ name, clicked string }{
		{"Save all", "save-all"},
		{"Save", "save"},
		{"Delete", "delete-1"}, // selectorFor takes the first "Delete" line
	} {
		sel := selectorFor(t, out.Snapshot, "button", c.name)
		in.Actions = []script.Step{{Action: script.Click{Selector: sel}}}
		got, err := ex.Explore(context.Background(), in)
		if err != nil {
			t.Fatalf("click %s: %v", sel, err)
		}
		want := regexp.MustCompile(`(?m)^- paragraph: clicked ` + regexp.QuoteMeta(c.clicked) + `$`)
		if !want.MatchString(got.Snapshot) {
			t.Errorf("%s did not click %s, snapshot:\n%s", sel, c.clicked, got.Snapshot)
		}
	}
}

// FR-017 edge case: a failing action names its step and still returns the page.
func TestExplore_failedActionKeepsSnapshot(t *testing.T) {
	base := fixtureApp(t)
	out, err := newExplorer().Explore(context.Background(), explorer.Input{
		StorageState: fixtureState,
		URL:          base + "/projects.html",
		Actions:      []script.Step{{Action: script.Click{Selector: "#does-not-exist"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "step 1 click #does-not-exist") {
		t.Fatalf("error = %v, want step 1 click #does-not-exist", err)
	}
	if !strings.Contains(out.Snapshot, `role=button[name="New project"]`) {
		t.Errorf("snapshot at failure missing:\n%s", out.Snapshot)
	}
}
