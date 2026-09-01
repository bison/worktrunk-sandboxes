package cmd

import (
	"strings"
	"testing"
)

func TestEnterArgs(t *testing.T) {
	got := enterArgs("myproject-my-feature", "zsh")
	want := "exec -it myproject-my-feature zsh -l"
	if strings.Join(got, " ") != want {
		t.Errorf("explicit shell: argv = %q, want %q", strings.Join(got, " "), want)
	}

	got = enterArgs("myproject-my-feature", "")
	if len(got) != 6 || got[3] != "sh" || got[4] != "-c" {
		t.Fatalf("default shell: argv = %q", got)
	}
	// The fallback script must resolve the shell inside the container, not
	// on the host, and must land in a login shell either way.
	if !strings.Contains(got[5], "getent passwd") || !strings.Contains(got[5], `-l`) {
		t.Errorf("default shell script = %q", got[5])
	}
}
