//go:build linux

/*
Package plugins tests Linux process supervision, startup boundaries, and shutdown behavior.
*/
package plugins

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestExecSupervisorStartsAndStopsPluginProcess(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewExecSupervisor(root)
	if err != nil {
		t.Fatalf("create supervisor: %v", err)
	}
	t.Setenv("PLUGIN_TEST_SECRET", "must-not-reach-plugin")
	artifact := writePluginScript(t, root, "#!/bin/sh\n[ \"$HOMELAB_PLUGIN_ID\" = test.plugin ] || exit 2\n[ \"$HOMELAB_PLUGIN_VERSION\" = 1.0.0 ] || exit 3\n[ -z \"${PLUGIN_TEST_SECRET+x}\" ] || exit 4\n: > \"$HOMELAB_PLUGIN_SOCKET\"\nexec /bin/sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, err := supervisor.Start(ctx, testProcessSpec(artifact))
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	readyDeadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(process.SocketPath()); err == nil {
			break
		}
		select {
		case <-process.(*execProcess).done:
			t.Fatalf("plugin exited before receiving its socket path: %v", process.(*execProcess).exitError())
		default:
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("plugin did not receive its private socket path")
		}
		time.Sleep(time.Millisecond)
	}
	info, err := os.Stat(filepath.Dir(process.SocketPath()))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory must be private, info=%v err=%v", info, err)
	}
	if err := process.Stop(context.Background()); err == nil {
		t.Fatal("plugin stop without a deadline was accepted")
	}
	if err := process.Stop(ctx); err != nil {
		t.Fatalf("stop plugin: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(process.SocketPath())); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime directory should be removed after exit, err=%v", err)
	}
}

func TestExecSupervisorKillsPluginAfterStopDeadline(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewExecSupervisor(root)
	if err != nil {
		t.Fatalf("create supervisor: %v", err)
	}
	artifact := writePluginScript(t, root, "#!/bin/sh\n: > \"$HOMELAB_PLUGIN_SOCKET.ready\"\ntrap '' TERM\nwhile :; do :; done\n")
	startCtx, cancelStart := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStart()
	process, err := supervisor.Start(startCtx, testProcessSpec(artifact))
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	readyDeadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(process.SocketPath() + ".ready"); err == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("plugin helper did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelStop()
	if err := process.Stop(stopCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop should report its expired deadline, got %v", err)
	}
	select {
	case <-process.(*execProcess).done:
	case <-time.After(3 * time.Second):
		t.Fatal("plugin process did not exit after forced termination")
	}
}

func TestExecSupervisorRetriesStopAfterSignalFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewExecSupervisor(root)
	if err != nil {
		t.Fatalf("create supervisor: %v", err)
	}
	artifact := writePluginScript(t, root, "#!/bin/sh\n: > \"$HOMELAB_PLUGIN_SOCKET.ready\"\nexec /bin/sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	processValue, err := supervisor.Start(ctx, testProcessSpec(artifact))
	if err != nil {
		t.Fatalf("start plugin: %v", err)
	}
	process := processValue.(*execProcess)
	readyDeadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(process.SocketPath() + ".ready"); err == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("plugin helper did not become ready")
		}
		time.Sleep(time.Millisecond)
	}

	var signals atomic.Uint32
	process.signalGroup = func(pid int, signal syscall.Signal) error {
		if signals.Add(1) == 1 {
			return syscall.EPERM
		}
		return syscall.Kill(pid, signal)
	}
	if err := process.Stop(ctx); err == nil {
		t.Fatal("first stop should report the injected signal failure")
	}
	if err := process.Stop(ctx); err != nil {
		t.Fatalf("second stop should retry the process-group signal: %v", err)
	}
	if signals.Load() != 2 {
		t.Fatalf("process-group signal calls = %d, want 2", signals.Load())
	}
}

func TestExecSupervisorAbortsProcessWhenStartupContextExpiresBeforeReturn(t *testing.T) {
	command := exec.Command("/bin/sleep", "30")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	process := &execProcess{command: command, runtimeDir: t.TempDir(), done: make(chan struct{}), signalGroup: syscall.Kill}
	go process.wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if err := abortStartedProcess(ctx, process); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup cancellation error = %v", err)
	}
	select {
	case <-process.done:
	case <-time.After(time.Second):
		t.Fatal("process survived cancellation at the startup boundary")
	}
}

func TestExecSupervisorRejectsUnsafeRuntimeAndArtifact(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewExecSupervisor(root); err == nil {
		t.Fatal("group-readable runtime directory was accepted")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	supervisor, err := NewExecSupervisor(root)
	if err != nil {
		t.Fatalf("create supervisor: %v", err)
	}
	artifact := writePluginScript(t, root, "#!/bin/sh\nexit 0\n")
	link := filepath.Join(root, "plugin-link")
	if err := os.Symlink(artifact, link); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := supervisor.Start(ctx, testProcessSpec(link)); err == nil {
		t.Fatal("symlink plugin artifact was accepted")
	}
	badSpec := testProcessSpec(artifact)
	badSpec.Manifest.Platform.OS = "darwin"
	if _, err := supervisor.Start(ctx, badSpec); err == nil {
		t.Fatal("incompatible plugin platform was accepted")
	}
}

func testProcessSpec(artifact string) ProcessSpec {
	return ProcessSpec{Manifest: Manifest{
		PluginID: "test.plugin", Version: "1.0.0",
		Platform: Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
	}, ArtifactPath: artifact}
}

func writePluginScript(t *testing.T, directory, contents string) string {
	t.Helper()
	path := filepath.Join(directory, "plugin")
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write plugin script: %v", err)
	}
	return path
}
