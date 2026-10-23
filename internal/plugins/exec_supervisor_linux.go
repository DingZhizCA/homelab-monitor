//go:build linux

/*
Package plugins supervises native Linux plugin processes without elevating them.

This implementation creates a private runtime directory and provides bounded
process shutdown. Production construction routes execution through a trusted
launcher that applies inherited rlimits and no-new-privileges before exec.
*/
package plugins

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
)

const linuxUnixSocketPathLimit = 108

// ExecSupervisor starts plugin artifacts as separate, unprivileged processes.
type ExecSupervisor struct {
	runtimeRoot  string
	launcherPath string
	limits       ProcessLimits
}

// NewLimitedExecSupervisor requires a trusted launcher and explicit kernel limits.
func NewLimitedExecSupervisor(runtimeRoot, launcherPath string, limits ProcessLimits) (*ExecSupervisor, error) {
	supervisor, err := NewExecSupervisor(runtimeRoot)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(launcherPath) || limits.Validate() != nil {
		return nil, errors.New("absolute plugin launcher and positive process limits are required")
	}
	info, err := os.Lstat(launcherPath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("plugin launcher must be an executable, non-writable regular file")
	}
	parentInfo, err := os.Lstat(filepath.Dir(launcherPath))
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 || parentInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("plugin launcher parent must be a protected directory")
	}
	supervisor.launcherPath = filepath.Clean(launcherPath)
	supervisor.limits = limits
	return supervisor, nil
}

// NewExecSupervisor opens an existing private runtime directory for plugin sockets.
func NewExecSupervisor(runtimeRoot string) (*ExecSupervisor, error) {
	if !filepath.IsAbs(runtimeRoot) {
		return nil, errors.New("plugin runtime path must be absolute")
	}
	info, err := os.Lstat(runtimeRoot)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("plugin runtime path must be an existing private non-symlink directory")
	}
	return &ExecSupervisor{runtimeRoot: filepath.Clean(runtimeRoot)}, nil
}

// Start launches one executable artifact and returns its private Plugin Host socket path.
func (s *ExecSupervisor) Start(ctx context.Context, spec ProcessSpec) (Process, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !identifierPattern.MatchString(spec.Manifest.PluginID) || !versionPattern.MatchString(spec.Manifest.Version) {
		return nil, errors.New("plugin process requires a valid plugin ID and version")
	}
	if spec.Manifest.Platform.OS != runtime.GOOS || spec.Manifest.Platform.Arch != runtime.GOARCH {
		return nil, errors.New("plugin artifact platform does not match the host")
	}
	if !filepath.IsAbs(spec.ArtifactPath) {
		return nil, errors.New("plugin artifact path must be absolute")
	}
	artifactInfo, err := os.Lstat(spec.ArtifactPath)
	if err != nil || artifactInfo.Mode()&os.ModeSymlink != 0 || !artifactInfo.Mode().IsRegular() || artifactInfo.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("plugin artifact must be an executable regular non-symlink file")
	}

	runtimeDir, err := os.MkdirTemp(s.runtimeRoot, "p-")
	if err != nil {
		return nil, fmt.Errorf("create plugin runtime directory: %w", err)
	}
	if err := os.Chmod(runtimeDir, 0o700); err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("secure plugin runtime directory: %w", err)
	}
	socketPath := filepath.Join(runtimeDir, "s")
	if len(socketPath) >= linuxUnixSocketPathLimit {
		_ = os.RemoveAll(runtimeDir)
		return nil, errors.New("plugin runtime path is too long for a Linux Unix socket")
	}

	command := exec.Command(spec.ArtifactPath)
	if s.launcherPath != "" {
		command = exec.Command(s.launcherPath, launcherArguments(spec.ArtifactPath, s.limits)...)
	}
	command.Env = []string{
		"HOMELAB_PLUGIN_ID=" + spec.Manifest.PluginID,
		"HOMELAB_PLUGIN_VERSION=" + spec.Manifest.Version,
		"HOMELAB_PLUGIN_SOCKET=" + socketPath,
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := command.Start(); err != nil {
		_ = os.RemoveAll(runtimeDir)
		return nil, fmt.Errorf("start plugin artifact: %w", err)
	}
	process := &execProcess{command: command, runtimeDir: runtimeDir, done: make(chan struct{}), signalGroup: syscall.Kill}
	go process.wait()
	if err := abortStartedProcess(ctx, process); err != nil {
		return nil, err
	}
	return process, nil
}

func abortStartedProcess(ctx context.Context, process *execProcess) error {
	if err := ctx.Err(); err != nil {
		killErr := syscall.Kill(-process.command.Process.Pid, syscall.SIGKILL)
		if killErr != nil {
			processKillErr := process.command.Process.Kill()
			if errors.Is(killErr, syscall.ESRCH) || errors.Is(processKillErr, os.ErrProcessDone) {
				killErr = processKillErr
			} else {
				killErr = errors.Join(killErr, processKillErr)
			}
		}
		<-process.done
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return errors.Join(err, fmt.Errorf("terminate plugin after startup deadline: %w", killErr))
		}
		return err
	}
	return nil
}

type execProcess struct {
	command    *exec.Cmd
	runtimeDir string
	done       chan struct{}

	mu          sync.Mutex
	waitErr     error
	stopped     bool
	signalGroup func(int, syscall.Signal) error
}

func (p *execProcess) SocketPath() string {
	return filepath.Join(p.runtimeDir, "s")
}

func (p *execProcess) wait() {
	err := p.command.Wait()
	p.mu.Lock()
	p.waitErr = err
	p.mu.Unlock()
	_ = os.RemoveAll(p.runtimeDir)
	close(p.done)
}

func (p *execProcess) Stop(ctx context.Context) error {
	if err := requirePluginDeadline(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		select {
		case <-p.done:
			return p.exitError()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.stopped = true
	p.mu.Unlock()

	if err := p.signalGroup(-p.command.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		p.mu.Lock()
		p.stopped = false
		p.mu.Unlock()
		return fmt.Errorf("signal plugin process group: %w", err)
	}
	select {
	case <-p.done:
		// A plugin may leave helper processes in its process group after its leader exits.
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
		return p.exitError()
	case <-ctx.Done():
		_ = syscall.Kill(-p.command.Process.Pid, syscall.SIGKILL)
		return ctx.Err()
	}
}

func (p *execProcess) exitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(p.waitErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return nil
		}
	}
	return fmt.Errorf("plugin process exited: %w", p.waitErr)
}
