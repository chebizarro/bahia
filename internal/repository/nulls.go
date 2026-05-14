package repository

import "database/sql"

func nullStringValue(v sql.NullString) string {
	if v.Valid {
		return v.String
	}
	return ""
}

func nullStringDefault(v sql.NullString, fallback string) string {
	if v.Valid {
		return v.String
	}
	return fallback
}

