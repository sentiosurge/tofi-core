package storage

// ListAllApps returns all apps across all users (for migration).
func (db *DB) ListAllApps() ([]*AppRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at,
		COALESCE(parameters,'{}'), COALESCE(parameter_defs,'{}'),
		COALESCE(source,'local'), COALESCE(source_url,''), COALESCE(version,''), COALESCE(author,''),
		COALESCE(manifest_json,''), COALESCE(has_scripts,0)
		FROM apps ORDER BY user_id, name`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAppRecords(rows)
}

// GetAppByName finds an app by name and user ID.
func (db *DB) GetAppByName(userID, name string) (*AppRecord, error) {
	query := `SELECT id, name, COALESCE(description,''), COALESCE(prompt,''), COALESCE(system_prompt,''),
		COALESCE(model,''), COALESCE(skills,'[]'), COALESCE(schedule_rules,'[]'), COALESCE(capabilities,'{}'),
		buffer_size, renewal_threshold, is_active, user_id, created_at, updated_at,
		COALESCE(parameters,'{}'), COALESCE(parameter_defs,'{}'),
		COALESCE(source,'local'), COALESCE(source_url,''), COALESCE(version,''), COALESCE(author,''),
		COALESCE(manifest_json,''), COALESCE(has_scripts,0)
		FROM apps WHERE user_id = ? AND name = ? LIMIT 1`
	row := db.conn.QueryRow(query, userID, name)
	return scanAppRecord(row)
}
