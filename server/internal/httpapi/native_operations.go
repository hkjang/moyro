package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hkjang/moyro/server/internal/store"
	"github.com/jackc/pgx/v5"
)

type operationalState string

const (
	operationalReady   operationalState = "ready"
	operationalWarning operationalState = "warning"
	operationalUnknown operationalState = "unknown"
)

type databasePoolStatus struct {
	Acquired int32 `json:"acquired"`
	Idle     int32 `json:"idle"`
	Total    int32 `json:"total"`
	Max      int32 `json:"max"`
}

type databaseMigrationStatus struct {
	AppliedVersion int64  `json:"applied_version"`
	AppliedName    string `json:"applied_name"`
	AppliedAt      int64  `json:"applied_at"`
	TargetVersion  int64  `json:"target_version"`
	TargetName     string `json:"target_name"`
}

type databaseOperationalStatus struct {
	State     operationalState        `json:"state"`
	Message   string                  `json:"message"`
	Migration databaseMigrationStatus `json:"migration"`
	Pool      databasePoolStatus      `json:"pool"`
}

type scheduledQueueStatus struct {
	Pending       int64 `json:"pending"`
	Processing    int64 `json:"processing"`
	Retry         int64 `json:"retry"`
	Dead          int64 `json:"dead"`
	Due           int64 `json:"due"`
	ExpiredLeases int64 `json:"expired_leases"`
}

type reminderQueueStatus struct {
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Due        int64 `json:"due"`
}

type approvalQueueStatus struct {
	Pending    int64 `json:"pending"`
	Processing int64 `json:"processing"`
	Failed     int64 `json:"failed"`
}

type workerOperationalStatus struct {
	State             operationalState     `json:"state"`
	Message           string               `json:"message"`
	RuntimeObservable bool                 `json:"runtime_observable"`
	Scheduled         scheduledQueueStatus `json:"scheduled"`
	Reminders         reminderQueueStatus  `json:"reminders"`
	Approvals         approvalQueueStatus  `json:"approvals"`
}

type webhookOperationalStatus struct {
	State             operationalState `json:"state"`
	Message           string           `json:"message"`
	RuntimeObservable bool             `json:"runtime_observable"`
	Pending           int64            `json:"pending"`
	Processing        int64            `json:"processing"`
	Retry             int64            `json:"retry"`
	Succeeded         int64            `json:"succeeded"`
	Dead              int64            `json:"dead"`
	ExpiredLeases     int64            `json:"expired_leases"`
	LastSucceededAt   int64            `json:"last_succeeded_at"`
	LastDeadAt        int64            `json:"last_dead_at"`
}

type storageOperationalStatus struct {
	State             operationalState `json:"state"`
	Message           string           `json:"message"`
	ConfiguredBackend string           `json:"configured_backend"`
	ActiveBackend     string           `json:"active_backend"`
	Fallback          bool             `json:"fallback"`
	FileCount         int64            `json:"file_count"`
	Bytes             int64            `json:"bytes"`
}

type nativeOperationsSnapshot struct {
	CheckedAt int64                     `json:"checked_at"`
	Database  databaseOperationalStatus `json:"database"`
	Workers   workerOperationalStatus   `json:"workers"`
	Webhooks  webhookOperationalStatus  `json:"webhooks"`
	Storage   storageOperationalStatus  `json:"storage"`
}

type nativeOperationsReader interface {
	Snapshot(context.Context) nativeOperationsSnapshot
}

type fileStorageRuntime struct {
	ConfiguredBackend string
	ActiveBackend     string
	Fallback          bool
	FilesystemRoot    string
}

type postgresOperationsReader struct {
	db              *store.DB
	storage         fileStorageRuntime
	targetVersion   int64
	targetName      string
	targetAvailable bool
}

func newPostgresOperationsReader(db *store.DB, storageRuntime fileStorageRuntime) *postgresOperationsReader {
	version, name, err := store.EmbeddedMigrationTarget()
	return &postgresOperationsReader{
		db: db, storage: storageRuntime,
		targetVersion: version, targetName: name, targetAvailable: err == nil,
	}
}

