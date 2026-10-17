package hub

import "database/sql"

// SQLite cannot cover GROUP BY a JSON expression on this WITHOUT ROWID table:
// it seeks the payload for every result. Keep only derived aggregate counts in
// SQLite, transactionally maintained even by a rollback/older Hub binary.
func ensureTaskCountSchema(tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='task_status_counts'`).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		var serverColumn int
		if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('task_status_counts') WHERE name='server'`).Scan(&serverColumn); err != nil {
			return err
		}
		if serverColumn == 0 {
			for _, name := range []string{"task_counts_insert", "task_counts_delete", "task_counts_update_remove", "task_counts_update_add"} {
				if _, err := tx.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
					return err
				}
			}
			if _, err := tx.Exec("DROP TABLE task_status_counts"); err != nil {
				return err
			}
			exists = 0
		}
	}
	for _, ddl := range []string{
		`CREATE TABLE IF NOT EXISTS task_status_counts(kind TEXT NOT NULL,status TEXT NOT NULL,server TEXT NOT NULL,n INTEGER NOT NULL,PRIMARY KEY(kind,status,server)) WITHOUT ROWID`,
		`CREATE TRIGGER IF NOT EXISTS task_counts_insert AFTER INSERT ON task_records WHEN NEW.kind IN ('shell','relay') BEGIN INSERT INTO task_status_counts VALUES(NEW.kind,coalesce(json_extract(NEW.payload,'$.status'),''),coalesce(json_extract(NEW.payload,'$.server'),json_extract(NEW.payload,'$.agent_id'),''),1) ON CONFLICT(kind,status,server) DO UPDATE SET n=n+1; END`,
		`CREATE TRIGGER IF NOT EXISTS task_counts_delete AFTER DELETE ON task_records WHEN OLD.kind IN ('shell','relay') BEGIN UPDATE task_status_counts SET n=n-1 WHERE kind=OLD.kind AND status=coalesce(json_extract(OLD.payload,'$.status'),'') AND server=coalesce(json_extract(OLD.payload,'$.server'),json_extract(OLD.payload,'$.agent_id'),''); END`,
		`CREATE TRIGGER IF NOT EXISTS task_counts_update_remove AFTER UPDATE ON task_records WHEN OLD.kind IN ('shell','relay') BEGIN UPDATE task_status_counts SET n=n-1 WHERE kind=OLD.kind AND status=coalesce(json_extract(OLD.payload,'$.status'),'') AND server=coalesce(json_extract(OLD.payload,'$.server'),json_extract(OLD.payload,'$.agent_id'),''); END`,
		`CREATE TRIGGER IF NOT EXISTS task_counts_update_add AFTER UPDATE ON task_records WHEN NEW.kind IN ('shell','relay') BEGIN INSERT INTO task_status_counts VALUES(NEW.kind,coalesce(json_extract(NEW.payload,'$.status'),''),coalesce(json_extract(NEW.payload,'$.server'),json_extract(NEW.payload,'$.agent_id'),''),1) ON CONFLICT(kind,status,server) DO UPDATE SET n=n+1; END`,
	} {
		if _, err := tx.Exec(ddl); err != nil {
			return err
		}
	}
	if exists == 0 {
		_, err := tx.Exec(`INSERT INTO task_status_counts SELECT kind,coalesce(json_extract(payload,'$.status'),''),coalesce(json_extract(payload,'$.server'),json_extract(payload,'$.agent_id'),''),count(*) FROM task_records WHERE kind IN ('shell','relay') GROUP BY kind,coalesce(json_extract(payload,'$.status'),''),coalesce(json_extract(payload,'$.server'),json_extract(payload,'$.agent_id'),'')`)
		return err
	}
	return nil
}
