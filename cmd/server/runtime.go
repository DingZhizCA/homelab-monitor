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
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
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

type pluginMetricBatchRuntimeConfig struct {
	maxObservations int
	maxSamples      int
	maxBodyBytes    int64
	guard           storage.MetricWriteGuard
}

func (c pluginMetricBatchRuntimeConfig) disabled() bool {
	return c.maxObservations == 0 && c.maxSamples == 0 && c.maxBodyBytes == 0
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
	minuteRetention  time.Duration
	hourRetention    time.Duration
	dayRetention     time.Duration
}

type aggregationRuntimeConfig struct {
	interval        time.Duration
	maxSnapshots    int
	timeout         time.Duration
	minuteRetention time.Duration
	hourRetention   time.Duration
	dayRetention    time.Duration
}

type pluginAggregationRuntimeConfig struct {
	interval        time.Duration
	maxObservations int
	maxScanned      int
	rawRetention    time.Duration
	timeout         time.Duration
	minuteRetention time.Duration
	hourRetention   time.Duration
	dayRetention    time.Duration
}

func (c pluginAggregationRuntimeConfig) disabled() bool {
	return c.interval == 0 && c.maxObservations == 0 && c.maxScanned == 0 && c.timeout == 0
}

func (c aggregationRuntimeConfig) disabled() bool {
	return c.interval == 0 && c.maxSnapshots == 0 && c.timeout == 0 && c.minuteRetention == 0 && c.hourRetention == 0 && c.dayRetention == 0
}

type spaceRecoveryRuntimeConfig struct {
	interval           time.Duration
	timeout            time.Duration
	walTruncateBytes   int64
	vacuumMinFreePages int64
	vacuumMaxPages     int64
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
	return c.interval == 0 && c.rawRetention == 0 && c.receiptRetention == 0 && c.deleteLimit == 0 && c.timeout == 0 && c.minuteRetention == 0 && c.hourRetention == 0 && c.dayRetention == 0
}

func (c spaceRecoveryRuntimeConfig) disabled() bool {
	return c.interval == 0 && c.timeout == 0 && c.walTruncateBytes == 0 && c.vacuumMinFreePages == 0 && c.vacuumMaxPages == 0
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
	historyTiers     *storage.HistoryTierPolicy
}

func (c authRuntimeConfig) disabled() bool {
	return len(c.origins) == 0 && c.sessionTTL == 0 && c.loginMaxKeys == 0 && c.loginMaxAttempts == 0 && c.loginWindow == 0 && c.historyMaxRange == 0 && c.historyMaxPoints == 0 && c.historyTiers == nil
}

type authRuntime struct {
	handler  *server.AuthHandler
	queries  *server.MetricQueryHandler
	history  *server.MetricHistoryHandler
	registry *dashboard.MetricRegistry
}

