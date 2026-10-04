package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"screencaster/core/failure"
	"screencaster/core/lock"
	"screencaster/core/renderer"
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
			name:       "config missing",
			args:       []string{"render", "demos/a.yaml"},
			err:        errors.New("config not found: /work/screencaster.yaml"),
			wantCode:   1,
			wantStderr: "config not found: /work/screencaster.yaml\n",
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
