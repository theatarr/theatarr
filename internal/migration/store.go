package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/theatarr/theatarr/internal/store"
)

// ErrNotFound is returned for an unknown plan id.
var ErrNotFound = errors.New("migration: no such plan")

// Summary is one saved plan in a list.
type Summary struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Steps     map[string]int `json:"steps"`
}

// Save stores a new plan with its inventory (without the raw arr bodies) and sets the plan's id.
func Save(ctx context.Context, st *store.Store, p *Plan, inv *Inventory) error {
	ib, err := json.Marshal(inv.WithoutRaw())
	if err != nil {
		return err
	}
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		pb, err := json.Marshal(p)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO migration_plan (name, inventory, plan, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)`, p.Name, string(ib), string(pb), store.Millis(p.CreatedAt), store.Millis(p.UpdatedAt))
		if err != nil {
			return err
		}
		if p.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		pb, err = json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE migration_plan SET plan = ? WHERE id = ?`, string(pb), p.ID)
		return err
	})
}

// update stores a plan's new state.
func update(ctx context.Context, st *store.Store, p *Plan) error {
	pb, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return st.WriteTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE migration_plan SET plan = ?, updated_at = ? WHERE id = ?`, string(pb),
			store.Millis(p.UpdatedAt), p.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		return nil
	})
}

// Load reads one plan and its inventory.
func Load(ctx context.Context, db *sql.DB, id int64) (*Plan, *Inventory, error) {
	var pb, ib string
	err := db.QueryRowContext(ctx, `SELECT plan, inventory FROM migration_plan WHERE id = ?`, id).Scan(&pb, &ib)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	var p Plan
	if err := json.Unmarshal([]byte(pb), &p); err != nil {
		return nil, nil, fmt.Errorf("plan %d: %w", id, err)
	}
	if p.SchemaVersion != SchemaVersion {
		return nil, nil, fmt.Errorf("plan %d has schema_version %d, this Theatarr reads %d", id, p.SchemaVersion, SchemaVersion)
	}
	var inv Inventory
	if err := json.Unmarshal([]byte(ib), &inv); err != nil {
		return nil, nil, fmt.Errorf("plan %d inventory: %w", id, err)
	}
	return &p, &inv, nil
}

// List summarises the saved plans, newest first.
func List(ctx context.Context, db *sql.DB) ([]Summary, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, plan FROM migration_plan ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Summary{}
	for rows.Next() {
		var id int64
		var pb string
		if err := rows.Scan(&id, &pb); err != nil {
			return nil, err
		}
		var p Plan
		if err := json.Unmarshal([]byte(pb), &p); err != nil {
			return nil, fmt.Errorf("plan %d: %w", id, err)
		}
		s := Summary{ID: id, Name: p.Name, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Steps: map[string]int{}}
		for _, st := range p.Steps {
			s.Steps[st.Status]++
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
