package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrPluginRuntimeStuck = errors.New("plugin runtime is stuck and requires process restart")

type mattermostActivationResult struct {
	err error
}

// activateMattermostPlugin bounds the upstream Environment.Activate call.
// Mattermost's public runtime does not accept a context for OnActivate, so on
// Linux Moyro snapshots its existing direct children and terminates only the
// newly-created plugin process tree if the hook exceeds the deadline. Killing
// that process closes the RPC call and lets the upstream supervisor unwind.
func (h *Host) activateMattermostPlugin(ctx context.Context, p *Plugin) error {
	if h.mmEnv == nil {
		return errors.New("Mattermost runtime is unavailable")
	}
	if p == nil || p.Manifest == nil {
		return ErrPluginNotFound
	}
	baseline, err := snapshotDirectChildProcesses()
	if err != nil {
		return fmt.Errorf("prepare bounded plugin activation: %w", err)
	}
	timeout := h.activationTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	activationCtx, cancelActivation := context.WithTimeout(ctx, timeout)
	defer cancelActivation()

	done := make(chan mattermostActivationResult, 1)
	go func() {
		_, _, activateErr := h.mmEnv.Activate(p.Manifest.ID)
		done <- mattermostActivationResult{err: activateErr}
	}()

	select {
	case result := <-done:
		return result.err
	case <-activationCtx.Done():
		// Prefer a concurrently-completed activation over killing a healthy
		// process when the result and timer become ready together.
		select {
		case result := <-done:
			return result.err
		default:
		}
	}

	timeoutErr := fmt.Errorf("plugin %s activation exceeded %s: %w", p.Manifest.ID, timeout, activationCtx.Err())
	killErr := terminateNewChildProcessTrees(baseline)
	stopTimeout := h.activationStopTimeout
	if stopTimeout <= 0 {
		stopTimeout = 10 * time.Second
	}
	stopTimer := time.NewTimer(stopTimeout)
	defer stopTimer.Stop()
	select {
	case result := <-done:
		h.mmEnv.RemovePlugin(p.Manifest.ID)
		if result.err != nil {
			return errors.Join(timeoutErr, killErr, fmt.Errorf("plugin activation stopped: %w", result.err))
		}
		return errors.Join(timeoutErr, killErr)
	case <-stopTimer.C:
		h.mmEnv.RemovePlugin(p.Manifest.ID)
		h.runtimePoisoned.Store(true)
		return errors.Join(ErrPluginRuntimeStuck, timeoutErr, killErr, errors.New("plugin activation did not stop after process termination"))
	}
}
