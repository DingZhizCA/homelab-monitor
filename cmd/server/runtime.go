/*
Package main assembles optional persistence, WAL maintenance, human
authentication, and metric query runtimes from explicit Server command-line
configuration.

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
}

type checkpointRuntimeConfig struct {
	interval          time.Duration
	walThresholdBytes int64
	timeout           time.Duration
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
	handler *server.AuthHandler
	queries *server.MetricQueryHandler
	history *server.MetricHistoryHandler
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
	writer, err := storage.NewMetricBatchWriter(databases.Metrics, config.maxSnapshots)
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
	return &authRuntime{handler: handler, queries: queries, history: history}, nil
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
