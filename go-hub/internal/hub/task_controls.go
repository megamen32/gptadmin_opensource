package hub

import (
	"database/sql"
	"errors"
)

// Cancellation shares the task transaction and survives a lost poll response.
// The original task's final result is its acknowledgement; until then any Hub
// may redeliver the control after 30 seconds. The worker action is idempotent.
func (s *Server) nextDurableTaskControlLocked(server string) (string, error) {
	if s.cfg.ConfigDir == "" {
		return "", nil
	}
	var id string
	err := s.withTaskDatabase(func(t *taskTransaction) error {
		err := t.tx.QueryRow(`SELECT task_id FROM task_controls WHERE server=? AND next_attempt<=? ORDER BY next_attempt,task_id LIMIT 1`, server, nowFloat()).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = t.tx.Exec(`UPDATE task_controls SET next_attempt=? WHERE task_id=?`, nowFloat()+30, id)
		return err
	})
	if err != nil {
		return "", err
	}
	if id != "" {
		s.removeLocalTaskControlLocked(server, id)
	}
	return id, nil
}
func (s *Server) removeLocalTaskControlLocked(server, id string) {
	queue := s.shellControls[server]
	out := queue[:0]
	for _, item := range queue {
		if item.TaskID != id {
			out = append(out, item)
		}
	}
	s.shellControls[server] = out
}
func (s *Server) acknowledgeTaskControlLocked(server, id string) error {
	if err := s.withTaskDatabase(func(t *taskTransaction) error {
		_, err := t.tx.Exec(`DELETE FROM task_controls WHERE task_id=? AND server=?`, id, server)
		return err
	}); err != nil {
		return err
	}
	s.removeLocalTaskControlLocked(server, id)
	return nil
}
