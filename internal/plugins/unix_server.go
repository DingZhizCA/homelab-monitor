/*
Package plugins provides the plugin-side Unix-socket gRPC serving helper.

The helper binds only inside an existing private runtime directory, refuses to
replace an existing path, and removes only the socket inode it created. It does
not provide process sandboxing or peer credentials across distinct OS users.
*/
package plugins

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"
)

// ServeUnixPlugin serves one implementation on a private Unix socket until cancellation.
func ServeUnixPlugin(ctx context.Context, socketPath string, implementation PluginContract) error {
	if ctx == nil || implementation == nil {
		return errors.New("plugin context and implementation are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(socketPath) {
		return errors.New("plugin socket path must be absolute")
	}
	directory := filepath.Dir(socketPath)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect plugin runtime directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("plugin runtime directory must be private and must not be a symbolic link")
	}
	if _, err := os.Lstat(socketPath); err == nil {
		return errors.New("plugin socket path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect plugin socket path: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on plugin Unix socket: %w", err)
	}
	socketInfo, err := os.Lstat(socketPath)
	if err != nil || socketInfo.Mode()&os.ModeSocket == 0 {
		_ = listener.Close()
		if err != nil {
			return fmt.Errorf("inspect created plugin socket: %w", err)
		}
		return errors.New("created plugin path is not a Unix socket")
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		removeOwnedSocket(socketPath, socketInfo)
		return fmt.Errorf("restrict plugin socket permissions: %w", err)
	}
	defer removeOwnedSocket(socketPath, socketInfo)

	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxPluginRPCMessageBytes),
		grpc.MaxSendMsgSize(maxPluginRPCMessageBytes),
	)
	if err := RegisterGRPCPlugin(server, implementation); err != nil {
		_ = listener.Close()
		return err
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	select {
	case err := <-serveResult:
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	case <-ctx.Done():
		server.Stop()
		err := <-serveResult
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			return err
		}
		return nil
	}
}

func removeOwnedSocket(path string, created os.FileInfo) {
	current, err := os.Lstat(path)
	if err == nil && os.SameFile(created, current) {
		_ = os.Remove(path)
	}
}
