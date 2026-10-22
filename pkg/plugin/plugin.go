/*
Package plugin is the public SDK surface for native Homelab Monitor collectors.

It exposes the transport-independent contract and a bounded gRPC-over-Unix
server entry point. Plugin authors still own configuration, collection logic,
and any permissions granted to the plugin process.
*/
package plugin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
	"google.golang.org/grpc"
)

const maxRPCMessageBytes = 4 << 20

type (
	Plugin              = plugins.PluginContract
	HostIdentity        = plugins.HostIdentity
	HostCapabilities    = plugins.HostCapabilities
	ProtocolRange       = plugins.ProtocolRange
	CollectorDescriptor = plugins.CollectorDescriptor
	MetricDescriptor    = plugins.MetricDescriptor
	MetricType          = plugins.MetricType
	CollectRequest      = plugins.CollectRequest
	CollectResult       = plugins.CollectResult
	MetricSample        = plugins.MetricSample
	DataQuality         = plugins.DataQuality
	HealthStatus        = plugins.HealthStatus
	RPCError            = plugins.RPCError
	RPCCode             = plugins.RPCCode
)

const (
	MetricCounter = plugins.MetricCounter
	MetricGauge   = plugins.MetricGauge
	MetricState   = plugins.MetricState
	MetricEvent   = plugins.MetricEvent

	QualityValid     = plugins.QualityValid
	QualityPartial   = plugins.QualityPartial
	QualityEstimated = plugins.QualityEstimated
	QualityMissing   = plugins.QualityMissing
	QualityReset     = plugins.QualityReset
	QualityStale     = plugins.QualityStale

	RPCInvalidArgument      = plugins.RPCInvalidArgument
	RPCPermissionDenied     = plugins.RPCPermissionDenied
	RPCResourceExhausted    = plugins.RPCResourceExhausted
	RPCDeadlineExceeded     = plugins.RPCDeadlineExceeded
	RPCUnavailable          = plugins.RPCUnavailable
	RPCIncompatibleProtocol = plugins.RPCIncompatibleProtocol
	RPCInternal             = plugins.RPCInternal

	HostFeatureMetricSampleLabels = plugins.HostFeatureMetricSampleLabels
)

// HostSupportsFeature reports whether the Plugin Host advertised a capability.
func HostSupportsFeature(identity HostIdentity, feature string) bool {
	for _, supported := range identity.HostFeatures {
		if supported == feature {
			return true
		}
	}
	return false
}

// ServeUnix serves one plugin contract on a private Unix socket until ctx ends.
func ServeUnix(ctx context.Context, socketPath string, implementation Plugin) error {
	if ctx == nil || implementation == nil {
		return errors.New("plugin context and implementation are required")
	}
	if runtime.GOOS != "linux" {
		return errors.New("native plugin Unix socket serving is supported only on Linux")
	}
	if !filepath.IsAbs(socketPath) || len(socketPath) >= 108 {
		return errors.New("plugin socket path must be absolute and fit the Linux Unix socket limit")
	}
	parent := filepath.Dir(socketPath)
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("plugin socket parent must be an existing private non-symlink directory")
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
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0o600); err != nil {
		return fmt.Errorf("secure plugin Unix socket: %w", err)
	}
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxRPCMessageBytes),
		grpc.MaxSendMsgSize(maxRPCMessageBytes),
	)
	if err := plugins.RegisterGRPCPlugin(server, implementation); err != nil {
		return err
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	select {
	case err := <-serveDone:
		if err != nil && !errors.Is(err, net.ErrClosed) {
			return fmt.Errorf("serve plugin RPC: %w", err)
		}
		return nil
	case <-ctx.Done():
		server.Stop()
		return ctx.Err()
	}
}
