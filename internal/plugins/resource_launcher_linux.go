//go:build linux

/*
Package plugins provides the race-free Linux launcher used before plugin exec.

The launcher applies non-privileged hard resource limits and no-new-privileges
to itself, then replaces itself with the already verified plugin artifact.
*/
package plugins

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"golang.org/x/sys/unix"
)

// ProcessLimits are hard kernel limits inherited by a plugin and its descendants.
type ProcessLimits struct {
	MaxOpenFiles         uint64
	MaxProcesses         uint64
	MaxFileBytes         uint64
	MaxAddressSpaceBytes uint64
	MaxCPUSeconds        uint64
}

func (l ProcessLimits) Validate() error {
	if l.MaxOpenFiles < 1 || l.MaxProcesses < 1 || l.MaxFileBytes < 1 || l.MaxAddressSpaceBytes < 1 || l.MaxCPUSeconds < 1 {
		return errors.New("all plugin process limits must be positive")
	}
	return nil
}

// RunPluginLauncher parses an internal launcher request, applies limits, and execs the artifact.
func RunPluginLauncher(arguments []string) error {
	flags := flag.NewFlagSet("homelab-plugin-launcher", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	artifact := flags.String("artifact", "", "verified plugin artifact")
	openFiles := flags.Uint64("max-open-files", 0, "open file limit")
	processes := flags.Uint64("max-processes", 0, "process limit")
	fileBytes := flags.Uint64("max-file-bytes", 0, "created file byte limit")
	addressBytes := flags.Uint64("max-address-space-bytes", 0, "virtual address byte limit")
	cpuSeconds := flags.Uint64("max-cpu-seconds", 0, "cumulative CPU second limit")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return errors.New("invalid plugin launcher arguments")
	}
	limits := ProcessLimits{MaxOpenFiles: *openFiles, MaxProcesses: *processes, MaxFileBytes: *fileBytes, MaxAddressSpaceBytes: *addressBytes, MaxCPUSeconds: *cpuSeconds}
	if err := limits.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(*artifact) {
		return errors.New("plugin launcher artifact path must be absolute")
	}
	info, err := os.Lstat(*artifact)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("plugin launcher artifact must be an executable regular non-symlink file")
	}
	for _, limit := range []struct {
		resource int
		value    uint64
	}{
		{unix.RLIMIT_NOFILE, limits.MaxOpenFiles},
		{unix.RLIMIT_NPROC, limits.MaxProcesses},
		{unix.RLIMIT_FSIZE, limits.MaxFileBytes},
		{unix.RLIMIT_AS, limits.MaxAddressSpaceBytes},
		{unix.RLIMIT_CPU, limits.MaxCPUSeconds},
	} {
		if err := unix.Setrlimit(limit.resource, &unix.Rlimit{Cur: limit.value, Max: limit.value}); err != nil {
			return fmt.Errorf("apply plugin resource limit %d: %w", limit.resource, err)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("apply plugin no-new-privileges: %w", err)
	}
	return syscall.Exec(*artifact, []string{*artifact}, os.Environ())
}

func launcherArguments(artifact string, limits ProcessLimits) []string {
	return []string{
		"--artifact", artifact,
		"--max-open-files", strconv.FormatUint(limits.MaxOpenFiles, 10),
		"--max-processes", strconv.FormatUint(limits.MaxProcesses, 10),
		"--max-file-bytes", strconv.FormatUint(limits.MaxFileBytes, 10),
		"--max-address-space-bytes", strconv.FormatUint(limits.MaxAddressSpaceBytes, 10),
		"--max-cpu-seconds", strconv.FormatUint(limits.MaxCPUSeconds, 10),
	}
}
