package vm

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestDeploymentDeletionDataDispositionAndReplay(t *testing.T) {
	for _, disposition := range []domain.VMDataDisposition{"", domain.VMDataRetain, domain.VMDataExport, domain.VMDataDelete} {
		t.Run("disposition-"+string(disposition), func(t *testing.T) {
			p, f, q := checkpointRequest(t)
			dir := filepath.Dir(p.recordPath(f.resource.ID))
			originalDisk := f.resource.Components[domain.VMComponentDisk]
			nvram := filepath.Join(dir, "nvram.fd")
			tpm := filepath.Join(dir, "swtpm")
			oldSet := filepath.Join(dir, "set-old", "disk")
			if err := os.MkdirAll(tpm, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(oldSet), 0700); err != nil {
				t.Fatal(err)
			}
			for path, value := range map[string]string{nvram: "NVRAM bytes", filepath.Join(tpm, "tpm-state"): "TPM bytes", oldSet: "old restore set"} {
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			q.Deployment.Firmware = domain.VMFirmwareUEFI
			q.Deployment.TPM.Enabled = true
			tpmID := uuid.New()
			q.Deployment.TPM.IdentityID = &tpmID
			f.resource.Components[domain.VMComponentNVRAM] = nvram
			f.resource.Components[domain.VMComponentSWTPM] = tpm
			if err := p.writeRecord(context.Background(), q.Deployment, f.resource); err != nil {
				t.Fatal(err)
			}
			q.Operation.Kind = domain.VMOperationDelete
			q.Operation.DeleteTarget = domain.VMDeleteDeployment
			q.Operation.CheckpointID = nil
			q.Checkpoint = nil
			q.Operation.DataDisposition = disposition
			q.Operation.ID = uuid.New()
			q.Operation.RequiredTier = domain.VMApprovalDestructive
			approval := uuid.New()
			q.Operation.ApprovalID = &approval
			q.Operation.ProviderFingerprint = f.resource.Fingerprint
			for attempt := 0; attempt < 2; attempt++ {
				result, err := p.Execute(context.Background(), q)
				if err != nil || !result.Confirmed || *result.Observation.RuntimeState != domain.VMRuntimeAbsent {
					t.Fatalf("delete %d: %+v %v", attempt, result, err)
				}
				for _, path := range []string{originalDisk, nvram, filepath.Join(tpm, "tpm-state"), oldSet} {
					_, err := os.Stat(path)
					if disposition == domain.VMDataDelete {
						if !os.IsNotExist(err) {
							t.Fatalf("explicit delete retained %s: %v", path, err)
						}
					} else if err != nil {
						t.Fatalf("guest data implicitly deleted: %s: %v", path, err)
					}
				}
				if disposition == domain.VMDataDelete && len(result.RetainedStorageRefs) != 0 {
					t.Fatal("deleted storage still reported retained")
				}
				if disposition != domain.VMDataDelete && len(result.RetainedStorageRefs) == 0 {
					t.Fatal("retained guest data reported released")
				}
				if disposition == domain.VMDataExport {
					if result.Diagnostic.EvidenceDigest == "" {
						t.Fatal("export evidence missing")
					}
					if _, err = p.verifyDeletionExport(context.Background(), q, result.Diagnostic.EvidenceDigest); err != nil {
						t.Fatal(err)
					}
				}
				p, err = NewPersistentProvider(p.cfg, f)
				if err != nil {
					t.Fatal(err)
				}
			}
			if disposition == domain.VMDataDelete {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				foreign := filepath.Join(dir, "new-foreign-data")
				if err := os.WriteFile(foreign, []byte("retain"), 0600); err != nil {
					t.Fatal(err)
				}
				if result, err := p.Execute(context.Background(), q); err == nil || result.Confirmed {
					t.Fatal("completed journal authorized recreated directory cleanup")
				}
				if _, err := os.Stat(foreign); err != nil {
					t.Fatal("recreated foreign directory removed")
				}
			}
			q.Operation.RequestHash = DigestBytes([]byte("other request"))
			if result, err := p.Execute(context.Background(), q); err == nil || result.Confirmed {
				t.Fatal("delete replay accepted changed admission")
			}
		})
	}
}

func TestDeletionRequiresExplicitDestructiveApprovalAndExactAbsentRecord(t *testing.T) {
	for _, condition := range []string{"unapproved", "invalid", "foreign-absent", "export-failure"} {
		t.Run(condition, func(t *testing.T) {
			p, f, q := checkpointRequest(t)
			disk := f.resource.Components[domain.VMComponentDisk]
			q.Operation.Kind = domain.VMOperationDelete
			q.Operation.ID = uuid.New()
			q.Operation.DeleteTarget = domain.VMDeleteDeployment
			q.Operation.CheckpointID = nil
			q.Checkpoint = nil
			q.Operation.DataDisposition = domain.VMDataDelete
			switch condition {
			case "invalid":
				q.Operation.DataDisposition = "erase"
			case "foreign-absent":
				rec, err := p.readRecord(f.resource.ID)
				if err != nil {
					t.Fatal(err)
				}
				rec.Marker.DeploymentID = uuid.New()
				if err := writeJSON(context.Background(), p.recordPath(f.resource.ID), rec); err != nil {
					t.Fatal(err)
				}
				f.resource.State = domain.VMRuntimeAbsent
				q.Operation.DataDisposition = domain.VMDataRetain
			case "export-failure":
				q.Operation.DataDisposition = domain.VMDataExport
				f.failCopy = domain.VMComponentDisk
			}
			before := f.mutations
			result, err := p.Execute(context.Background(), q)
			if err == nil || result.Confirmed || f.mutations != before {
				t.Fatal("unsafe delete reached provider")
			}
			if _, err := os.Stat(disk); err != nil {
				t.Fatal("refused deletion erased guest data")
			}
		})
	}
}
