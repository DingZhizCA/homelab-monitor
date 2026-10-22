/*
Package main assembles optional persistence, bounded storage maintenance and
capacity protection, human authentication, Dashboard management, and metric
query runtimes from explicit Server command-line configuration.

Each feature remains disabled when all of its settings are absent. Partial
configuration fails before the HTTP listener starts.
*/
package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
	"github.com/DingZhizCA/homelab-monitor/internal/server"
	"github.com/DingZhizCA/homelab-monitor/internal/storage"
)

type stringListFlag []string

func (f *stringListFlag) String() string { return strings.Join(*f, ",") }

func (f *stringListFlag) Set(value string) error {
	if value == "" {
		return errors.New("value must not be empty")
	}
	*f = append(*f, value)
	return nil
}

type metricBatchRuntimeConfig struct {
	maxSnapshots int
	maxBodyBytes int64
	guard        storage.MetricWriteGuard
}

type checkpointRuntimeConfig struct {
	interval          time.Duration
	walThresholdBytes int64
	timeout           time.Duration
}

type retentionRuntimeConfig struct {
	interval         time.Duration
	rawRetention     time.Duration
	receiptRetention time.Duration
	deleteLimit      int
	timeout          time.Duration
}

type capacityRuntimeConfig struct {
	coreMaxBytes       int64
	metricsSoftBytes   int64
	metricsHardBytes   int64
	filesystemMinBytes int64
}

func (c capacityRuntimeConfig) disabled() bool {
	return c.coreMaxBytes == 0 && c.metricsSoftBytes == 0 && c.metricsHardBytes == 0 && c.filesystemMinBytes == 0
}

func (c retentionRuntimeConfig) disabled() bool {
	return c.interval == 0 && c.rawRetention == 0 && c.receiptRetention == 0 && c.deleteLimit == 0 && c.timeout == 0
}

func (c checkpointRuntimeConfig) disabled() bool {
	return c.interval == 0 && c.walThresholdBytes == 0 && c.timeout == 0
}

func (c metricBatchRuntimeConfig) disabled() bool {
	return c.maxSnapshots == 0 && c.maxBodyBytes == 0
}

type authRuntimeConfig struct {
	origins          []string
	sessionTTL       time.Duration
	loginMaxKeys     int
	loginMaxAttempts int
	loginWindow      time.Duration
	historyMaxRange  time.Duration
	historyMaxPoints int
}

func (c authRuntimeConfig) disabled() bool {
	return len(c.origins) == 0 && c.sessionTTL == 0 && c.loginMaxKeys == 0 && c.loginMaxAttempts == 0 && c.loginWindow == 0 && c.historyMaxRange == 0 && c.historyMaxPoints == 0
}

type authRuntime struct {
	handler  *server.AuthHandler
	queries  *server.MetricQueryHandler
	history  *server.MetricHistoryHandler
	registry *dashboard.MetricRegistry
}

type dashboardRuntimeConfig struct {
	maxBodyBytes int64
}

func (c dashboardRuntimeConfig) disabled() bool { return c.maxBodyBytes == 0 }

type alertRuleRuntimeConfig struct {
	maxBodyBytes  int64
	maxDepth      int
	maxConditions int
}

func (c alertRuleRuntimeConfig) disabled() bool {
	return c.maxBodyBytes == 0 && c.maxDepth == 0 && c.maxConditions == 0
}

type pluginRuntimeConfig struct {
	artifactDirectory string
	maxArtifactBytes  int64
}

func (c pluginRuntimeConfig) disabled() bool {
	return c.artifactDirectory == "" && c.maxArtifactBytes == 0
}

type pluginRuntime struct {
	control    *server.PluginControlHandler
	artifacts  *server.PluginArtifactHandler
	admin      *server.PluginAdminHandler
	repository *plugins.FileArtifactRepository
}

func (r *pluginRuntime) Close() error {
	if r == nil || r.repository == nil {
		return nil
	}
	return r.repository.Close()
}

func openServerStorage(ctx context.Context, directory string) (*storage.Databases, error) {
	if directory == "" {
		return nil, nil
	}
	return storage.Open(ctx, directory)
}