func (r *postgresOperationsReader) Snapshot(ctx context.Context) nativeOperationsSnapshot {
	now := time.Now().UnixMilli()
	storageRuntime := fileStorageRuntime{}
	if r != nil {
		storageRuntime = r.storage
	}
	snapshot := nativeOperationsSnapshot{
		CheckedAt: now,
		Database:  databaseOperationalStatus{State: operationalUnknown, Message: "데이터베이스 상태를 확인하지 못했습니다."},
		Workers: workerOperationalStatus{
			State: operationalUnknown, RuntimeObservable: false,
			Message: "작업 큐와 Worker 실행 상태를 확인하지 못했습니다.",
		},
		Webhooks: webhookOperationalStatus{
			State: operationalUnknown, RuntimeObservable: false,
			Message: "Webhook 전달 큐와 실행 상태를 확인하지 못했습니다.",
		},
		Storage: storageOperationalStatus{
			State: operationalUnknown, Message: "파일 저장소 상태를 확인하지 못했습니다.",
			ConfiguredBackend: normalizedBackend(storageRuntime.ConfiguredBackend),
			ActiveBackend:     normalizedBackend(storageRuntime.ActiveBackend),
			Fallback:          storageRuntime.Fallback,
		},
	}
	if r == nil || r.db == nil || r.db.Pool == nil {
		return snapshot
	}

	snapshot.Database = r.readDatabase(ctx)
	snapshot.Workers = r.readWorkers(ctx, now)
	snapshot.Webhooks = r.readWebhooks(ctx, now)
	snapshot.Storage = r.readStorage(ctx)
	return snapshot
}

func normalizedBackend(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "fs"
	}
	return value
}

func (r *postgresOperationsReader) readDatabase(ctx context.Context) databaseOperationalStatus {
	status := databaseOperationalStatus{State: operationalUnknown, Message: "PostgreSQL 응답을 확인하지 못했습니다."}
	poolStats := r.db.Pool.Stat()
	status.Pool = databasePoolStatus{
		Acquired: poolStats.AcquiredConns(), Idle: poolStats.IdleConns(),
		Total: poolStats.TotalConns(), Max: poolStats.MaxConns(),
	}
	if err := r.db.Pool.Ping(ctx); err != nil {
		return status
	}
	status.Migration.TargetVersion = r.targetVersion
	status.Migration.TargetName = r.targetName
	err := r.db.Pool.QueryRow(ctx, `
		SELECT version, name, applied_at
		FROM schema_migrations
		ORDER BY version DESC
		LIMIT 1
	`).Scan(
		&status.Migration.AppliedVersion,
		&status.Migration.AppliedName,
		&status.Migration.AppliedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		status.State = operationalWarning
		status.Message = "PostgreSQL은 응답하지만 적용된 Migration 기록이 없습니다."
		return status
	}
	if err != nil || !r.targetAvailable {
		status.Message = "PostgreSQL은 응답하지만 Migration 일치 여부를 확인하지 못했습니다."
		return status
	}
	if status.Migration.AppliedVersion != status.Migration.TargetVersion || status.Migration.AppliedName != status.Migration.TargetName {
		status.State = operationalWarning
		status.Message = "적용된 Migration과 실행 이미지의 목표 Migration이 다릅니다."
		return status
	}
	status.State = operationalReady
	status.Message = "PostgreSQL 응답과 Migration 목표 일치를 확인했습니다."
	return status
}

