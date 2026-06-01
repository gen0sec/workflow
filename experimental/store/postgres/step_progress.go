package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/deepnoodle-ai/workflow"
)

// stepProgressLockKey hashes the logical row identity
// (execution_id, step_name, branch_id) into a 64-bit advisory-lock
// key. Null-byte separators between the strings prevent
// "exe"+"abc"+"def" colliding with "ex"+"eab"+"cdef". FNV-1a is the
// cheap stdlib non-cryptographic hash — collisions cost an
// occasional extra serialization, not data corruption.
func stepProgressLockKey(executionID, stepName, branchID string) int64 {
	h := fnv.New64a()
	h.Write([]byte(executionID))
	h.Write([]byte{0})
	h.Write([]byte(stepName))
	h.Write([]byte{0})
	h.Write([]byte(branchID))
	// pg_advisory_xact_lock takes a BIGINT; the sign bit of a uint64
	// FNV-1a hash flows through as a negative number, which Postgres
	// accepts. Pre-2026 this used INSERT … ON CONFLICT against a
	// PRIMARY KEY on (execution_id, step_name, branch_id); the PK
	// was dropped to let consumers convert this table to a
	// TimescaleDB hypertable (hypertables reject any UNIQUE/PK that
	// excludes the partitioning column). Mutual exclusion is now
	// enforced at the application layer via this advisory lock.
	return int64(h.Sum64())
}

// UpdateStepProgress implements workflow.StepProgressStore by
// upserting one row per (execution_id, step_name, branch_id) — a
// step running on two branches produces two rows; the latest update
// to a given (exec, step, branch) wins.
//
// Pre-2026 this was an `INSERT … ON CONFLICT (execution_id, step_name,
// branch_id) DO UPDATE`. The PK that backed the conflict target was
// dropped to let consumers convert this table to a TimescaleDB
// hypertable (TimescaleDB rejects any UNIQUE/PK that excludes the
// partitioning column). The "one row per logical key" invariant now
// lives in this function: a per-key advisory lock serializes writers
// inside a transaction, then SELECT-decides between INSERT and UPDATE.
//
// The advisory lock scope is the transaction (`pg_advisory_xact_lock`,
// auto-released at COMMIT/ROLLBACK), so a crash here cannot leave
// the lock held.
func (s *Store) UpdateStepProgress(ctx context.Context, executionID string, p workflow.StepProgress) error {
	var detail []byte
	if p.Detail != nil {
		b, err := json.Marshal(p.Detail)
		if err != nil {
			return fmt.Errorf("postgres: marshal progress detail: %w", err)
		}
		detail = b
	}

	tbl := s.t("workflow_step_progress")
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin step progress %s/%s: %w", executionID, p.StepName, err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`,
		stepProgressLockKey(executionID, p.StepName, p.BranchID),
	); err != nil {
		return fmt.Errorf("postgres: lock step progress %s/%s: %w", executionID, p.StepName, err)
	}

	// EXISTS probe under the advisory lock: any other tx targeting
	// this same (exec, step, branch) is blocked at the lock above,
	// so this read sees the current "do we have a row?" answer
	// without race.
	var have bool
	probe := fmt.Sprintf(
		`SELECT EXISTS (SELECT 1 FROM %s WHERE execution_id=$1 AND step_name=$2 AND branch_id=$3)`,
		tbl)
	if err := tx.QueryRow(ctx, probe, executionID, p.StepName, p.BranchID).Scan(&have); err != nil {
		return fmt.Errorf("postgres: probe step progress %s/%s: %w", executionID, p.StepName, err)
	}

	if have {
		updQ := fmt.Sprintf(`
			UPDATE %s SET
				status      = $4,
				activity    = $5,
				attempt     = $6,
				detail      = $7,
				started_at  = $8,
				finished_at = $9,
				error       = $10,
				updated_at  = NOW()
			WHERE execution_id=$1 AND step_name=$2 AND branch_id=$3
		`, tbl)
		if _, err := tx.Exec(ctx, updQ,
			executionID, p.StepName, p.BranchID,
			string(p.Status), p.ActivityName, p.Attempt, detail,
			nullTime(p.StartedAt), nullTime(p.FinishedAt), p.Error,
		); err != nil {
			return fmt.Errorf("postgres: update step progress %s/%s: %w", executionID, p.StepName, err)
		}
	} else {
		insQ := fmt.Sprintf(`
			INSERT INTO %s (
				execution_id, step_name, branch_id, status, activity,
				attempt, detail, started_at, finished_at, error, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NOW())
		`, tbl)
		if _, err := tx.Exec(ctx, insQ,
			executionID, p.StepName, p.BranchID,
			string(p.Status), p.ActivityName, p.Attempt, detail,
			nullTime(p.StartedAt), nullTime(p.FinishedAt), p.Error,
		); err != nil {
			return fmt.Errorf("postgres: insert step progress %s/%s: %w", executionID, p.StepName, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit step progress %s/%s: %w", executionID, p.StepName, err)
	}
	return nil
}

// GetStepProgress returns every step progress row recorded for an
// execution, ordered by started_at (NULLS LAST) then step_name. One
// row per (step_name, branch_id) (enforced by the application-level
// advisory lock in UpdateStepProgress). Returns an empty slice if no
// rows exist. Use this on the read side to render per-step status for
// a run whose identity came back from runquery.Store.GetRun, which
// intentionally does not carry step progress.
func (s *Store) GetStepProgress(ctx context.Context, executionID string) ([]workflow.StepProgress, error) {
	query := fmt.Sprintf(`
		SELECT step_name, branch_id, status, activity, attempt,
		       detail, started_at, finished_at, error
		FROM %s
		WHERE execution_id = $1
		ORDER BY started_at ASC NULLS LAST, step_name ASC, branch_id ASC
	`, s.t("workflow_step_progress"))

	rows, err := s.pool.Query(ctx, query, executionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: query step progress %s: %w", executionID, err)
	}
	defer rows.Close()

	var out []workflow.StepProgress
	for rows.Next() {
		var (
			p          workflow.StepProgress
			status     string
			detail     []byte
			startedAt  *time.Time
			finishedAt *time.Time
		)
		if err := rows.Scan(
			&p.StepName, &p.BranchID, &status, &p.ActivityName, &p.Attempt,
			&detail, &startedAt, &finishedAt, &p.Error,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan step progress: %w", err)
		}
		p.Status = workflow.StepStatus(status)
		if len(detail) > 0 {
			pd := &workflow.ProgressDetail{}
			if err := json.Unmarshal(detail, pd); err != nil {
				return nil, fmt.Errorf("postgres: unmarshal progress detail for %s/%s: %w", executionID, p.StepName, err)
			}
			p.Detail = pd
		}
		if startedAt != nil {
			p.StartedAt = *startedAt
		}
		if finishedAt != nil {
			p.FinishedAt = *finishedAt
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate step progress %s: %w", executionID, err)
	}
	return out, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
