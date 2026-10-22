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
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("component=server level=ERROR msg=%q", err)
	}
}

func run() error {
	addr := flag.String("listen", ":8080", "HTTP(S) listen address")
	webDir := flag.String("web-dir", "", "directory containing the built Dashboard (disabled when empty)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate for HTTPS (both TLS flags are required together)")
	tlsKey := flag.String("tls-key", "", "TLS private key for HTTPS (both TLS flags are required together)")
	agentCredentials := flag.String("agent-credentials", "", "private JSON file containing per-device token SHA-256 verifiers")
	storageDirectory := flag.String("storage-dir", "", "private local SSD directory for core.db and metrics.db (disabled when empty)")
	metricMaxSnapshots := flag.Int("metrics-batch-max-snapshots", 0, "maximum snapshots accepted in one persisted Agent batch")
	metricMaxBodyBytes := flag.Int64("metrics-batch-max-body-bytes", 0, "maximum encoded bytes accepted by the persisted Agent batch endpoint")
	var authOrigins stringListFlag
	flag.Var(&authOrigins, "auth-origin", "allowed HTTPS browser Origin; repeat for additional Origins")
	authSessionTTL := flag.Duration("auth-session-ttl", 0, "server-side browser Session lifetime")
	authLoginMaxKeys := flag.Int("auth-login-max-keys", 0, "maximum retained login limiter identities")
	authLoginMaxAttempts := flag.Int("auth-login-max-attempts", 0, "maximum login-related attempts per identity and window")
	authLoginWindow := flag.Duration("auth-login-window", 0, "login limiter fixed-window duration")
	historyMaxRange := flag.Duration("history-max-range", 0, "maximum time range for one historical metric query")
	historyMaxPoints := flag.Int("history-max-points", 0, "maximum observations returned by one historical metric query")
	flag.Parse()
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("both tls-cert and tls-key are required")
	}
	if *agentCredentials == "" {
		log.Printf("component=server level=WARN msg=%q", "Agent device authentication is disabled for compatibility; metrics requests are not bound to device credentials")
	}
	metricConfig := metricBatchRuntimeConfig{maxSnapshots: *metricMaxSnapshots, maxBodyBytes: *metricMaxBodyBytes}
	if !metricConfig.disabled() && *agentCredentials == "" {
		return errors.New("persisted metrics batches require Agent device credentials")
	}
	authConfig := authRuntimeConfig{
		origins: authOrigins, sessionTTL: *authSessionTTL,
		loginMaxKeys: *authLoginMaxKeys, loginMaxAttempts: *authLoginMaxAttempts, loginWindow: *authLoginWindow,
		historyMaxRange: *historyMaxRange, historyMaxPoints: *historyMaxPoints,
	}
	if !authConfig.disabled() && *tlsCert == "" {
		return errors.New("human authentication requires configured HTTPS")
	}
	if *webDir != "" {
		if info, err := os.Stat(*webDir); err != nil || !info.IsDir() {
			return errors.New("web-dir must be a readable directory")
		}
	}
	var deviceAuthorizer server.DeviceAuthorizer
	if *agentCredentials != "" {
		if *tlsCert == "" {
			return errors.New("agent credentials require configured HTTPS")
		}
		credentials, err := server.LoadDeviceCredentials(*agentCredentials)
		if err != nil {
			return err
		}
		deviceAuthorizer = credentials
	}
	databases, err := openServerStorage(context.Background(), *storageDirectory)
	if err != nil {
		return err
	}
	if databases != nil {
		defer func() {
			if err := databases.Close(); err != nil {
				log.Printf("component=server level=ERROR msg=%q", err)
			}
		}()
		log.Printf("component=server level=INFO storage=%q msg=%q", *storageDirectory, "SQLite storage initialized")
	}
	store := server.NewStore()
	restored, err := restoreMetricCurrentState(context.Background(), databases, store)
	if err != nil {
		return err
	}
	if restored > 0 {
		log.Printf("component=server level=INFO devices=%d msg=%q", restored, "restored current state from SQLite")
	}
	broker := server.NewBroker()
	presence := server.NewPresence()
	handler := server.NewHandlerWithDeviceAuthorizer(store, broker, deviceAuthorizer, presence)
	metricsBatch, err := buildMetricBatchRuntime(databases, handler, metricConfig)
	if err != nil {
		return err
	}
	humanAuth, err := buildAuthRuntime(databases, store, authConfig)
	if err != nil {
		return err
	}
	options := server.RouterOptions{API: handler, MetricsBatch: metricsBatch, WebDirectory: *webDir}
	if humanAuth != nil {
		options.Auth = humanAuth.handler
		options.MetricQueries = humanAuth.queries
		options.MetricHistory = humanAuth.history
	}
	router, err := server.NewRouter(options)
	if err != nil {
		return err
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
		return fmt.Errorf("serve HTTP: %w", err)
	}
	log.Print("component=server level=INFO msg=stopped")
	return nil
}
