package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/hkjang/moyro/server/internal/approval"
	"github.com/hkjang/moyro/server/internal/mcpserver"
)

// ApprovalExecutor recovers protected actions that were approved just before
// a process crash or transient database failure. MCP post creation carries a
// unique approval_request_id, so retries resolve to the original side effect.
type ApprovalExecutor struct {
	approval *approval.Service
	mcp      *mcpserver.Service
	logger   *slog.Logger
	interval time.Duration
}

func newApprovalExecutor(native *nativeServices, logger *slog.Logger) *ApprovalExecutor {
	if native == nil || native.approval == nil || native.mcp == nil {
		return nil
	}
	return &ApprovalExecutor{approval: native.approval, mcp: native.mcp, logger: logger, interval: 5 * time.Second}
}

func (w *ApprovalExecutor) Run(ctx context.Context) {
	if w == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		w.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *ApprovalExecutor) runOnce(ctx context.Context) {
	requests, err := w.approval.PendingExecutions(ctx, 100)
	if err != nil {
		if ctx.Err() == nil {
			w.logger.Warn("approval recovery scan failed", "err", err)
		}
		return
	}
	for i := range requests {
		request := &requests[i]
		_, _, err := w.mcp.ExecuteApproved(ctx, request)
		if err == nil {
			continue
		}
		_ = w.approval.RecordExecutionFailure(ctx, request.ID, 30*time.Second)
		w.logger.Warn("approved action retry failed", "request_id", request.ID, "action", request.ActionType, "err", err)
	}
}
