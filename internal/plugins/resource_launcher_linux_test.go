//go:build linux

/*
Package plugins tests that the Linux launcher installs process limits before
replacing itself with a plugin artifact.
*/
package plugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcessLimitsRequireEveryBoundary(t *testing.T) {
	valid := ProcessLimits{
		MaxOpenFiles: 64, MaxProcesses: 32, MaxFileBytes: 1 << 20,
		MaxAddressSpaceBytes: 1 << 30, MaxCPUSeconds: 30,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid limits rejected: %v", err)
	}
	invalid := []ProcessLimits{valid, valid, valid, valid, valid}
	invalid[0].MaxOpenFiles = 0
	invalid[1].MaxProcesses = 0
	invalid[2].MaxFileBytes = 0
	invalid[3].MaxAddressSpaceBytes = 0
	invalid[4].MaxCPUSeconds = 0
	for index, limits := range invalid {
		if err := limits.Validate(); err == nil {
			t.Fatalf("zero process limit at index %d was accepted", index)
		}
	}
	if err := RunPluginLauncher(launcherArguments("relative-plugin", valid)); err == nil {
		t.Fatal("relative plugin artifact was accepted")
	}
}

func TestRunPluginLauncherAppliesLimits(t *testing.T) {
	if os.Getenv("HOMELAB_LAUNCHER_TEST_CHILD") == "1" {
		separator := -1
		for index, argument := range os.Args {
			if argument == "--" {
				separator = index
				break
			}
		}
		if separator < 0 {
			os.Exit(2)
		}
		if err := RunPluginLauncher(os.Args[separator+1:]); err != nil {
			os.Exit(3)
		}
		return
	}

	root := t.TempDir()
	output := filepath.Join(root, "limits")
	artifact := filepath.Join(root, "plugin")
	script := "#!/bin/sh\ngrep 'Max open files' /proc/self/limits > \"$LIMIT_OUTPUT\"\ngrep '^NoNewPrivs:[[:space:]]*1$' /proc/self/status >> \"$LIMIT_OUTPUT\"\n"
	if err := os.WriteFile(artifact, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	limits := ProcessLimits{
		MaxOpenFiles: 37, MaxProcesses: 256, MaxFileBytes: 1 << 20,
		MaxAddressSpaceBytes: 1 << 30, MaxCPUSeconds: 30,
	}
	arguments := append([]string{"-test.run=^TestRunPluginLauncherAppliesLimits$", "--"}, launcherArguments(artifact, limits)...)
	command := exec.Command(os.Args[0], arguments...)
	command.Env = append(os.Environ(), "HOMELAB_LAUNCHER_TEST_CHILD=1", "LIMIT_OUTPUT="+output)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("launcher subprocess failed: %v: %s", err, combined)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "37") || !strings.Contains(text, "NoNewPrivs:") {
		t.Fatalf("launcher did not apply expected boundaries: %q", text)
	}
}