func (r *postgresOperationsReader) readWorkers(ctx context.Context, now int64) workerOperationalStatus {
	status := workerOperationalStatus{
		State: operationalUnknown, RuntimeObservable: false,
		Message: "큐 수치는 조회됐지만 Worker heartbeat가 없어 실행 상태는 미확인입니다.",
	}
	err := r.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status='pending'),
			COUNT(*) FILTER (WHERE status='processing'),
			COUNT(*) FILTER (WHERE status='retry'),
			COUNT(*) FILTER (WHERE status='dead'),
			COUNT(*) FILTER (WHERE status IN ('pending','retry') AND next_attempt_at <= $1),
			COUNT(*) FILTER (WHERE status='processing' AND lease_until <= $1)
		FROM scheduled_posts
	`, now).Scan(
		&status.Scheduled.Pending,
		&status.Scheduled.Processing,
		&status.Scheduled.Retry,
		&status.Scheduled.Dead,
		&status.Scheduled.Due,
		&status.Scheduled.ExpiredLeases,
	)
	if err != nil {
		status.Message = "예약 메시지 작업 큐를 확인하지 못했습니다."
		return status
	}
	err = r.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE delivered_at=0),
			COUNT(*) FILTER (WHERE delivered_at=-1),
			COUNT(*) FILTER (WHERE delivered_at=0 AND remind_at <= $1)
		FROM post_reminders
	`, now).Scan(&status.Reminders.Pending, &status.Reminders.Processing, &status.Reminders.Due)
	if err != nil {
		status.Message = "리마인더 작업 큐를 확인하지 못했습니다."
		return status
	}
	err = r.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status='pending'),
			COUNT(*) FILTER (WHERE status='processing'),
			COUNT(*) FILTER (WHERE status='failed')
		FROM workflow_outbox
	`).Scan(&status.Approvals.Pending, &status.Approvals.Processing, &status.Approvals.Failed)
	if err != nil {
		status.Message = "승인 실행 작업 큐를 확인하지 못했습니다."
		return status
	}
	if status.Scheduled.Dead > 0 || status.Scheduled.ExpiredLeases > 0 || status.Approvals.Failed > 0 {
		status.State = operationalWarning
		status.Message = "실패 작업 또는 만료된 Lease가 있습니다. Worker 실행 상태는 별도로 확인해야 합니다."
	}
	return status
}

func (r *postgresOperationsReader) readWebhooks(ctx context.Context, now int64) webhookOperationalStatus {
	status := webhookOperationalStatus{
		State: operationalUnknown, RuntimeObservable: false,
		Message: "전달 큐 수치는 조회됐지만 Dispatcher heartbeat가 없어 실행 상태는 미확인입니다.",
	}
	err := r.db.Pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status='pending'),
			COUNT(*) FILTER (WHERE status='processing'),
			COUNT(*) FILTER (WHERE status='retry'),
			COUNT(*) FILTER (WHERE status='succeeded'),
			COUNT(*) FILTER (WHERE status='dead'),
			COUNT(*) FILTER (WHERE status='processing' AND lease_until <= $1),
			COALESCE(MAX(succeeded_at) FILTER (WHERE status='succeeded'), 0),
			COALESCE(MAX(dead_at) FILTER (WHERE status='dead'), 0)
		FROM integration_deliveries
	`, now).Scan(
		&status.Pending,
		&status.Processing,
		&status.Retry,
		&status.Succeeded,
		&status.Dead,
		&status.ExpiredLeases,
		&status.LastSucceededAt,
		&status.LastDeadAt,
	)
	if err != nil {
		status.Message = "Webhook 전달 큐를 확인하지 못했습니다."
		return status
	}
	if status.Retry > 0 || status.Dead > 0 || status.ExpiredLeases > 0 {
		status.State = operationalWarning
		status.Message = "재시도, DLQ 또는 만료된 Lease가 있습니다. Dispatcher 실행 상태는 별도로 확인해야 합니다."
	}
	return status
}

func (r *postgresOperationsReader) readStorage(ctx context.Context) storageOperationalStatus {
	status := storageOperationalStatus{
		State:             operationalUnknown,
		ConfiguredBackend: normalizedBackend(r.storage.ConfiguredBackend),
		ActiveBackend:     normalizedBackend(r.storage.ActiveBackend),
		Fallback:          r.storage.Fallback,
		Message:           "저장소 연결 상태를 확인하지 못했습니다.",
	}
	if err := r.db.Pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size), 0)
		FROM file_infos
		WHERE delete_at=0
	`).Scan(&status.FileCount, &status.Bytes); err != nil {
		status.Message = "파일 메타데이터 사용량을 확인하지 못했습니다."
		return status
	}
	if status.Fallback {
		status.State = operationalWarning
		status.Message = "설정한 저장소를 초기화하지 못해 로컬 파일시스템으로 대체되었습니다."
		return status
	}
	if status.ActiveBackend != "fs" {
		status.Message = "파일 메타데이터는 조회됐지만 원격 저장소 연결 Probe는 제공되지 않습니다."
		return status
	}
	info, err := os.Stat(r.storage.FilesystemRoot)
	if err != nil || !info.IsDir() {
		status.State = operationalWarning
		status.Message = "로컬 파일 저장 경로를 확인하지 못했습니다. 쓰기 검사는 수행하지 않았습니다."
		return status
	}
	status.State = operationalReady
	status.Message = "로컬 파일 저장 경로와 메타데이터 조회를 확인했습니다. 쓰기 검사는 수행하지 않았습니다."
	return status
}

func (h *handlers) getNativeAdminOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.operations == nil {
		writeError(w, http.StatusServiceUnavailable, "api.moyro.admin.operations.unavailable", "operational diagnostics are unavailable")
		return
	}
	writeJSON(w, http.StatusOK, h.operations.Snapshot(r.Context()))
}