type dashboardRuntimeConfig struct {
	maxBodyBytes  int64
	pluginMetrics bool
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

type alertEvaluationRuntimeConfig struct {
	enabled                bool
	interval               time.Duration
	timeout                time.Duration
	maxRulesPerRun         int
	maxIncidentStates      int
	maxIncidentArchives    int
	archiveCleanupInterval time.Duration
	archiveRetention       time.Duration
	archiveCleanupLimit    int
	archiveCleanupTimeout  time.Duration
	pendingFor             *time.Duration
	recoveryFor            *time.Duration
	unknownBehavior        alerts.UncertainBehavior
	disabledBehavior       alerts.UncertainBehavior
}

type alertEvaluationRuntime struct {
	engine           *alerts.Engine
	incidents        *server.IncidentHandler
	archiveRetention *storage.IncidentArchiveRetentionManager
}

func (c alertEvaluationRuntimeConfig) disabled() bool {
	return !c.enabled && c.interval == 0 && c.timeout == 0 && c.maxRulesPerRun == 0 && c.maxIncidentStates == 0 && c.maxIncidentArchives == 0 && c.archiveCleanupInterval == 0 && c.archiveRetention == 0 && c.archiveCleanupLimit == 0 && c.archiveCleanupTimeout == 0 && c.pendingFor == nil && c.recoveryFor == nil && c.unknownBehavior == "" && c.disabledBehavior == ""
}

type pluginRuntimeConfig struct {
	artifactDirectory     string
	maxArtifactBytes      int64
	maxRepositoryBytes    int64
	maxPackages           int
	maxScanEntries        int
	maxCollectionPolicies int
	cleanupInterval       time.Duration
	orphanGrace           time.Duration
	cleanupDeleteLimit    int
	cleanupTimeout        time.Duration
}

func (c pluginRuntimeConfig) disabled() bool {
	return c.artifactDirectory == "" && c.maxArtifactBytes == 0 && c.maxRepositoryBytes == 0 && c.maxPackages == 0 && c.maxScanEntries == 0 && c.maxCollectionPolicies == 0 && c.cleanupInterval == 0 && c.orphanGrace == 0 && c.cleanupDeleteLimit == 0 && c.cleanupTimeout == 0
}

type pluginRuntime struct {
	control     *server.PluginControlHandler
	artifacts   *server.PluginArtifactHandler
	admin       *server.PluginAdminHandler
	policies    *server.PluginCollectionPolicyHandler
	repository  *plugins.FileArtifactRepository
	maintenance *plugins.ArtifactMaintenance
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
		MinuteRetention: config.minuteRetention, HourRetention: config.hourRetention, DayRetention: config.dayRetention,
		DeleteLimit: config.deleteLimit, Timeout: config.timeout,
	}, logRetentionObservation)
}

func logRetentionObservation(observation storage.RetentionObservation) {
	if observation.Err != nil {
		log.Printf("component=storage level=ERROR raw_cutoff=%q receipt_cutoff=%q duration=%q msg=%q", observation.RawCutoff, observation.ReceiptCutoff, observation.Duration, observation.Err)
		return
	}
	log.Printf("component=storage level=INFO raw_cutoff=%q receipt_cutoff=%q deleted_snapshots=%d deleted_receipts=%d deleted_plugin_observations=%d deleted_plugin_receipts=%d deleted_minute=%d deleted_hour=%d deleted_day=%d deleted_plugin_minute=%d deleted_plugin_hour=%d deleted_plugin_day=%d duration=%q msg=%q", observation.RawCutoff, observation.ReceiptCutoff, observation.DeletedSnapshots, observation.DeletedReceipts, observation.DeletedPluginObservations, observation.DeletedPluginReceipts, observation.DeletedMinute, observation.DeletedHour, observation.DeletedDay, observation.DeletedPluginMinute, observation.DeletedPluginHour, observation.DeletedPluginDay, observation.Duration, "bounded metric retention completed")
}

func buildAggregationRuntime(databases *storage.Databases, config aggregationRuntimeConfig) (*storage.MetricAggregationManager, error) {
	if databases == nil {
		if config.disabled() {
			return nil, nil
		}
		return nil, errors.New("storage-dir and all positive aggregation settings are required together")
	}
	if config.interval <= 0 || config.maxSnapshots < 1 || config.timeout <= 0 || config.minuteRetention <= 0 || config.hourRetention <= config.minuteRetention || config.dayRetention <= config.hourRetention {
		return nil, errors.New("storage-dir, positive aggregation bounds, and increasing minute, hour, and day retention windows are required together")
	}
	return storage.NewMetricAggregationManager(databases.Metrics, dashboard.NewMetricRegistry(), storage.AggregationConfig{
		Interval: config.interval, MaxSnapshots: config.maxSnapshots, Timeout: config.timeout,
	}, logAggregationObservation)
}

func logAggregationObservation(observation storage.AggregationObservation) {
	level := "INFO"
	if observation.Err != nil {
		level = "ERROR"
	}
	log.Printf("component=storage level=%s processed_snapshots=%d updated_buckets=%d pending_snapshots=%d duration=%q error=%q msg=%q", level, observation.ProcessedSnapshots, observation.UpdatedBuckets, observation.PendingSnapshots, observation.Duration, observation.Err, "bounded metric aggregation completed")
}

