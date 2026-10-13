package config

import "testing"

func TestBackupRunAsyncIntakeDefaultsOffAndRequiresExplicitConfig(t *testing.T) {
	if Defaults().BackupRun.AsyncIntakeEnabled {
		t.Fatal("backup run async intake must default off")
	}
	t.Setenv("BAHIA_BACKUP_RUN__ASYNC_INTAKE_ENABLED", "true")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.BackupRun.AsyncIntakeEnabled {
		t.Fatal("explicit backup run config was not loaded")
	}
}
