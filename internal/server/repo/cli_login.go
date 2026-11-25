package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// LoginPayload is the NOTIFY message of a CLI login: the new token's device row, no token and no
// hash. The stream sends it as `event: login` (the event key picks the name).
func LoginPayload(deviceName, os, cliVersion string, at time.Time) []byte {
	b, _ := json.Marshal(struct {
		Event      string `json:"event"`
		DeviceName string `json:"device_name"`
		OS         string `json:"os"`
		CLIVersion string `json:"cli_version"`
		At         string `json:"at"`
	}{"login", deviceName, os, cliVersion, at.UTC().Format(time.RFC3339)}) // plain fields: cannot fail
	return b
}

// InsertCLITokenAndNotify writes the token and, in the same transaction, tells the learner's stream.
func (s *Store) InsertCLITokenAndNotify(ctx context.Context, arg InsertCLITokenParams) (InsertCLITokenRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return InsertCLITokenRow{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := s.q.WithTx(tx).InsertCLIToken(ctx, arg)
	if err != nil {
		return InsertCLITokenRow{}, fmt.Errorf("insert cli token: %w", err)
	}
	if err := notifyLearner(ctx, tx, arg.LearnerID, LoginPayload(arg.DeviceName, arg.Os, arg.CliVersion, row.CreatedAt.Time)); err != nil {
		return InsertCLITokenRow{}, fmt.Errorf("notify login: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return InsertCLITokenRow{}, fmt.Errorf("commit: %w", err)
	}
	return row, nil
}
