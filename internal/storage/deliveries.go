package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/shitamachi/forgelet/internal/delivery"
)

func (s *Store) EnqueueDelivery(ctx context.Context, r delivery.Record) (delivery.Record, error) {
	var result delivery.Record
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var body []byte
		err := tx.QueryRowContext(ctx, "SELECT body FROM deliveries WHERE id=?", r.ID).Scan(&body)
		if err == nil {
			if err := json.Unmarshal(body, &result); err != nil {
				return err
			}
			if result.RunID != r.RunID || result.JobID != r.JobID || result.Target.Name != r.Target.Name || result.Digest != r.Digest || result.RollbackOf != r.RollbackOf {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE state IN ('queued','preparing','awaiting_merge','delivered','deployed')").Scan(&count); err != nil {
			return err
		}
		if count >= 1024 {
			return ErrLimit
		}
		r.Version = 1
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO deliveries(id,run_id,target,state,version,created_at,updated_at,body) VALUES(?,?,?,?,?,?,?,?)", r.ID, r.RunID, r.Target.Name, r.State, r.Version, r.CreatedAt.UnixNano(), r.UpdatedAt.UnixNano(), data)
		result = r
		return err
	})
	return result, err
}
func (s *Store) SaveDelivery(ctx context.Context, r delivery.Record) error {
	oldVersion := r.Version
	r.Version++
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, "UPDATE deliveries SET state=?,version=?,updated_at=?,body=? WHERE id=? AND version=?", r.State, r.Version, r.UpdatedAt.UnixNano(), body, r.ID, oldVersion)
	if err != nil {
		return fmt.Errorf("save delivery: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (s *Store) GetDelivery(ctx context.Context, id string) (delivery.Record, error) {
	var r delivery.Record
	var body []byte
	if err := s.db.QueryRowContext(ctx, "SELECT body FROM deliveries WHERE id=?", id).Scan(&body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, errors.Join(ErrNotFound, delivery.ErrNotFound)
		}
		return r, notFound(err)
	}
	err := json.Unmarshal(body, &r)
	return r, err
}
func (s *Store) Deliveries(ctx context.Context, active bool) ([]delivery.Record, error) {
	query := "SELECT body FROM deliveries ORDER BY created_at DESC,id DESC LIMIT 100"
	if active {
		query = "SELECT body FROM deliveries WHERE state IN ('queued','preparing','awaiting_merge','delivered','deployed') ORDER BY created_at,id LIMIT 1024"
	}
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []delivery.Record{}
	for rows.Next() {
		var body []byte
		var r delivery.Record
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}
