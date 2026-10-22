/*
Package main implements the homelab-server executable entry point.

本文件职责：
- 创建并配置 homelab-server 的 HTTP 路由器
- 注册 Server 提供的 HTTP API 路由
- 启动 HTTP Server
- 处理进程启动阶段的错误

本文件不负责：
- Linux 系统指标采集
- 指标 schema、保留和聚合策略
- Web Dashboard 页面实现

数据链路：
homelab-agent -> HTTP API -> homelab-server -> Web Dashboard
*/
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/server"
	"github.com/DingZhizCA/homelab-monitor/internal/storage"
)

type metricStorageConfig struct {
	directory    string
	maxSnapshots int
	maxBodyBytes int64
}

func main() {
	addr := flag.String("listen", ":8080", "HTTP(S) listen address")
	webDir := flag.String("web-dir", "", "directory containing the built Dashboard (disabled when empty)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate for HTTPS (both TLS flags are required together)")
	tlsKey := flag.String("tls-key", "", "TLS private key for HTTPS (both TLS flags are required together)")
	agentCredentials := flag.String("agent-credentials", "", "private JSON file containing per-device token SHA-256 verifiers")
	storageDirectory := flag.String("storage-dir", "", "private local SSD directory for core.db and metrics.db (disabled when empty)")
	metricMaxSnapshots := flag.Int("metrics-batch-max-snapshots", 0, "maximum snapshots accepted in one persisted Agent batch")
	metricMaxBodyBytes := flag.Int64("metrics-batch-max-body-bytes", 0, "maximum encoded bytes accepted by the persisted Agent batch endpoint")
	flag.Parse()
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("component=server level=ERROR msg=both tls-cert and tls-key are required")
	}
	if *agentCredentials == "" {
		log.Printf("component=server level=WARN msg=%q", "Agent device authentication is disabled for compatibility; metrics requests are not bound to device credentials")
	}
	if *storageDirectory != "" && *agentCredentials == "" {
		log.Fatal("component=server level=ERROR msg=persisted metrics batches require Agent device credentials")
	}
	if *webDir != "" {
		if info, err := os.Stat(*webDir); err != nil || !info.IsDir() {
			log.Fatalf("component=server level=ERROR msg=%q", "web-dir must be a readable directory")
		}
	}
	var deviceAuthorizer server.DeviceAuthorizer
	if *agentCredentials != "" {
		if *tlsCert == "" {
			log.Fatal("component=server level=ERROR msg=agent credentials require configured HTTPS")
		}
		credentials, err := server.LoadDeviceCredentials(*agentCredentials)
		if err != nil {
			log.Fatalf("component=server level=ERROR msg=%q", err)
		}
		deviceAuthorizer = credentials
	}
	store := server.NewStore()
	broker := server.NewBroker()
	presence := server.NewPresence()
	handler := server.NewHandlerWithDeviceAuthorizer(store, broker, deviceAuthorizer, presence)
	databases, metricsBatch, err := openMetricStorage(context.Background(), metricStorageConfig{
		directory:    *storageDirectory,
		maxSnapshots: *metricMaxSnapshots,
		maxBodyBytes: *metricMaxBodyBytes,
	}, handler)
	if err != nil {
		log.Fatalf("component=server level=ERROR msg=%q", err)
	}
	if databases != nil {
		defer func() {
			if err := databases.Close(); err != nil {
				log.Printf("component=server level=ERROR msg=%q", err)
			}
		}()
		log.Printf("component=server level=INFO storage=%q msg=%q", *storageDirectory, "SQLite storage initialized")
	}
	router, err := server.NewRouter(server.RouterOptions{API: handler, MetricsBatch: metricsBatch, WebDirectory: *webDir})
	if err != nil {
		if databases != nil {
			_ = databases.Close()
		}
		log.Fatalf("component=server level=ERROR msg=%q", err)
	}

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("component=server level=ERROR msg=%q", err)
		}
	}()
	log.Printf("component=server level=INFO listen=%q msg=%q", *addr, "started")
	var serveErr error
	if *tlsCert != "" {
		serveErr = httpServer.ListenAndServeTLS(*tlsCert, *tlsKey)
	} else {
		serveErr = httpServer.ListenAndServe()
	}
	if err := serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("component=server level=INFO msg=stopped")
}

func openMetricStorage(ctx context.Context, config metricStorageConfig, api *server.Handler) (*storage.Databases, *server.MetricsBatchHandler, error) {
	disabled := config.directory == "" && config.maxSnapshots == 0 && config.maxBodyBytes == 0
	if disabled {
		return nil, nil, nil
	}
	if config.directory == "" || config.maxSnapshots < 1 || config.maxBodyBytes < 1 {
		return nil, nil, errors.New("storage-dir and positive metrics batch limits must be configured together")
	}
	databases, err := storage.Open(ctx, config.directory)
	if err != nil {
		return nil, nil, err
	}
	writer, err := storage.NewMetricBatchWriter(databases.Metrics, config.maxSnapshots)
	if err != nil {
		_ = databases.Close()
		return nil, nil, err
	}
	handler, err := server.NewMetricsBatchHandler(api, writer, config.maxSnapshots, config.maxBodyBytes)
	if err != nil {
		_ = databases.Close()
		return nil, nil, err
	}
	return databases, handler, nil
}
