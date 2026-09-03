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
	"os/signal"
	"syscall"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/server"
)

func main() {
	addr := flag.String("listen", ":8080", "HTTP listen address")
	flag.Parse()
	store := server.NewStore()
	broker := server.NewBroker()
	presence := server.NewPresence()
	handler := server.NewHandler(store, broker, presence)
	// 使用独立 ServeMux，避免依赖全局 DefaultServeMux。
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", handler.Health)
	mux.HandleFunc("/api/v1/metrics", handler.Metrics)
	mux.HandleFunc("/api/v1/devices", handler.Devices)
	mux.HandleFunc("/api/v1/devices/", handler.Device)
	mux.HandleFunc("/api/v1/events", handler.Events)
	mux.HandleFunc("/api/v1/presence", handler.Presence)
	mux.HandleFunc("/api/v1/refresh/", handler.RefreshStatus)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           mux,
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
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Print("component=server level=INFO msg=stopped")
}
