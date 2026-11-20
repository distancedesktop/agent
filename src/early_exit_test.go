package main

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"distancedesktop/agent/src/backend"
)

func TestTokenEarlyExits(t *testing.T) {
	if mode := os.Getenv("DISTANCE_TEST_EARLY_EXIT"); mode != "" {
		backend.Register(fakeBackend{})
		flag.CommandLine = flag.NewFlagSet("agent", flag.ExitOnError)
		os.Args = []string{"agent", "--backend", "fake", "--token-file", os.Getenv("DISTANCE_TEST_TOKEN_PATH"), "--" + mode}
		main()
		return
	}
	for _, mode := range []string{"dry-run", "fingerprint"} {
		t.Run(mode, func(t *testing.T) {
			for _, invalid := range []bool{false, true} {
				dir := t.TempDir()
				path := filepath.Join(dir, "viewer-token")
				if invalid {
					if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command(os.Args[0], "-test.run=^TestTokenEarlyExits$")
				cmd.Env = append(os.Environ(), "DISTANCE_TEST_EARLY_EXIT="+mode, "DISTANCE_TEST_TOKEN_PATH="+path, "HOME="+dir, "XDG_CONFIG_HOME="+dir)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", mode, err, out)
				}
				if !invalid {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("early exit touched token: %v", err)
					}
				}
			}
		})
	}
}
