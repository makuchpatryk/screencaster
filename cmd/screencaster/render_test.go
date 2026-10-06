package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"screencaster/internal/adapters/lock"
	"screencaster/internal/app/renderer"
	"screencaster/internal/domain/failure"
)

func TestRun_langFlagOverridesScript(t *testing.T) { // FR-011, BR-002
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"no flag keeps the script's languages", []string{"render", "demos/a.yaml"}, nil},
		{"comma list", []string{"render", "demos/a.yaml", "--lang", "en,pl"}, []string{"en", "pl"}},
		{"repeated flag", []string{"render", "--lang", "pl", "--lang", "en", "demos/a.yaml"}, []string{"pl", "en"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got renderer.Request
			fake := func(_ context.Context, req renderer.Request) ([]renderer.Output, error) {
				got = req
				return nil, nil
			}
			if code := run(context.Background(), tt.args, "/work", fake, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if !reflect.DeepEqual(got.LangOverride, tt.want) {
				t.Errorf("LangOverride = %q, want %q", got.LangOverride, tt.want)
			}
			if got.WorkDir != "/work" || got.ScriptPath != "demos/a.yaml" {
				t.Errorf("request = %+v", got)
			}
		})
	}
}

func TestRun_exitCodeAndOutput(t *testing.T) { // FR-011
	step := 4
	tests := []struct {
		name       string
		args       []string
		outs       []renderer.Output
		err        error
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "success prints every path",
			args:       []string{"render", "demos/a.yaml"},
			outs:       []renderer.Output{{Lang: "en", Path: "/work/output/a.en.mp4"}, {Lang: "pl", Path: "/work/output/a.pl.mp4"}},
			wantStdout: "/work/output/a.en.mp4\n/work/output/a.pl.mp4\n",
		},
		{
			name:       "validation errors, one per line",
			args:       []string{"render", "demos/a.yaml"},
			err:        failure.ValidationErrors{{Pointer: "/steps/0/narration", Message: "missing narration for pl"}, {Message: "voice not installed: x"}},
			wantCode:   1,
			wantStderr: "/steps/0/narration: missing narration for pl\nvoice not installed: x\n",
		},
		{
			name:       "step failure",
			args:       []string{"render", "demos/a.yaml"},
			err:        &failure.Failure{Step: &step, Lang: "en", Action: "click", Target: "#x", Message: "timeout"},
			wantCode:   1,
			wantStderr: "step 4 (en) click #x: timeout\n",
		},
		{
			name:       "lock held",
			args:       []string{"render", "demos/a.yaml"},
			err:        errRenderRunning,
			wantCode:   1,
			wantStderr: "another render is running\n",
		},
		{
			name:     "missing script argument",
			args:     []string{"render"},
			wantCode: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := func(context.Context, renderer.Request) ([]renderer.Output, error) { return tt.outs, tt.err }
			var stdout, stderr bytes.Buffer

			code := run(context.Background(), tt.args, "/work", fake, &stdout, &stderr)
			if code != tt.wantCode {
				t.Errorf("exit = %d, want %d", code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if tt.wantStderr != "" && stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRun_warnsAboutScreencasterYAML(t *testing.T) { // decision 58
	const warning = "screencaster.yaml is ignored; move its fields into the demo script\n"
	tests := []struct {
		name       string
		hasConfig  bool
		wantStderr string
	}{
		{"stray file is warned about", true, warning},
		{"no file, no warning", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.hasConfig {
				if err := os.WriteFile(filepath.Join(dir, "screencaster.yaml"), []byte("baseUrl: http://x\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var called bool
			fake := func(context.Context, renderer.Request) ([]renderer.Output, error) {
				called = true
				return nil, nil
			}
			var stderr bytes.Buffer

			code := run(context.Background(), []string{"render", "demos/a.yaml"}, dir, fake, &bytes.Buffer{}, &stderr)
			if code != 0 || !called {
				t.Errorf("exit = %d, rendered = %v; the warning must not stop the render", code, called)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRun_progressGoesToStderr(t *testing.T) {
	fake := func(_ context.Context, req renderer.Request) ([]renderer.Output, error) {
		req.Progress("en", 3, 12, "click", `role=button[name="New project"]`)
		req.Progress("en", 4, 12, "wait", "")
		return nil, nil
	}
	var stdout, stderr bytes.Buffer

	run(context.Background(), []string{"render", "demos/a.yaml"}, "/work", fake, &stdout, &stderr)
	want := "[en] step 3/12 click role=button[name=\"New project\"]\n[en] step 4/12 wait\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
}

// A screenshots render has no language: progress drops the [lang] prefix and
// stdout lists every PNG, one per line (decision 72).
func TestRun_screenshotsProgressHasNoLanguageAndPathsListEveryPNG(t *testing.T) {
	fake := func(_ context.Context, req renderer.Request) ([]renderer.Output, error) {
		req.Progress("", 1, 3, "goto", "/")
		req.Progress("", 2, 3, "screenshot", "")
		req.Progress("", 3, 3, "screenshot", "#form")
		return []renderer.Output{{Path: "/work/demos/output/a/screenshots/01.png"}, {Path: "/work/demos/output/a/screenshots/02.png"}}, nil
	}
	var stdout, stderr bytes.Buffer

	if code := run(context.Background(), []string{"render", "demos/a.yaml"}, "/work", fake, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if want := "step 1/3 goto /\nstep 2/3 screenshot\nstep 3/3 screenshot #form\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if want := "/work/demos/output/a/screenshots/01.png\n/work/demos/output/a/screenshots/02.png\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRun_logLinesGoToStderrAndPathsToStdout(t *testing.T) { // decision 64
	fake := func(_ context.Context, req renderer.Request) ([]renderer.Output, error) {
		req.Log("render start: demos/a.yaml name=a")
		req.Progress("en", 1, 1, "goto", "/")
		req.Log("render done in 3s")
		return []renderer.Output{{Lang: "en", Path: "/work/demos/output/a.en.mp4"}}, nil
	}
	var stdout, stderr bytes.Buffer

	run(context.Background(), []string{"render", "demos/a.yaml"}, "/work", fake, &stdout, &stderr)
	wantErr := "render start: demos/a.yaml name=a\n[en] step 1/1 goto /\nrender done in 3s\n"
	if stderr.String() != wantErr {
		t.Errorf("stderr = %q, want %q", stderr.String(), wantErr)
	}
	if want := "/work/demos/output/a.en.mp4\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want only the output path %q", stdout.String(), want)
	}
}

func TestRun_failedRenderPrintsItsEndLineThenTheErrorOnce(t *testing.T) { // decision 64
	fake := func(_ context.Context, req renderer.Request) ([]renderer.Output, error) {
		req.Log("render failed after 4s")
		return nil, errors.New("boom")
	}
	var stdout, stderr bytes.Buffer

	if code := run(context.Background(), []string{"render", "demos/a.yaml"}, "/work", fake, &stdout, &stderr); code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if want := "render failed after 4s\nboom\n"; stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestLockRender_heldLockFailsFastWithPRDMessage(t *testing.T) { // ADR-45
	dir := t.TempDir()
	held, err := lock.TryAcquire(filepath.Join(dir, renderLockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()

	if _, err := lockRender(dir); err == nil || err.Error() != "another render is running" {
		t.Errorf("err = %v, want another render is running", err)
	}
}