func openServerStorageWithCapacity(ctx context.Context, directory string, config capacityRuntimeConfig) (*storage.Databases, error) {
	if directory == "" {
		if config.disabled() {
			return nil, nil
		}
		return nil, errors.New("storage-dir and all positive capacity settings are required together")
	}
	if err := validateCapacityRuntimeConfig(config); err != nil {
		return nil, err
	}
	return storage.OpenWithPageLimits(ctx, directory, config.coreMaxBytes, config.metricsHardBytes)
}

func buildCheckpointRuntime(databases *storage.Databases, config checkpointRuntimeConfig) (*storage.CheckpointManager, error) {
	if databases == nil {
		if config.disabled() {
			return nil, nil
		}
		return nil, errors.New("storage-dir and all positive checkpoint settings are required together")
	}
	if config.interval <= 0 || config.walThresholdBytes < 1 || config.timeout <= 0 {
		return nil, errors.New("storage-dir and all positive checkpoint settings are required together")
	}
	return storage.NewCheckpointManager(databases, storage.CheckpointConfig{
		Interval: config.interval, WALThresholdBytes: config.walThresholdBytes, Timeout: config.timeout,
	}, logCheckpointObservation)
}

func logCheckpointObservation(observation storage.CheckpointObservation) {
	if observation.Err != nil {
		log.Printf("component=storage level=ERROR database=%q reason=%q wal_bytes=%d duration=%q msg=%q", observation.Database, observation.Reason, observation.WALBytes, observation.Duration, observation.Err)
		return
	}
	log.Printf("component=storage level=INFO database=%q reason=%q wal_bytes=%d busy=%d log_frames=%d checkpointed_frames=%d duration=%q msg=%q", observation.Database, observation.Reason, observation.WALBytes, observation.Busy, observation.LogFrames, observation.CheckpointedFrames, observation.Duration, "passive WAL checkpoint completed")
}

func buildRetentionRuntime(databases *storage.Databases, config retentionRuntimeConfig) (*storage.MetricRetentionManager, error) {
	if databases == nil {
		if config.disabled() {
			return nil, nil
		}
		return nil, errors.New("storage-dir and all positive retention settings are required together")
	}
	if config.interval <= 0 || config.rawRetention <= 0 || config.receiptRetention <= config.rawRetention || config.deleteLimit < 1 || config.timeout <= 0 {
		return nil, errors.New("storage-dir, positive retention settings, and a receipt window longer than the raw window are required together")
	}
	return storage.NewMetricRetentionManager(databases.Metrics, storage.RetentionConfig{
		Interval: config.interval, RawRetention: config.rawRetention, ReceiptRetention: config.receiptRetention,
		DeleteLimit: config.deleteLimit, Timeout: config.timeout,
	}, logRetentionObservation)
}

func logRetentionObservation(observation storage.RetentionObservation) {
	if observation.Err != nil {
		log.Printf("component=storage level=ERROR raw_cutoff=%q receipt_cutoff=%q duration=%q msg=%q", observation.RawCutoff, observation.ReceiptCutoff, observation.Duration, observation.Err)
		return
	}
	log.Printf("component=storage level=INFO raw_cutoff=%q receipt_cutoff=%q deleted_snapshots=%d deleted_receipts=%d duration=%q msg=%q", observation.RawCutoff, observation.ReceiptCutoff, observation.DeletedSnapshots, observation.DeletedReceipts, observation.Duration, "bounded metric retention completed")
}

func buildCapacityRuntime(ctx context.Context, databases *storage.Databases, retention *storage.MetricRetentionManager, checkpoint *storage.CheckpointManager, config capacityRuntimeConfig) (*storage.CapacityManager, error) {
	if databases == nil {
		if config.disabled() {
			return nil, nil
		}
		return nil, errors.New("storage-dir and all positive capacity settings are required together")
	}
	if retention == nil || checkpoint == nil {
		return nil, errors.New("storage retention and checkpoint maintenance are required for capacity management")
	}
	if err := validateCapacityRuntimeConfig(config); err != nil {
		return nil, err
	}
	return storage.NewCapacityManager(ctx, databases, storage.CapacityConfig{
		CoreMaxBytes: config.coreMaxBytes, MetricsSoftBytes: config.metricsSoftBytes,
		MetricsHardBytes: config.metricsHardBytes, FilesystemMinBytes: config.filesystemMinBytes,
	}, retention, checkpoint, logCapacityObservation)
}