func buildPluginAggregationRuntime(databases *storage.Databases, config pluginAggregationRuntimeConfig) (*storage.PluginMetricAggregationManager, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil {
		return nil, errors.New("storage-dir and all positive plugin aggregation settings are required together")
	}
	if config.interval <= 0 || config.maxObservations < 1 || config.maxScanned < 1 || config.rawRetention <= 0 || config.timeout <= 0 || config.minuteRetention <= config.rawRetention || config.hourRetention <= config.minuteRetention || config.dayRetention <= config.hourRetention {
		return nil, errors.New("storage-dir, positive plugin aggregation bounds, and increasing raw, minute, hour, and day retention windows are required together")
	}
	return storage.NewPluginMetricAggregationManager(databases.Metrics, storage.PluginAggregationConfig{
		Interval: config.interval, MaxObservations: config.maxObservations, MaxScanned: config.maxScanned,
		RawRetention: config.rawRetention, Timeout: config.timeout,
	}, logPluginAggregationObservation)
}

func logPluginAggregationObservation(observation storage.PluginAggregationObservation) {
	level := "INFO"
	if observation.Err != nil {
		level = "ERROR"
	}
	log.Printf("component=storage level=%s processed_observations=%d scanned_observations=%d updated_series=%d pending_observations=%d duration=%q error=%q msg=%q", level, observation.ProcessedObservations, observation.ScannedObservations, observation.UpdatedSeries, observation.PendingObservations, observation.Duration, observation.Err, "bounded plugin metric aggregation completed")
}

func buildSpaceRecoveryRuntime(databases *storage.Databases, config spaceRecoveryRuntimeConfig) (*storage.SpaceRecoveryManager, error) {
	if databases == nil {
		if config.disabled() {
			return nil, nil
		}
		return nil, errors.New("storage-dir and all positive physical space recovery settings are required together")
	}
	if config.interval <= 0 || config.timeout <= 0 || config.walTruncateBytes < 1 || config.vacuumMinFreePages < 1 || config.vacuumMaxPages < 1 {
		return nil, errors.New("storage-dir and all positive physical space recovery settings are required together")
	}
	return storage.NewSpaceRecoveryManager(context.Background(), databases, storage.SpaceRecoveryConfig{
		Interval: config.interval, Timeout: config.timeout, WALTruncateBytes: config.walTruncateBytes,
		VacuumMinFreePages: config.vacuumMinFreePages, VacuumMaxPagesPerRun: config.vacuumMaxPages,
	}, logSpaceRecoveryObservation)
}

