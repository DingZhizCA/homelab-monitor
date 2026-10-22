/*
Package main implements the homelab-server executable entry point.

本文件职责：
- 创建并配置 homelab-server 的 HTTP 路由器
- 注册 Server 提供的 HTTP API 路由
- 启动 HTTP Server
- 处理进程启动阶段的错误

本文件不负责：
- Linux 系统指标采集
- 指标历史数据持久化
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
)

func main() {
	addr := flag.String("listen", ":8080", "HTTP(S) listen address")
	webDir := flag.String("web-dir", "", "directory containing the built Dashboard (disabled when empty)")
	tlsCert := flag.String("tls-cert", "", "TLS certificate for HTTPS (both TLS flags are required together)")
	tlsKey := flag.String("tls-key", "", "TLS private key for HTTPS (both TLS flags are required together)")
	agentCredentials := flag.String("agent-credentials", "", "private JSON file containing per-device token SHA-256 verifiers")
	flag.Parse()
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("component=server level=ERROR msg=both tls-cert and tls-key are required")
	}
	if *agentCredentials == "" {
		log.Printf("component=server level=WARN msg=%q", "Agent device authentication is disabled for compatibility; metrics requests are not bound to device credentials")
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
	if *webDir != "" {
		if info, err := os.Stat(*webDir); err != nil || !info.IsDir() {
			log.Fatalf("component=server level=ERROR msg=%q", "web-dir must be a readable directory")
		}
	}
	router, err := server.NewRouter(server.RouterOptions{API: handler, WebDirectory: *webDir})
	if err != nil {
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
