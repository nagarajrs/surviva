package main

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
)

// TestExitCodeAndErr exercises exitCodeAndErr against a real subprocess exit
// (both clean and nonzero), re-execing this same test binary -- the
// standard, portable way to get a genuine *exec.ExitError without shelling
// out to a platform-specific "false"/"exit 3" command.
func TestExitCodeAndErr(t *testing.T) {
	if code := os.Getenv("SURVIVA_TEST_HELPER_EXIT"); code != "" {
		n, _ := strconv.Atoi(code)
		os.Exit(n)
	}

	cases := []struct {
		exitCode int
		wantMsg  string
	}{
		{0, ""},
		{3, ""},
	}
	for _, tc := range cases {
		cmd := exec.Command(os.Args[0], "-test.run=TestExitCodeAndErr")
		cmd.Env = append(os.Environ(), "SURVIVA_TEST_HELPER_EXIT="+strconv.Itoa(tc.exitCode))
		waitErr := cmd.Run()

		gotCode, gotMsg := exitCodeAndErr(waitErr)
		if gotCode != tc.exitCode || gotMsg != tc.wantMsg {
			t.Errorf("exitCodeAndErr(exit %d) = (%d, %q), want (%d, %q)", tc.exitCode, gotCode, gotMsg, tc.exitCode, tc.wantMsg)
		}
	}
}

func TestExitCodeAndErrNonExitError(t *testing.T) {
	// A command that never starts (e.g. not found) fails with something
	// other than *exec.ExitError.
	err := exec.Command("surviva-test-nonexistent-binary-xyz").Run()
	if err == nil {
		t.Fatal("expected an error starting a nonexistent binary")
	}
	code, msg := exitCodeAndErr(err)
	if code != 1 || msg == "" {
		t.Errorf("exitCodeAndErr(non-ExitError) = (%d, %q), want (1, <non-empty>)", code, msg)
	}
}