func validateCapacityRuntimeConfig(config capacityRuntimeConfig) error {
	if config.coreMaxBytes < 1 || config.metricsSoftBytes < 1 || config.metricsHardBytes <= config.metricsSoftBytes || config.filesystemMinBytes < 1 {
		return errors.New("storage-dir, positive capacity settings, and metrics soft bytes below hard bytes are required together")
	}
	return nil
}

func logCapacityObservation(observation storage.CapacityObservation) {
	level := "INFO"
	if observation.Level == "hard" || observation.Err != nil {
		level = "ERROR"
	} else if observation.Level == "soft" {
		level = "WARN"
	}
	log.Printf("component=storage level=%s capacity=%q metrics_used_bytes=%d metrics_allocated_bytes=%d metrics_wal_bytes=%d filesystem_free_bytes=%d incoming_bytes=%d cleanup_attempted=%t recovery_deferred=%t deleted_snapshots=%d deleted_receipts=%d error=%q msg=%q", level, observation.Level, observation.MetricsUsedBytes, observation.MetricsAllocatedBytes, observation.MetricsWALBytes, observation.FilesystemFreeBytes, observation.IncomingBytes, observation.CleanupAttempted, observation.RecoveryDeferred, observation.DeletedSnapshots, observation.DeletedReceipts, observation.Err, "metric storage capacity evaluated")
}

func restoreMetricCurrentState(ctx context.Context, databases *storage.Databases, store *server.Store) (int, error) {
	if databases == nil {
		return 0, nil
	}
	if store == nil {
		return 0, errors.New("current-state store is required")
	}
	source, err := storage.NewMetricCurrentSource(databases.Metrics)
	if err != nil {
		return 0, err
	}
	snapshots, err := source.LoadLatest(ctx)
	if err != nil {
		return 0, err
	}
	for _, snapshot := range snapshots {
		if _, restored := store.RestoreLatest(snapshot.Envelope, snapshot.ReceivedAt); !restored {
			return 0, errors.New("persisted current metric snapshot could not be restored")
		}
	}
	return len(snapshots), nil
}

func buildMetricBatchRuntime(databases *storage.Databases, api *server.Handler, config metricBatchRuntimeConfig) (*server.MetricsBatchHandler, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || config.maxSnapshots < 1 || config.maxBodyBytes < 1 {
		return nil, errors.New("storage-dir and positive metrics batch limits must be configured together")
	}
	writer, err := storage.NewMetricBatchWriterWithGuard(databases.Metrics, config.maxSnapshots, config.guard)
	if err != nil {
		return nil, err
	}
	return server.NewMetricsBatchHandler(api, writer, config.maxSnapshots, config.maxBodyBytes)
}