func logSpaceRecoveryObservation(observation storage.SpaceRecoveryObservation) {
	level := "INFO"
	if observation.Err != nil {
		level = "ERROR"
	}
	log.Printf("component=storage level=%s database=%q allocated_before=%d allocated_after=%d free_pages_before=%d free_pages_after=%d wal_before=%d wal_after=%d vacuum_attempted=%t truncate_attempted=%t busy=%d log_frames=%d checkpointed_frames=%d duration=%q error=%q msg=%q", level, observation.Database, observation.AllocatedBytesBefore, observation.AllocatedBytesAfter, observation.FreePagesBefore, observation.FreePagesAfter, observation.WALBytesBefore, observation.WALBytesAfter, observation.VacuumAttempted, observation.TruncateAttempted, observation.Busy, observation.LogFrames, observation.CheckpointedFrames, observation.Duration, observation.Err, "bounded SQLite physical space recovery completed")
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
	log.Printf("component=storage level=%s capacity=%q metrics_used_bytes=%d metrics_allocated_bytes=%d metrics_wal_bytes=%d filesystem_free_bytes=%d incoming_bytes=%d cleanup_attempted=%t recovery_deferred=%t deleted_snapshots=%d deleted_receipts=%d deleted_plugin_observations=%d deleted_plugin_receipts=%d error=%q msg=%q", level, observation.Level, observation.MetricsUsedBytes, observation.MetricsAllocatedBytes, observation.MetricsWALBytes, observation.FilesystemFreeBytes, observation.IncomingBytes, observation.CleanupAttempted, observation.RecoveryDeferred, observation.DeletedSnapshots, observation.DeletedReceipts, observation.DeletedPluginObservations, observation.DeletedPluginReceipts, observation.Err, "metric storage capacity evaluated")
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

func buildPluginMetricHistoryRuntime(databases *storage.Databases, authRuntime *authRuntime, maxScanned int, budget pluginmetrics.HistoryBudget, tiers *storage.HistoryTierPolicy) (*server.PluginMetricHistoryHandler, error) {
	if maxScanned == 0 {
		return nil, nil
	}
	if databases == nil || authRuntime == nil || maxScanned < 1 || budget.MaxRange <= 0 || budget.MaxPoints < 1 {
		return nil, errors.New("storage, human authentication, history budgets, and a positive plugin observation scan limit are required together")
	}
	var source *storage.PluginMetricHistorySource
	var err error
	if tiers == nil {
		source, err = storage.NewPluginMetricHistorySource(databases.Metrics, maxScanned)
	} else {
		source, err = storage.NewTieredPluginMetricHistorySource(databases.Metrics, maxScanned, *tiers)
	}
	if err != nil {
		return nil, err
	}
	service, err := pluginmetrics.NewHistoryService(source, budget)
	if err != nil {
		return nil, err
	}
	return server.NewPluginMetricHistoryHandler(service, authRuntime.handler)
}

func buildPluginMetricCatalogRuntime(databases *storage.Databases, authRuntime *authRuntime, maxDescriptors int) (*server.PluginMetricCatalogHandler, error) {
	if maxDescriptors == 0 {
		return nil, nil
	}
	if databases == nil || authRuntime == nil || maxDescriptors < 1 {
		return nil, errors.New("storage, human authentication, and a positive plugin descriptor limit are required together")
	}
	source, err := storage.NewPluginMetricDescriptorSource(databases.Metrics)
	if err != nil {
		return nil, err
	}
	service, err := pluginmetrics.NewCatalogService(source, maxDescriptors)
	if err != nil {
		return nil, err
	}
	return server.NewPluginMetricCatalogHandler(service, authRuntime.handler)
}

func buildPluginMetricBatchRuntime(databases *storage.Databases, api *server.Handler, config pluginMetricBatchRuntimeConfig) (*server.PluginMetricsBatchHandler, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || config.maxObservations < 1 || config.maxSamples < 1 || config.maxBodyBytes < 1 {
		return nil, errors.New("storage-dir and all positive plugin metric batch limits must be configured together")
	}
	writer, err := storage.NewPluginMetricBatchWriterWithGuard(databases.Metrics, config.maxObservations, config.maxSamples, config.guard)
	if err != nil {
		return nil, err
	}
	return server.NewPluginMetricsBatchHandler(api, writer, config.maxObservations, config.maxSamples, config.maxBodyBytes)
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
	var historySource dashboard.HistorySource
	if config.historyTiers == nil {
		historySource, err = storage.NewMetricHistorySource(databases.Metrics, registry)
	} else {
		historySource, err = storage.NewTieredMetricHistorySource(databases.Metrics, registry, *config.historyTiers)
	}
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
	var pluginMetrics dashboard.PluginMetricPublicationSource
	if config.pluginMetrics {
		pluginMetrics, err = storage.NewPluginMetricDescriptorSource(databases.Metrics)
		if err != nil {
			return nil, err
		}
	}
	publication, err := dashboard.NewViewerPublicationPolicy(humanAuth.registry, pluginMetrics)
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

func buildAlertEvaluationRuntime(databases *storage.Databases, humanAuth *authRuntime, snapshots dashboard.SnapshotSource, ruleConfig alertRuleRuntimeConfig, config alertEvaluationRuntimeConfig) (*alertEvaluationRuntime, error) {
	if config.disabled() {
		return nil, nil
	}
	if !config.enabled || databases == nil || humanAuth == nil || humanAuth.handler == nil || humanAuth.registry == nil || snapshots == nil || ruleConfig.maxBodyBytes < 1 || ruleConfig.maxDepth < 1 || ruleConfig.maxConditions < 1 || config.interval <= 0 || config.timeout <= 0 || config.maxRulesPerRun < 1 || config.maxIncidentStates < 1 || config.maxIncidentArchives < 1 || config.pendingFor == nil || config.recoveryFor == nil || config.unknownBehavior == "" || config.disabledBehavior == "" {
		return nil, errors.New("enabled alert evaluation requires storage, authentication, current snapshots, alert-rule limits, positive scheduling and Incident capacities, and explicit Incident policy")
	}
	limits := alerts.Limits{MaxDepth: ruleConfig.maxDepth, MaxConditions: ruleConfig.maxConditions}
	rules, err := storage.NewAlertRuleRepository(databases.Core, humanAuth.registry, limits)
	if err != nil {
		return nil, err
	}
	incidents, err := storage.NewIncidentRepository(databases.Core, config.maxIncidentStates, config.maxIncidentArchives)
	if err != nil {
		return nil, err
	}
	samples, err := alerts.NewCurrentSnapshotSamples(snapshots, humanAuth.registry)
	if err != nil {
		return nil, err
	}
	evaluator, err := alerts.NewEvaluator(humanAuth.registry, limits)
	if err != nil {
		return nil, err
	}
	engine, err := alerts.NewEngine(samples, rules, incidents, evaluator, alerts.EngineConfig{
		Interval: config.interval, Timeout: config.timeout, MaxRulesPerRun: config.maxRulesPerRun,
		IncidentPolicy: alerts.IncidentPolicy{PendingFor: config.pendingFor, RecoveryFor: config.recoveryFor, UnknownBehavior: config.unknownBehavior, DisabledBehavior: config.disabledBehavior},
	}, logAlertEvaluationObservation)
	if err != nil {
		return nil, err
	}
	service, err := alerts.NewIncidentService(incidents)
	if err != nil {
		return nil, err
	}
	activation, err := alerts.NewIncidentActivationService(incidents)
	if err != nil {
		return nil, err
	}
	archives, err := alerts.NewIncidentArchiveService(incidents, config.maxIncidentArchives)
	if err != nil {
		return nil, err
	}
	handler, err := server.NewIncidentHandler(service, activation, archives, humanAuth.handler, ruleConfig.maxBodyBytes)
	if err != nil {
		return nil, err
	}
	var archiveRetention *storage.IncidentArchiveRetentionManager
	cleanupConfigured := config.archiveCleanupInterval != 0 || config.archiveRetention != 0 || config.archiveCleanupLimit != 0 || config.archiveCleanupTimeout != 0
	if cleanupConfigured {
		archiveRetention, err = storage.NewIncidentArchiveRetentionManager(databases.Core, storage.IncidentArchiveRetentionConfig{
			Interval: config.archiveCleanupInterval, Retention: config.archiveRetention,
			DeleteLimit: config.archiveCleanupLimit, Timeout: config.archiveCleanupTimeout,
		}, logIncidentArchiveRetentionObservation)
		if err != nil {
			return nil, err
		}
	}
	return &alertEvaluationRuntime{engine: engine, incidents: handler, archiveRetention: archiveRetention}, nil
}

func logIncidentArchiveRetentionObservation(observation storage.IncidentArchiveRetentionObservation) {
	if observation.Err != nil {
		log.Printf("component=alerts level=ERROR cutoff=%q deleted=%d duration=%q error=%q msg=%q", observation.Cutoff, observation.Deleted, observation.Duration, observation.Err, "bounded Incident archive retention failed")
		return
	}
	log.Printf("component=alerts level=INFO cutoff=%q deleted=%d duration=%q msg=%q", observation.Cutoff, observation.Deleted, observation.Duration, "bounded Incident archive retention completed")
}

func logAlertEvaluationObservation(observation alerts.EngineObservation) {
	if observation.Err != nil {
		log.Printf("component=alerts level=ERROR device_id=%q rule_id=%q revision=%d evaluation=%q duration=%q msg=%q", observation.DeviceID, observation.RuleID, observation.Revision, observation.Evaluation, observation.Duration, observation.Err)
		return
	}
	log.Printf("component=alerts level=INFO device_id=%q rule_id=%q revision=%d evaluation=%q incident=%q duration=%q msg=%q", observation.DeviceID, observation.RuleID, observation.Revision, observation.Evaluation, observation.Incident, observation.Duration, "alert rule evaluated")
}

func buildPluginRuntime(databases *storage.Databases, devices server.DeviceAuthorizer, humanAuth *authRuntime, config pluginRuntimeConfig) (*pluginRuntime, error) {
	if config.disabled() {
		return nil, nil
	}
	if databases == nil || devices == nil || humanAuth == nil || config.artifactDirectory == "" || config.maxArtifactBytes < 1 || config.maxRepositoryBytes < config.maxArtifactBytes || config.maxPackages < 1 || config.maxScanEntries < config.maxPackages || config.maxCollectionPolicies < 1 || config.cleanupInterval <= 0 || config.orphanGrace <= 0 || config.cleanupDeleteLimit < 1 || config.cleanupTimeout <= 0 {
		return nil, errors.New("storage, authentication, plugin artifact directory, capacity limits, and bounded cleanup settings are required together")
	}
	state, err := storage.NewBoundedPluginRepository(databases.Core, config.maxCollectionPolicies)
	if err != nil {
		return nil, err
	}
	artifacts, err := plugins.NewFileArtifactRepository(config.artifactDirectory, plugins.ArtifactRepositoryLimits{
		MaxArtifactBytes: config.maxArtifactBytes, MaxRepositoryBytes: config.maxRepositoryBytes,
		MaxPackages: config.maxPackages, MaxScanEntries: config.maxScanEntries,
	})
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
	mutation := &sync.Mutex{}
	admin, err := server.NewPluginAdminHandlerWithMutationLock(state, artifacts, mutation)
	if err != nil {
		return nil, err
	}
	policies, err := server.NewPluginCollectionPolicyHandler(state, artifacts, devices)
	if err != nil {
		return nil, err
	}
	maintenance, err := plugins.NewArtifactMaintenance(artifacts, state, mutation, plugins.ArtifactMaintenanceConfig{
		Interval: config.cleanupInterval, OrphanGrace: config.orphanGrace,
		DeleteLimit: config.cleanupDeleteLimit, Timeout: config.cleanupTimeout,
	}, logArtifactMaintenanceObservation)
	if err != nil {
		return nil, err
	}
	closeOnError = false
	return &pluginRuntime{control: control, artifacts: download, admin: admin, policies: policies, repository: artifacts, maintenance: maintenance}, nil
}

func logArtifactMaintenanceObservation(observation plugins.ArtifactMaintenanceObservation) {
	if observation.Err != nil {
		log.Printf("component=plugins level=ERROR cutoff=%q scanned_entries=%d packages=%d used_bytes=%d deleted_packages=%d deleted_bytes=%d duration=%q msg=%q", observation.Cutoff, observation.ScannedEntries, observation.Packages, observation.UsedBytes, observation.DeletedPackages, observation.DeletedBytes, observation.Duration, observation.Err)
		return
	}
	log.Printf("component=plugins level=INFO cutoff=%q scanned_entries=%d packages=%d used_bytes=%d deleted_packages=%d deleted_bytes=%d duration=%q msg=%q", observation.Cutoff, observation.ScannedEntries, observation.Packages, observation.UsedBytes, observation.DeletedPackages, observation.DeletedBytes, observation.Duration, "bounded plugin artifact cleanup completed")
}
