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

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
	"github.com/DingZhizCA/homelab-monitor/internal/server"
	"github.com/DingZhizCA/homelab-monitor/internal/storage"
)

type explicitDurationFlag struct {
	value time.Duration
	set   bool
}

func (f *explicitDurationFlag) String() string { return f.value.String() }

func (f *explicitDurationFlag) Set(value string) error {
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	f.value, f.set = parsed, true
	return nil
}

func (f *explicitDurationFlag) pointer() *time.Duration {
	if !f.set {
		return nil
	}
	value := f.value
	return &value
}

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
	checkpointInterval := flag.Duration("storage-checkpoint-interval", 0, "interval between WAL checkpoint inspections")
	checkpointWALBytes := flag.Int64("storage-checkpoint-wal-bytes", 0, "WAL size that triggers a passive checkpoint")
	checkpointTimeout := flag.Duration("storage-checkpoint-timeout", 0, "timeout for each passive WAL checkpoint")
	spaceRecoveryInterval := flag.Duration("storage-space-recovery-interval", 0, "interval between physical SQLite space recovery passes")
	spaceRecoveryTimeout := flag.Duration("storage-space-recovery-timeout", 0, "timeout per database physical recovery pass")
	walTruncateBytes := flag.Int64("storage-wal-truncate-bytes", 0, "WAL bytes that trigger a truncating checkpoint")
	vacuumMinFreePages := flag.Int64("storage-vacuum-min-free-pages", 0, "free pages that trigger incremental vacuum")
	vacuumMaxPages := flag.Int64("storage-vacuum-max-pages", 0, "maximum pages requested from incremental vacuum per pass")
	incrementalVacuumMigrate := flag.Bool("storage-incremental-vacuum-migrate", false, "explicitly convert legacy databases with startup VACUUM before serving")
	incrementalVacuumMigrationTimeout := flag.Duration("storage-incremental-vacuum-migration-timeout", 0, "timeout for explicit legacy incremental-vacuum conversion")
	retentionInterval := flag.Duration("storage-retention-interval", 0, "interval between bounded metric retention transactions")
	rawRetention := flag.Duration("storage-raw-retention", 0, "retention window for raw metric snapshots")
	receiptRetention := flag.Duration("storage-receipt-retention", 0, "retention window for metric batch retry receipts")
	retentionDeleteLimit := flag.Int("storage-retention-delete-limit", 0, "maximum snapshots and receipts deleted per retention transaction")
	retentionTimeout := flag.Duration("storage-retention-timeout", 0, "timeout for each metric retention transaction")
	aggregationInterval := flag.Duration("storage-aggregation-interval", 0, "interval between bounded metric aggregation transactions")
	aggregationMaxSnapshots := flag.Int("storage-aggregation-max-snapshots", 0, "maximum raw snapshots aggregated per transaction")
	aggregationTimeout := flag.Duration("storage-aggregation-timeout", 0, "timeout for each metric aggregation transaction")
	pluginAggregationInterval := flag.Duration("storage-plugin-aggregation-interval", 0, "interval between bounded plugin metric aggregation transactions")
	pluginAggregationMaxObservations := flag.Int("storage-plugin-aggregation-max-observations", 0, "maximum queued plugin observations aggregated per transaction")
	pluginAggregationMaxScanned := flag.Int("storage-plugin-aggregation-max-scanned", 0, "maximum raw plugin observations scanned per aggregation transaction")
	pluginAggregationTimeout := flag.Duration("storage-plugin-aggregation-timeout", 0, "timeout for each plugin metric aggregation transaction")
	minuteRetention := flag.Duration("storage-minute-retention", 0, "retention and query window for minute metric aggregates")
	hourRetention := flag.Duration("storage-hour-retention", 0, "retention and query window for hour metric aggregates")
	dayRetention := flag.Duration("storage-day-retention", 0, "retention and query window for day metric aggregates")
	coreMaxBytes := flag.Int64("storage-core-max-bytes", 0, "hard maximum bytes allocated to core.db pages")
	metricsSoftBytes := flag.Int64("storage-metrics-soft-bytes", 0, "logical metric bytes that enter soft capacity state")
	metricsHardBytes := flag.Int64("storage-metrics-hard-bytes", 0, "hard maximum bytes allocated to metrics.db pages")
	filesystemMinBytes := flag.Int64("storage-filesystem-min-bytes", 0, "filesystem bytes reserved from metric ingestion")
	metricMaxSnapshots := flag.Int("metrics-batch-max-snapshots", 0, "maximum snapshots accepted in one persisted Agent batch")
	metricMaxBodyBytes := flag.Int64("metrics-batch-max-body-bytes", 0, "maximum encoded bytes accepted by the persisted Agent batch endpoint")
	pluginMetricMaxObservations := flag.Int("plugin-metrics-batch-max-observations", 0, "maximum plugin observations accepted in one persisted batch")
	pluginMetricMaxSamples := flag.Int("plugin-metrics-batch-max-samples", 0, "maximum total plugin samples accepted in one persisted batch")
	pluginMetricMaxBodyBytes := flag.Int64("plugin-metrics-batch-max-body-bytes", 0, "maximum encoded bytes accepted by the plugin metric batch endpoint")
	var authOrigins stringListFlag
	flag.Var(&authOrigins, "auth-origin", "allowed HTTPS browser Origin; repeat for additional Origins")
	authSessionTTL := flag.Duration("auth-session-ttl", 0, "server-side browser Session lifetime")
	authLoginMaxKeys := flag.Int("auth-login-max-keys", 0, "maximum retained login limiter identities")
	authLoginMaxAttempts := flag.Int("auth-login-max-attempts", 0, "maximum login-related attempts per identity and window")
	authLoginWindow := flag.Duration("auth-login-window", 0, "login limiter fixed-window duration")
	historyMaxRange := flag.Duration("history-max-range", 0, "maximum time range for one historical metric query")
	historyMaxPoints := flag.Int("history-max-points", 0, "maximum observations returned by one historical metric query")
	pluginHistoryMaxScanned := flag.Int("plugin-history-max-observations-scanned", 0, "maximum raw plugin observations scanned by one history query")
	pluginMetricMaxDescriptors := flag.Int("plugin-metric-max-descriptors", 0, "maximum observed plugin metric descriptors returned per device")
	dashboardMaxBodyBytes := flag.Int64("dashboard-max-body-bytes", 0, "maximum Dashboard mutation request body bytes")
	alertRuleMaxBodyBytes := flag.Int64("alert-rule-max-body-bytes", 0, "maximum alert-rule mutation request body bytes")
	alertRuleMaxDepth := flag.Int("alert-rule-max-depth", 0, "maximum alert-rule expression depth")
	alertRuleMaxConditions := flag.Int("alert-rule-max-conditions", 0, "maximum metric conditions in one alert rule")
	alertEvaluationEnabled := flag.Bool("alert-evaluation-enabled", false, "enable bounded evaluation of current alert rules")
	alertEvaluationInterval := flag.Duration("alert-evaluation-interval", 0, "interval between alert evaluation runs")
	alertEvaluationTimeout := flag.Duration("alert-evaluation-timeout", 0, "timeout for one alert evaluation run")
	alertEvaluationMaxRules := flag.Int("alert-evaluation-max-rules", 0, "maximum rules evaluated in one run")
	incidentMaxStates := flag.Int("incident-max-states", 0, "maximum current Incident states retained")
	var incidentPendingFor, incidentRecoveryFor explicitDurationFlag
	flag.Var(&incidentPendingFor, "incident-pending-for", "continuous true duration before firing; explicitly set 0s for immediate")
	flag.Var(&incidentRecoveryFor, "incident-recovery-for", "continuous false duration before resolving; explicitly set 0s for immediate")
	incidentUnknownBehavior := flag.String("incident-unknown-behavior", "", "Incident behavior for unknown evaluation: hold, reset_pending, or resolve")
	incidentDisabledBehavior := flag.String("incident-disabled-behavior", "", "Incident behavior for disabled rule: hold, reset_pending, or resolve")
	pluginArtifactDirectory := flag.String("plugin-artifact-dir", "", "private immutable plugin artifact repository directory")
	pluginMaxArtifactBytes := flag.Int64("plugin-max-artifact-bytes", 0, "maximum plugin artifact bytes read or served")
	pluginMaxRepositoryBytes := flag.Int64("plugin-max-repository-bytes", 0, "maximum bytes stored in plugin package files")
	pluginMaxPackages := flag.Int("plugin-max-packages", 0, "maximum immutable plugin packages retained")
	pluginMaxCollectionPolicies := flag.Int("plugin-max-collection-policies", 0, "maximum collection policies retained per device")
	pluginMaxScanEntries := flag.Int("plugin-max-scan-entries", 0, "maximum filesystem entries inspected per artifact operation")
	pluginCleanupInterval := flag.Duration("plugin-cleanup-interval", 0, "interval between bounded plugin artifact cleanup passes")
	pluginOrphanGrace := flag.Duration("plugin-orphan-grace", 0, "minimum unreferenced package age before cleanup")
	pluginCleanupDeleteLimit := flag.Int("plugin-cleanup-delete-limit", 0, "maximum packages deleted per cleanup pass")
	pluginCleanupTimeout := flag.Duration("plugin-cleanup-timeout", 0, "timeout for one plugin artifact cleanup pass")
	flag.Parse()
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("both tls-cert and tls-key are required")
	}
	if *agentCredentials == "" {
		log.Printf("component=server level=WARN msg=%q", "Agent device authentication is disabled for compatibility; metrics requests are not bound to device credentials")
	}
	metricConfig := metricBatchRuntimeConfig{maxSnapshots: *metricMaxSnapshots, maxBodyBytes: *metricMaxBodyBytes}
	pluginMetricConfig := pluginMetricBatchRuntimeConfig{
		maxObservations: *pluginMetricMaxObservations, maxSamples: *pluginMetricMaxSamples, maxBodyBytes: *pluginMetricMaxBodyBytes,
	}
	capacityConfig := capacityRuntimeConfig{
		coreMaxBytes: *coreMaxBytes, metricsSoftBytes: *metricsSoftBytes,
		metricsHardBytes: *metricsHardBytes, filesystemMinBytes: *filesystemMinBytes,
	}
	if (!metricConfig.disabled() || !pluginMetricConfig.disabled()) && *agentCredentials == "" {
		return errors.New("persisted metric batches require Agent device credentials")
	}
	authConfig := authRuntimeConfig{
		origins: authOrigins, sessionTTL: *authSessionTTL,
		loginMaxKeys: *authLoginMaxKeys, loginMaxAttempts: *authLoginMaxAttempts, loginWindow: *authLoginWindow,
		historyMaxRange: *historyMaxRange, historyMaxPoints: *historyMaxPoints,
	}
	aggregationConfig := aggregationRuntimeConfig{
		interval: *aggregationInterval, maxSnapshots: *aggregationMaxSnapshots, timeout: *aggregationTimeout,
		minuteRetention: *minuteRetention, hourRetention: *hourRetention, dayRetention: *dayRetention,
	}
	pluginAggregationConfig := pluginAggregationRuntimeConfig{
		interval: *pluginAggregationInterval, maxObservations: *pluginAggregationMaxObservations,
		maxScanned: *pluginAggregationMaxScanned, rawRetention: *rawRetention, timeout: *pluginAggregationTimeout,
		minuteRetention: *minuteRetention, hourRetention: *hourRetention, dayRetention: *dayRetention,
	}
	if !aggregationConfig.disabled() && !authConfig.disabled() {
		authConfig.historyTiers = &storage.HistoryTierPolicy{
			RawWindow: *rawRetention, MinuteWindow: *minuteRetention, HourWindow: *hourRetention, DayWindow: *dayRetention,
		}
	}
	var pluginHistoryTiers *storage.HistoryTierPolicy
	if !pluginAggregationConfig.disabled() && !authConfig.disabled() {
		pluginHistoryTiers = &storage.HistoryTierPolicy{
			RawWindow: *rawRetention, MinuteWindow: *minuteRetention, HourWindow: *hourRetention, DayWindow: *dayRetention,
		}
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
	databases, err := openServerStorageWithCapacity(context.Background(), *storageDirectory, capacityConfig)
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
	if *incrementalVacuumMigrate {
		if databases == nil || *incrementalVacuumMigrationTimeout <= 0 {
			return errors.New("incremental vacuum migration requires storage and a positive migration timeout")
		}
		migrationContext, cancelMigration := context.WithTimeout(context.Background(), *incrementalVacuumMigrationTimeout)
		migrationErr := storage.EnableIncrementalVacuum(migrationContext, databases, capacityConfig.filesystemMinBytes)
		cancelMigration()
		if migrationErr != nil {
			return migrationErr
		}
	} else if *incrementalVacuumMigrationTimeout != 0 {
		return errors.New("incremental vacuum migration timeout requires the explicit migration flag")
	}
	checkpointManager, err := buildCheckpointRuntime(databases, checkpointRuntimeConfig{
		interval: *checkpointInterval, walThresholdBytes: *checkpointWALBytes, timeout: *checkpointTimeout,
	})
	if err != nil {
		return err
	}
	retentionManager, err := buildRetentionRuntime(databases, retentionRuntimeConfig{
		interval: *retentionInterval, rawRetention: *rawRetention, receiptRetention: *receiptRetention,
		deleteLimit: *retentionDeleteLimit, timeout: *retentionTimeout,
		minuteRetention: *minuteRetention, hourRetention: *hourRetention, dayRetention: *dayRetention,
	})
	if err != nil {
		return err
	}
	aggregationManager, err := buildAggregationRuntime(databases, aggregationConfig)
	if err != nil {
		return err
	}
	if aggregationManager != nil && retentionManager == nil {
		return errors.New("metric aggregation requires configured metric retention")
	}
	pluginAggregationManager, err := buildPluginAggregationRuntime(databases, pluginAggregationConfig)
	if err != nil {
		return err
	}
	if pluginAggregationManager != nil && retentionManager == nil {
		return errors.New("plugin metric aggregation requires configured metric retention")
	}
	spaceRecoveryManager, err := buildSpaceRecoveryRuntime(databases, spaceRecoveryRuntimeConfig{
		interval: *spaceRecoveryInterval, timeout: *spaceRecoveryTimeout, walTruncateBytes: *walTruncateBytes,
		vacuumMinFreePages: *vacuumMinFreePages, vacuumMaxPages: *vacuumMaxPages,
	})
	if err != nil {
		return err
	}
	capacityManager, err := buildCapacityRuntime(context.Background(), databases, retentionManager, checkpointManager, capacityConfig)
	if err != nil {
		return err
	}
	metricConfig.guard = capacityManager
	pluginMetricConfig.guard = capacityManager
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
	pluginMetricsBatch, err := buildPluginMetricBatchRuntime(databases, handler, pluginMetricConfig)
	if err != nil {
		return err
	}
	humanAuth, err := buildAuthRuntime(databases, store, authConfig)
	if err != nil {
		return err
	}
	pluginMetricHistory, err := buildPluginMetricHistoryRuntime(databases, humanAuth, *pluginHistoryMaxScanned, pluginmetrics.HistoryBudget{MaxRange: *historyMaxRange, MaxPoints: *historyMaxPoints}, pluginHistoryTiers)
	if err != nil {
		return err
	}
	pluginMetricCatalog, err := buildPluginMetricCatalogRuntime(databases, humanAuth, *pluginMetricMaxDescriptors)
	if err != nil {
		return err
	}
	dashboards, err := buildDashboardRuntime(databases, humanAuth, dashboardRuntimeConfig{maxBodyBytes: *dashboardMaxBodyBytes})
	if err != nil {
		return err
	}
	alertRules, err := buildAlertRuleRuntime(databases, humanAuth, alertRuleRuntimeConfig{
		maxBodyBytes: *alertRuleMaxBodyBytes, maxDepth: *alertRuleMaxDepth, maxConditions: *alertRuleMaxConditions,
	})
	if err != nil {
		return err
	}
	alertEngine, err := buildAlertEvaluationRuntime(databases, humanAuth, store, alertRuleRuntimeConfig{
		maxBodyBytes: *alertRuleMaxBodyBytes, maxDepth: *alertRuleMaxDepth, maxConditions: *alertRuleMaxConditions,
	}, alertEvaluationRuntimeConfig{
		enabled: *alertEvaluationEnabled, interval: *alertEvaluationInterval, timeout: *alertEvaluationTimeout,
		maxRulesPerRun: *alertEvaluationMaxRules, maxIncidentStates: *incidentMaxStates,
		pendingFor: incidentPendingFor.pointer(), recoveryFor: incidentRecoveryFor.pointer(),
		unknownBehavior: alerts.UncertainBehavior(*incidentUnknownBehavior), disabledBehavior: alerts.UncertainBehavior(*incidentDisabledBehavior),
	})
	if err != nil {
		return err
	}
	pluginsRuntime, err := buildPluginRuntime(databases, deviceAuthorizer, humanAuth, pluginRuntimeConfig{
		artifactDirectory:  *pluginArtifactDirectory,
		maxArtifactBytes:   *pluginMaxArtifactBytes,
		maxRepositoryBytes: *pluginMaxRepositoryBytes,
		maxPackages:        *pluginMaxPackages, maxScanEntries: *pluginMaxScanEntries,
		maxCollectionPolicies: *pluginMaxCollectionPolicies,
		cleanupInterval:       *pluginCleanupInterval, orphanGrace: *pluginOrphanGrace,
		cleanupDeleteLimit: *pluginCleanupDeleteLimit, cleanupTimeout: *pluginCleanupTimeout,
	})
	if err != nil {
		return err
	}
	if pluginsRuntime != nil {
		defer func() {
			if err := pluginsRuntime.Close(); err != nil {
				log.Printf("component=server level=ERROR msg=%q", err)
			}
		}()
	}
	options := server.RouterOptions{API: handler, MetricsBatch: metricsBatch, PluginMetricsBatch: pluginMetricsBatch, WebDirectory: *webDir}
	if humanAuth != nil {
		options.Auth = humanAuth.handler
		options.Dashboards = dashboards
		options.AlertRules = alertRules
		options.MetricQueries = humanAuth.queries
		options.MetricHistory = humanAuth.history
		options.PluginMetricHistory = pluginMetricHistory
		options.PluginMetricCatalog = pluginMetricCatalog
	}
	if pluginsRuntime != nil {
		options.PluginControl = pluginsRuntime.control
		options.PluginArtifacts = pluginsRuntime.artifacts
		options.PluginAdmin = pluginsRuntime.admin
		options.CollectionPolicies = pluginsRuntime.policies
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
	if checkpointManager != nil {
		checkpointContext, cancelCheckpoint := context.WithCancel(ctx)
		checkpointDone := make(chan struct{})
		go func() {
			checkpointManager.Run(checkpointContext)
			close(checkpointDone)
		}()
		defer func() {
			cancelCheckpoint()
			<-checkpointDone
			checkpointManager.CheckpointAll(context.Background())
		}()
	}
	if retentionManager != nil {
		retentionContext, cancelRetention := context.WithCancel(ctx)
		retentionDone := make(chan struct{})
		go func() {
			retentionManager.Run(retentionContext)
			close(retentionDone)
		}()
		defer func() {
			cancelRetention()
			<-retentionDone
		}()
	}
	if aggregationManager != nil {
		aggregationContext, cancelAggregation := context.WithCancel(ctx)
		aggregationDone := make(chan struct{})
		go func() {
			aggregationManager.Run(aggregationContext)
			close(aggregationDone)
		}()
		defer func() {
			cancelAggregation()
			<-aggregationDone
		}()
	}
	if pluginAggregationManager != nil {
		aggregationContext, cancelAggregation := context.WithCancel(ctx)
		aggregationDone := make(chan struct{})
		go func() {
			pluginAggregationManager.Run(aggregationContext)
			close(aggregationDone)
		}()
		defer func() {
			cancelAggregation()
			<-aggregationDone
		}()
	}
	if spaceRecoveryManager != nil {
		spaceContext, cancelSpace := context.WithCancel(ctx)
		spaceDone := make(chan struct{})
		go func() {
			spaceRecoveryManager.Run(spaceContext)
			close(spaceDone)
		}()
		defer func() {
			cancelSpace()
			<-spaceDone
		}()
	}
	if alertEngine != nil {
		alertContext, cancelAlerts := context.WithCancel(ctx)
		alertDone := make(chan struct{})
		go func() {
			alertEngine.Run(alertContext)
			close(alertDone)
		}()
		defer func() {
			cancelAlerts()
			<-alertDone
		}()
	}
	if pluginsRuntime != nil {
		artifactContext, cancelArtifacts := context.WithCancel(ctx)
		artifactDone := make(chan struct{})
		go func() {
			pluginsRuntime.maintenance.Run(artifactContext)
			close(artifactDone)
		}()
		defer func() {
			cancelArtifacts()
			<-artifactDone
		}()
	}
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
