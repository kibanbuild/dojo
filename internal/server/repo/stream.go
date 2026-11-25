package repo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// LearnerChannel is the NOTIFY channel of one learner's progress (design 3.8, fan-out).
func LearnerChannel(learner pgtype.UUID) string {
	return "dojo_learner_" + hex.EncodeToString(learner.Bytes[:])
}

// ProgressPayload is the message of one changed ticket_progress row: no check output, so it stays
// far under the 8,000 byte NOTIFY limit.
func ProgressPayload(p TicketProgress) []byte {
	m := struct {
		ItemID      string  `json:"item_id"`
		State       string  `json:"state"`
		LabState    *string `json:"lab_state"`
		Lab         *string `json:"lab"`
		CheckCount  int32   `json:"check_count"`
		LastCheckOK *bool   `json:"last_check_ok"`
		LastCheckAt *string `json:"last_check_at"`
	}{ItemID: p.ItemID, State: p.State, CheckCount: p.CheckCount}
	if p.LabState.Valid {
		m.LabState = &p.LabState.String
	}
	if p.Lab.Valid {
		m.Lab = &p.Lab.String
	}
	if p.LastCheckOk.Valid {
		m.LastCheckOK = &p.LastCheckOk.Bool
	}
	if p.LastCheckAt.Valid {
		s := p.LastCheckAt.Time.UTC().Format(time.RFC3339Nano)
		m.LastCheckAt = &s
	}
	b, _ := json.Marshal(m) // plain fields: cannot fail
	return b
}

// notifyLearner sends one message on the learner's channel inside tx, so it is delivered at commit.
func notifyLearner(ctx context.Context, tx pgx.Tx, learner pgtype.UUID, msg []byte) error {
	_, err := tx.Exec(ctx, "SELECT pg_notify($1, $2)", LearnerChannel(learner), string(msg))
	return err
}

// notifyProgress sends one message per changed row inside tx, so it is delivered only at commit.
func notifyProgress(ctx context.Context, tx pgx.Tx, learner pgtype.UUID, rows map[string]*TicketProgress) error {
	items := make([]string, 0, len(rows))
	for id := range rows {
		items = append(items, id)
	}
	sort.Strings(items)
	for _, id := range items {
		if err := notifyLearner(ctx, tx, learner, ProgressPayload(*rows[id])); err != nil {
			return fmt.Errorf("notify: %w", err)
		}
	}
	return nil
}

// ListenConn opens a connection outside the pool for LISTEN, named so it can be told apart in pg_stat_activity.
func (s *Store) ListenConn(ctx context.Context) (*pgx.Conn, error) {
	cfg := s.pool.Config().ConnConfig.Copy()
	cfg.RuntimeParams["application_name"] = "dojo-api-listener"
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("listener connect: %w", err)
	}
	return conn, nil
}
