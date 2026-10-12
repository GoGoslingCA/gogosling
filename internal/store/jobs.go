package store

import (
	"context"
	"database/sql"

	"github.com/GoGoslingCA/gogosling/internal/model"
)

const jobCols = `id, agent_id, name, schedule, timezone, kind, input, session_key, missed_policy,
	enabled, next_run_at, last_run_at, last_status, last_error, created_at, updated_at`

// CreateJob 插入定时任务；AgentID 不存在时返回 CodeConflict（外键）。
func (s *Store) CreateJob(ctx context.Context, j *model.Job) error {
	if err := requireID("job", j.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		_, err := q.ExecContext(ctx, `INSERT INTO jobs(`+jobCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			j.ID, j.AgentID, j.Name, j.Schedule, j.Timezone, string(j.Kind), j.Input, j.SessionKey,
			string(j.MissedPolicy), boolInt(j.Enabled), fmtTimePtr(j.NextRunAt), fmtTimePtr(j.LastRunAt),
			string(j.LastStatus), j.LastError, fmtTime(j.CreatedAt), fmtTime(j.UpdatedAt))
		return wrapErr(ctx, "create job", err)
	})
}

// GetJob 按 ID 读取定时任务。
func (s *Store) GetJob(ctx context.Context, id string) (*model.Job, error) {
	row := s.rq().QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id)
	j, err := scanJob(row)
	if err != nil {
		return nil, notFoundOr(ctx, "get job", "job", id, err)
	}
	return j, nil
}

// ListJobs 返回某智能体的任务（agentID 为空表示全部），CreatedAt 升序。
func (s *Store) ListJobs(ctx context.Context, agentID string) ([]*model.Job, error) {
	query := `SELECT ` + jobCols + ` FROM jobs`
	args := []any{}
	if agentID != "" {
		query += " WHERE agent_id = ?"
		args = append(args, agentID)
	}
	query += " ORDER BY created_at, id"
	rows, err := s.rq().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrapErr(ctx, "list jobs", err)
	}
	out, err := collect(rows, scanJob)
	if err != nil {
		return nil, wrapErr(ctx, "list jobs", err)
	}
	return out, nil
}

// UpdateJob 覆盖写入全部可变字段（AgentID 不可变）。
func (s *Store) UpdateJob(ctx context.Context, j *model.Job) error {
	if err := requireID("job", j.ID); err != nil {
		return err
	}
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `UPDATE jobs SET
			name = ?, schedule = ?, timezone = ?, kind = ?, input = ?, session_key = ?,
			missed_policy = ?, enabled = ?, next_run_at = ?, last_run_at = ?, last_status = ?,
			last_error = ?, updated_at = ?
			WHERE id = ?`,
			j.Name, j.Schedule, j.Timezone, string(j.Kind), j.Input, j.SessionKey,
			string(j.MissedPolicy), boolInt(j.Enabled), fmtTimePtr(j.NextRunAt), fmtTimePtr(j.LastRunAt),
			string(j.LastStatus), j.LastError, fmtTime(j.UpdatedAt), j.ID)
		return affected(ctx, "update job", "job", j.ID, res, err)
	})
}

// DeleteJob 删除定时任务。
func (s *Store) DeleteJob(ctx context.Context, id string) error {
	return s.write(ctx, func(q dbtx) error {
		res, err := q.ExecContext(ctx, `DELETE FROM jobs WHERE id = ?`, id)
		return affected(ctx, "delete job", "job", id, res, err)
	})
}

func scanJob(sc scanner) (*model.Job, error) {
	var (
		j          model.Job
		timezone   sql.NullString
		kind       string
		input      sql.NullString
		sessionKey sql.NullString
		missed     string
		enabled    dbBool
		nextRun    dbNullTime
		lastRun    dbNullTime
		lastStatus sql.NullString
		lastError  sql.NullString
		created    dbTime
		updated    dbTime
	)
	if err := sc.Scan(&j.ID, &j.AgentID, &j.Name, &j.Schedule, &timezone, &kind, &input, &sessionKey,
		&missed, &enabled, &nextRun, &lastRun, &lastStatus, &lastError, &created, &updated); err != nil {
		return nil, err
	}
	j.Timezone = timezone.String
	j.Kind = model.JobKind(kind)
	j.Input = input.String
	j.SessionKey = sessionKey.String
	j.MissedPolicy = model.MissedPolicy(missed)
	j.Enabled = enabled.v
	j.NextRunAt = nextRun.t
	j.LastRunAt = lastRun.t
	j.LastStatus = model.JobRunStatus(lastStatus.String)
	j.LastError = lastError.String
	j.CreatedAt = created.t
	j.UpdatedAt = updated.t
	return &j, nil
}
