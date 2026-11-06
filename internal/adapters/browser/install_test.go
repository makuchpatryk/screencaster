package browser

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// The one command setup runs through the driver is fixed: no argument comes
// from a pin, the environment or user input (decision 82).
func TestInstallDeps_runsTheDriverWithAFixedArgumentList(t *testing.T) {
	var gotName string
	var gotArgs, gotEnv []string
	orig := run
	t.Cleanup(func() { run = orig })
	run = func(_ context.Context, _ io.Writer, env []string, name string, args ...string) error {
		gotName, gotArgs, gotEnv = name, args, env
		return nil
	}
	if err := (Installer{}).InstallDeps(context.Background(), "/opt/sc/playwright-driver", io.Discard); err != nil {
		t.Fatal(err)
	}
	if want := "/opt/sc/playwright-driver/node"; gotName != want {
		t.Errorf("command %q, want %q", gotName, want)
	}
	wantArgs := []string{"/opt/sc/playwright-driver/package/cli.js", "install-deps", "chromium"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("args %v, want %v", gotArgs, wantArgs)
	}
	if !reflect.DeepEqual(gotEnv, []string{"DEBIAN_FRONTEND=noninteractive"}) {
		t.Errorf("env %v, want only DEBIAN_FRONTEND=noninteractive", gotEnv)
	}
}

func TestInstallDeps_failureNamesTheCommand(t *testing.T) {
	orig := run
	t.Cleanup(func() { run = orig })
	run = func(context.Context, io.Writer, []string, string, ...string) error {
		return errors.New("exit status 100")
	}
	err := (Installer{}).InstallDeps(context.Background(), "/d", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "install-deps chromium") || !strings.Contains(err.Error(), "exit status 100") {
		t.Errorf("error %v, want one naming the command and the cause", err)
	}
}
