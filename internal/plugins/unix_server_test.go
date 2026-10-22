/*
Package plugins tests private Unix-socket serving and cleanup behavior.
*/
package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestServeUnixPluginRestrictsSocketAndCleansUp(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "runtime")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(directory, "plugin.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serveResult := make(chan error, 1)
	go func() { serveResult <- ServeUnixPlugin(ctx, socketPath, testPlugin{minHostMajor: 1, maxHostMajor: 1}) }()
	deadline := time.Now().Add(2 * time.Second)
	var socketInfo os.FileInfo
	for time.Now().Before(deadline) {
		var err error
		socketInfo, err = os.Lstat(socketPath)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if socketInfo == nil {
		cancel()
		t.Fatal("plugin socket was not created")
	}
	if socketInfo.Mode()&os.ModeSocket == 0 || socketInfo.Mode().Perm() != 0o600 {
		cancel()
		t.Fatalf("plugin socket mode = %v", socketInfo.Mode())
	}
	host, err := NewGRPCHost(socketPath)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	callCtx, cancelCall := context.WithTimeout(context.Background(), time.Second)
	if _, err := host.Handshake(callCtx, HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1}); err != nil {
		cancelCall()
		cancel()
		t.Fatal(err)
	}
	cancelCall()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := host.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-serveResult:
		if err != nil {
			t.Fatalf("ServeUnixPlugin() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin server did not stop after context cancellation")
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin socket remains after shutdown: %v", err)
	}
}

func TestServeUnixPluginRejectsUnsafeRuntimePaths(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ServeUnixPlugin(ctx, "relative.sock", testPlugin{}); err == nil {
		t.Fatal("relative socket path was accepted")
	}
	root := testutil.TempDir(t)
	permissive := filepath.Join(root, "permissive")
	if err := os.Mkdir(permissive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(permissive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ServeUnixPlugin(ctx, filepath.Join(permissive, "plugin.sock"), testPlugin{}); err == nil {
		t.Fatal("group-accessible runtime directory was accepted")
	}
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(private, "plugin.sock")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ServeUnixPlugin(ctx, existing, testPlugin{}); err == nil {
		t.Fatal("existing socket path was overwritten")
	}
	contents, err := os.ReadFile(existing)
	if err != nil || string(contents) != "keep" {
		t.Fatalf("existing path changed: %q, %v", contents, err)
	}
}

func TestRemoveOwnedSocketPreservesReplacementPath(t *testing.T) {
	directory := testutil.TempDir(t)
	socketPath := filepath.Join(directory, "plugin.sock")
	if err := os.WriteFile(socketPath, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	createdInfo, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	originalPath := filepath.Join(directory, "original.sock")
	if err := os.Rename(socketPath, originalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeOwnedSocket(socketPath, createdInfo)
	contents, err := os.ReadFile(socketPath)
	if err != nil || string(contents) != "replacement" {
		t.Fatalf("replacement path = %q, %v", contents, err)
	}
}
