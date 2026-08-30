package pluginhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const pluginBackupPrefix = ".moyro-plugin-backup-"

const (
	installPhasePrepared    = "prepared"
	installPhaseMovingOld   = "moving_old"
	installPhaseOldBackedUp = "old_backed_up"
	installPhasePromoting   = "promoting_new"
	installPhaseRestoring   = "restoring_old"
	installPhaseRemoving    = "removing_new"
)

// pluginInstallTransaction is a durable rollback marker. While this row
// exists, startup always restores the previous directory, plugin metadata,
// configuration envelope, and KV namespace. Deleting the marker is the
// commit point for a successful install.
type pluginInstallTransaction struct {
	ID          string
	PluginID    string
	HadTarget   bool
	BackupName  string
	Phase       string
	RestoreData bool
}

func newPluginInstallTransactionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate plugin install transaction id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func (h *Host) beginInstallTransaction(ctx context.Context, pluginID string, hadTarget bool) (*pluginInstallTransaction, error) {
	if h.db == nil {
		return nil, errors.New("plugin install transaction database is unavailable")
	}
	id, err := newPluginInstallTransactionID()
	if err != nil {
		return nil, err
	}
	journal := &pluginInstallTransaction{
		ID: id, PluginID: pluginID, HadTarget: hadTarget, Phase: installPhasePrepared,
	}
	if hadTarget {
		journal.BackupName = pluginBackupPrefix + id
		backupPath := filepath.Join(h.dir, journal.BackupName)
		if _, err := os.Lstat(backupPath); err == nil {
			return nil, fmt.Errorf("plugin backup path already exists: %s", journal.BackupName)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect plugin backup path: %w", err)
		}
	}

	tx, err := h.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, fmt.Errorf("begin plugin install transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO plugin_install_transactions
			(id,plugin_id,had_target,backup_name,phase,create_at)
		VALUES ($1,$2,$3,$4,$5,$6)
	`, journal.ID, journal.PluginID, journal.HadTarget, journal.BackupName, journal.Phase, time.Now().UnixMilli()); err != nil {
		return nil, fmt.Errorf("record plugin install transaction: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO plugin_install_plugin_snapshots
			(transaction_id,plugin_id,version,state,manifest,create_at,update_at,
			 enabled,runtime_kind,bundle_sha256,last_error,installed_by,
			 installed_at,activated_at,config_key_id,config_nonce,config_ciphertext)
		SELECT $1,id,version,state,manifest,create_at,update_at,
		       enabled,runtime_kind,bundle_sha256,last_error,installed_by,
		       installed_at,activated_at,config_key_id,config_nonce,config_ciphertext
		FROM plugins WHERE id=$2
	`, journal.ID, journal.PluginID)
	if err != nil {
		return nil, fmt.Errorf("snapshot plugin metadata: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_install_kv_snapshots
				(transaction_id,key,value,expire_at,create_at,update_at)
			SELECT $1,key,value,expire_at,create_at,update_at
			FROM plugin_key_values WHERE plugin_id=$2
		`, journal.ID, journal.PluginID); err != nil {
			return nil, fmt.Errorf("snapshot plugin key/value data: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		committed, reconcileErr := h.installTransactionExists(journal)
		if reconcileErr != nil {
			h.runtimePoisoned.Store(true)
			return nil, errors.Join(
				ErrPluginRuntimeStuck,
				fmt.Errorf("commit plugin install snapshot: %w", err),
				fmt.Errorf("reconcile plugin install snapshot: %w", reconcileErr),
			)
		}
		if !committed {
			return nil, fmt.Errorf("commit plugin install snapshot: %w", err)
		}
	}
	return journal, nil
}

// refreshInstallSnapshot advances the rollback point after the previous
// plugin has been hidden from dispatch and fully deactivated. The original
// snapshot remains available if the process dies during deactivation; this
// replacement is one PostgreSQL transaction, so recovery observes either the
// complete pre-deactivation or complete post-deactivation state.
func (h *Host) refreshInstallSnapshot(ctx context.Context, journal *pluginInstallTransaction) error {
	if h.db == nil || journal == nil {
		return errors.New("plugin install transaction is unavailable")
	}
	tx, err := h.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return fmt.Errorf("begin plugin install snapshot refresh: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var pluginID string
	if err := tx.QueryRow(ctx, `
		SELECT plugin_id FROM plugin_install_transactions WHERE id=$1 FOR UPDATE
	`, journal.ID).Scan(&pluginID); err != nil {
		return fmt.Errorf("lock plugin install snapshot: %w", err)
	}
	if pluginID != journal.PluginID {
		return errors.New("plugin install snapshot identity mismatch")
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_install_kv_snapshots WHERE transaction_id=$1`, journal.ID); err != nil {
		return fmt.Errorf("clear previous plugin KV snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_install_plugin_snapshots WHERE transaction_id=$1`, journal.ID); err != nil {
		return fmt.Errorf("clear previous plugin metadata snapshot: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO plugin_install_plugin_snapshots
			(transaction_id,plugin_id,version,state,manifest,create_at,update_at,
			 enabled,runtime_kind,bundle_sha256,last_error,installed_by,
			 installed_at,activated_at,config_key_id,config_nonce,config_ciphertext)
		SELECT $1,id,version,state,manifest,create_at,update_at,
		       enabled,runtime_kind,bundle_sha256,last_error,installed_by,
		       installed_at,activated_at,config_key_id,config_nonce,config_ciphertext
		FROM plugins WHERE id=$2
	`, journal.ID, pluginID)
	if err != nil {
		return fmt.Errorf("refresh plugin metadata snapshot: %w", err)
	}
	if tag.RowsAffected() == 1 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_install_kv_snapshots
				(transaction_id,key,value,expire_at,create_at,update_at)
			SELECT $1,key,value,expire_at,create_at,update_at
			FROM plugin_key_values WHERE plugin_id=$2
		`, journal.ID, pluginID); err != nil {
			return fmt.Errorf("refresh plugin key/value snapshot: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit plugin install snapshot refresh: %w", err)
	}
	return nil
}

func (h *Host) setInstallPhase(ctx context.Context, journal *pluginInstallTransaction, phase string) error {
	if h.db == nil || journal == nil {
		return errors.New("plugin install transaction is unavailable")
	}
	switch phase {
	case installPhaseMovingOld, installPhaseOldBackedUp, installPhasePromoting, installPhaseRestoring, installPhaseRemoving:
	default:
		return fmt.Errorf("invalid plugin install phase %q", phase)
	}
	tag, err := h.db.Pool.Exec(ctx, `
		UPDATE plugin_install_transactions SET phase=$3 WHERE id=$1 AND plugin_id=$2
	`, journal.ID, journal.PluginID, phase)
	if err != nil {
		return fmt.Errorf("record plugin install phase %s: %w", phase, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("record plugin install phase %s: journal is missing", phase)
	}
	journal.Phase = phase
	return nil
}

func (h *Host) commitInstallTransaction(ctx context.Context, journal *pluginInstallTransaction) error {
	if h.db == nil || journal == nil {
		return errors.New("plugin install transaction is unavailable")
	}
	tag, err := h.db.Pool.Exec(ctx, `DELETE FROM plugin_install_transactions WHERE id=$1 AND plugin_id=$2`, journal.ID, journal.PluginID)
	if err != nil {
		exists, reconcileErr := h.installTransactionExists(journal)
		if reconcileErr != nil {
			h.runtimePoisoned.Store(true)
			return errors.Join(
				ErrPluginRuntimeStuck,
				fmt.Errorf("commit plugin install transaction: %w", err),
				fmt.Errorf("reconcile plugin install commit: %w", reconcileErr),
			)
		}
		if !exists {
			// PostgreSQL committed the marker deletion but the connection failed
			// before pgx could observe the result. Treat the durable state as the
			// authoritative successful outcome; rolling back here would pair old
			// files with already-committed new metadata.
			return nil
		}
		return fmt.Errorf("commit plugin install transaction: %w", err)
	}
	if tag.RowsAffected() != 1 {
		exists, reconcileErr := h.installTransactionExists(journal)
		if reconcileErr != nil {
			h.runtimePoisoned.Store(true)
			return errors.Join(
				ErrPluginRuntimeStuck,
				errors.New("commit plugin install transaction: journal delete affected no rows"),
				fmt.Errorf("reconcile plugin install commit: %w", reconcileErr),
			)
		}
		if !exists {
			// The durable commit marker is already absent. This may be a retry
			// after the original DELETE committed, so rollback is no longer safe.
			return nil
		}
		return errors.New("commit plugin install transaction: journal delete affected no rows")
	}
	return nil
}

func (h *Host) installTransactionExists(journal *pluginInstallTransaction) (bool, error) {
	if h.db == nil || journal == nil {
		return false, errors.New("plugin install transaction database is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var exists bool
	if err := h.db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM plugin_install_transactions WHERE id=$1 AND plugin_id=$2
		)
	`, journal.ID, journal.PluginID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

func (h *Host) recoverInstallTransactions(ctx context.Context) error {
	if h.db == nil {
		return nil
	}
	rows, err := h.db.Pool.Query(ctx, `
		SELECT id,plugin_id,had_target,backup_name,phase
		FROM plugin_install_transactions ORDER BY create_at,id
	`)
	if err != nil {
		return fmt.Errorf("list incomplete plugin installs: %w", err)
	}
	journals := make([]*pluginInstallTransaction, 0)
	for rows.Next() {
		journal := &pluginInstallTransaction{}
		if err := rows.Scan(&journal.ID, &journal.PluginID, &journal.HadTarget, &journal.BackupName, &journal.Phase); err != nil {
			rows.Close()
			return fmt.Errorf("scan incomplete plugin install: %w", err)
		}
		journals = append(journals, journal)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate incomplete plugin installs: %w", err)
	}
	rows.Close()

	for _, journal := range journals {
		if err := h.restoreInstallFiles(ctx, journal); err != nil {
			return fmt.Errorf("recover plugin %s files: %w", journal.PluginID, err)
		}
		if err := h.restoreInstallDatabase(ctx, journal); err != nil {
			return fmt.Errorf("recover plugin %s data: %w", journal.PluginID, err)
		}
		h.logger.Warn("rolled back incomplete plugin install", "plugin", journal.PluginID, "transaction", journal.ID)
	}
	return nil
}

func (h *Host) restoreInstallFiles(ctx context.Context, journal *pluginInstallTransaction) error {
	if journal == nil || journal.PluginID == "" {
		return errors.New("invalid plugin install journal")
	}
	target, err := securePluginPath(h.dir, journal.PluginID)
	if err != nil || filepath.Dir(target) != filepath.Clean(h.dir) {
		return fmt.Errorf("invalid journal plugin id %q", journal.PluginID)
	}
	targetExists, err := pathExists(target)
	if err != nil {
		return fmt.Errorf("inspect plugin target: %w", err)
	}

	if !journal.HadTarget {
		if journal.BackupName != "" {
			return errors.New("new plugin journal unexpectedly has a backup")
		}
		if journal.Phase != installPhasePrepared && journal.Phase != installPhasePromoting && journal.Phase != installPhaseRemoving {
			return fmt.Errorf("invalid new plugin recovery phase %q", journal.Phase)
		}
		journal.RestoreData = journal.Phase != installPhasePrepared || targetExists
		if journal.Phase != installPhaseRemoving {
			if err := h.setInstallPhase(ctx, journal, installPhaseRemoving); err != nil {
				return err
			}
		}
		if targetExists {
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("remove incomplete plugin target: %w", err)
			}
			return syncDirectory(h.dir)
		}
		return nil
	}

	if !validBackupName(journal.ID, journal.BackupName) {
		return fmt.Errorf("invalid plugin backup name %q", journal.BackupName)
	}
	backup := filepath.Join(h.dir, journal.BackupName)
	backupExists, err := pathExists(backup)
	if err != nil {
		return fmt.Errorf("inspect plugin backup: %w", err)
	}
	if !backupExists {
		// The durable marker is written before the first rename. A target with
		// no backup therefore means either no rename occurred or file rollback
		// already completed before a prior recovery attempt was interrupted.
		if targetExists && (journal.Phase == installPhasePrepared || journal.Phase == installPhaseMovingOld) {
			// No filesystem switch occurred. Keep the live DB state instead of
			// replaying the preliminary snapshot: in-flight work or OnDeactivate
			// may have committed legitimate final writes after that snapshot.
			journal.RestoreData = false
			return nil
		}
		if targetExists && journal.Phase == installPhaseRestoring {
			journal.RestoreData = true
			return nil
		}
		return fmt.Errorf("previous plugin backup is missing during phase %q", journal.Phase)
	}
	if err := h.setInstallPhase(ctx, journal, installPhaseRestoring); err != nil {
		return err
	}
	journal.RestoreData = true
	if targetExists {
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove incomplete replacement target: %w", err)
		}
	}
	if err := os.Rename(backup, target); err != nil {
		return fmt.Errorf("restore previous plugin target: %w", err)
	}
	return syncDirectory(h.dir)
}

func (h *Host) restoreInstallDatabase(ctx context.Context, journal *pluginInstallTransaction) error {
	if h.db == nil || journal == nil {
		return errors.New("plugin install transaction database is unavailable")
	}
	tx, err := h.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin plugin install recovery: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var pluginID string
	if err := tx.QueryRow(ctx, `
		SELECT plugin_id FROM plugin_install_transactions WHERE id=$1 FOR UPDATE
	`, journal.ID).Scan(&pluginID); err != nil {
		return fmt.Errorf("lock plugin install journal: %w", err)
	}
	if pluginID != journal.PluginID {
		return errors.New("plugin install journal identity mismatch")
	}
	if !journal.RestoreData {
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_install_transactions WHERE id=$1`, journal.ID); err != nil {
			return fmt.Errorf("discard unmutated plugin install journal: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit unmutated plugin install recovery: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_key_values WHERE plugin_id=$1`, pluginID); err != nil {
		return fmt.Errorf("clear tentative plugin key/value data: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO plugins
			(id,version,state,manifest,create_at,update_at,enabled,runtime_kind,
			 bundle_sha256,last_error,installed_by,installed_at,activated_at,
			 config_key_id,config_nonce,config_ciphertext)
		SELECT plugin_id,version,state,manifest,create_at,update_at,enabled,runtime_kind,
		       bundle_sha256,last_error,installed_by,installed_at,activated_at,
		       config_key_id,config_nonce,config_ciphertext
		FROM plugin_install_plugin_snapshots WHERE transaction_id=$1 AND plugin_id=$2
		ON CONFLICT (id) DO UPDATE SET
			version=EXCLUDED.version,state=EXCLUDED.state,manifest=EXCLUDED.manifest,
			create_at=EXCLUDED.create_at,update_at=EXCLUDED.update_at,
			enabled=EXCLUDED.enabled,runtime_kind=EXCLUDED.runtime_kind,
			bundle_sha256=EXCLUDED.bundle_sha256,last_error=EXCLUDED.last_error,
			installed_by=EXCLUDED.installed_by,installed_at=EXCLUDED.installed_at,
			activated_at=EXCLUDED.activated_at,config_key_id=EXCLUDED.config_key_id,
			config_nonce=EXCLUDED.config_nonce,config_ciphertext=EXCLUDED.config_ciphertext
	`, journal.ID, pluginID)
	if err != nil {
		return fmt.Errorf("restore plugin metadata: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM plugins WHERE id=$1`, pluginID); err != nil {
			return fmt.Errorf("remove tentative plugin metadata: %w", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_key_values (plugin_id,key,value,expire_at,create_at,update_at)
			SELECT $2,key,value,expire_at,create_at,update_at
			FROM plugin_install_kv_snapshots WHERE transaction_id=$1
		`, journal.ID, pluginID); err != nil {
			return fmt.Errorf("restore plugin key/value data: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_install_transactions WHERE id=$1`, journal.ID); err != nil {
		return fmt.Errorf("finish plugin install recovery: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit plugin install recovery: %w", err)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func validBackupName(transactionID, name string) bool {
	return transactionID != "" && name == pluginBackupPrefix+transactionID &&
		filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		return fmt.Errorf("sync directory: %w", err)
	}
	if err := dir.Close(); err != nil {
		return fmt.Errorf("close synced directory: %w", err)
	}
	return nil
}