func buildAuthRuntime(databases *storage.Databases, current dashboard.SnapshotSource, config authRuntimeConfig) (*authRuntime, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || current == nil || len(config.origins) == 0 || config.sessionTTL <= 0 || config.loginMaxKeys < 1 || config.loginMaxAttempts < 1 || config.loginWindow <= 0 || config.historyMaxRange <= 0 || config.historyMaxPoints < 1 {
		return nil, errors.New("storage-dir and all positive auth and history settings are required together")
	}
	repository, err := storage.NewAuthRepository(databases.Core)
	if err != nil {
		return nil, err
	}
	service, err := auth.NewService(repository, repository, time.Now)
	if err != nil {
		return nil, err
	}
	limiter, err := auth.NewFixedWindowLimiter(config.loginMaxKeys, config.loginMaxAttempts, config.loginWindow, time.Now)
	if err != nil {
		return nil, err
	}
	handler, err := server.NewAuthHandler(service, limiter, config.sessionTTL, config.origins)
	if err != nil {
		return nil, err
	}
	registry := dashboard.NewMetricRegistry()
	policy := dashboard.AuthenticatedMetricAuthorizer{}
	queryService, err := dashboard.NewQueryService(current, policy, registry)
	if err != nil {
		return nil, err
	}
	queries, err := server.NewMetricQueryHandler(queryService, registry, handler)
	if err != nil {
		return nil, err
	}
	historySource, err := storage.NewMetricHistorySource(databases.Metrics, registry)
	if err != nil {
		return nil, err
	}
	historyService, err := dashboard.NewHistoryService(historySource, policy, registry, dashboard.HistoryBudget{MaxRange: config.historyMaxRange, MaxPoints: config.historyMaxPoints})
	if err != nil {
		return nil, err
	}
	history, err := server.NewMetricHistoryHandler(historyService, handler)
	if err != nil {
		return nil, err
	}
	return &authRuntime{handler: handler, queries: queries, history: history, registry: registry}, nil
}

func buildDashboardRuntime(databases *storage.Databases, humanAuth *authRuntime, config dashboardRuntimeConfig) (*server.DashboardHandler, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || humanAuth == nil || humanAuth.handler == nil || humanAuth.registry == nil || config.maxBodyBytes < 1 {
		return nil, errors.New("storage, human authentication, and a positive Dashboard request limit are required together")
	}
	repository, err := storage.NewDashboardRepository(databases.Core)
	if err != nil {
		return nil, err
	}
	publication, err := dashboard.NewViewerPublicationPolicy(humanAuth.registry)
	if err != nil {
		return nil, err
	}
	service, err := dashboard.NewService(repository, publication)
	if err != nil {
		return nil, err
	}
	return server.NewDashboardHandler(service, humanAuth.handler, config.maxBodyBytes)
}

func buildAlertRuleRuntime(databases *storage.Databases, humanAuth *authRuntime, config alertRuleRuntimeConfig) (*server.AlertRuleHandler, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || humanAuth == nil || humanAuth.handler == nil || humanAuth.registry == nil || config.maxBodyBytes < 1 || config.maxDepth < 1 || config.maxConditions < 1 {
		return nil, errors.New("storage, human authentication, and positive alert-rule request and expression limits are required together")
	}
	limits := alerts.Limits{MaxDepth: config.maxDepth, MaxConditions: config.maxConditions}
	repository, err := storage.NewAlertRuleRepository(databases.Core, humanAuth.registry, limits)
	if err != nil {
		return nil, err
	}
	service, err := alerts.NewRuleService(repository, humanAuth.registry, limits)
	if err != nil {
		return nil, err
	}
	return server.NewAlertRuleHandler(service, humanAuth.handler, config.maxBodyBytes)
}

func buildPluginRuntime(databases *storage.Databases, devices server.DeviceAuthorizer, humanAuth *authRuntime, config pluginRuntimeConfig) (*pluginRuntime, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || devices == nil || humanAuth == nil || config.artifactDirectory == "" || config.maxArtifactBytes < 1 {
		return nil, errors.New("storage, device authentication, human authentication, plugin artifact directory, and positive artifact limit are required together")
	}
	state, err := storage.NewPluginRepository(databases.Core)
	if err != nil {
		return nil, err
	}
	artifacts, err := plugins.NewFileArtifactRepository(config.artifactDirectory, config.maxArtifactBytes)
	if err != nil {
		return nil, err
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = artifacts.Close()
		}
	}()
	control, err := server.NewPluginControlHandler(state, devices)
	if err != nil {
		return nil, err
	}
	download, err := server.NewPluginArtifactHandler(state, artifacts, devices)
	if err != nil {
		return nil, err
	}
	admin, err := server.NewPluginAdminHandler(state, artifacts)
	if err != nil {
		return nil, err
	}
	closeOnError = false
	return &pluginRuntime{control: control, artifacts: download, admin: admin, repository: artifacts}, nil
}
