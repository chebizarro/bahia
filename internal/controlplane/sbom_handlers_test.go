package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	sbomadapter "github.com/openagentsinc/bahia/internal/adapters/sbom"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

func TestSBOMContextVMGenerateReturnsAcceptedAckAndEnqueues(t *testing.T) {
	ack, err := service.NewSBOMAcceptedAck("run generate/1")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeSBOMRequestRunner{generateAck: ack}
	h := sbomContextVMHandler{runner: runner}
	params, err := json.Marshal(service.SBOMGenerateRequest{
		IDempotencyKey: "run generate/1",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectRepository},
		SubjectLocator: domain.SBOMSubjectLocator{Repository: &domain.SBOMRepositoryLocator{RepositoryURL: "https://git.example/acme/api.git", Commit: "0123456789abcdef0123456789abcdef01234567"}},
		Source:         sbomadapter.SourceRequest{Kind: sbomadapter.SourceKindDirectory, Locator: "/workspace/api"},
		Formats:        []domain.SBOMFormat{domain.SBOMFormatSPDX},
		Generator:      sbomadapter.GeneratorSyft,
		Storage:        domain.SBOMStorageBlossom,
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := h.generate(context.Background(), ContextVMRequest{RPC: ContextVMJSONRPCRequest{Params: params}})
	if err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	got, ok := result.(service.SBOMAcceptedAck)
	if !ok {
		t.Fatalf("generate() result type = %T, want service.SBOMAcceptedAck", result)
	}
	if !got.Accepted || got.Status != "accepted" || got.StatusDTag != "sbom:run:run-generate-1" || got.IDempotencyKey != "run generate/1" {
		t.Fatalf("unexpected ack: %#v", got)
	}
	if runner.generateCalls != 1 || runner.importCalls != 0 {
		t.Fatalf("runner calls generate=%d import=%d", runner.generateCalls, runner.importCalls)
	}
	if runner.generateReq.SubjectLocator.Repository == nil || runner.generateReq.SubjectLocator.Repository.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("subjectLocator was not forwarded: %#v", runner.generateReq.SubjectLocator)
	}
}

func TestSBOMContextVMImportReturnsAcceptedAckAndEnqueues(t *testing.T) {
	ack, err := service.NewSBOMAcceptedAck("import-package")
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeSBOMRequestRunner{importAck: ack}
	h := sbomContextVMHandler{runner: runner}
	payload := []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","name":"pkg"}`)
	params, err := json.Marshal(sbomImportParams{
		IDempotencyKey: "import-package",
		Subject:        domain.SBOMSubject{Type: domain.SBOMSubjectPackage},
		SubjectLocator: domain.SBOMSubjectLocator{Package: &domain.SBOMPackageArtifactLocator{RepositoryID: "11111111-1111-1111-1111-111111111111", Namespace: "@acme", PackageName: "utils", Version: "1.2.3", Filename: "utils.tgz", SHA256: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Format:         domain.SBOMFormatSPDX,
		PayloadBase64:  base64.StdEncoding.EncodeToString(payload),
		Storage:        domain.SBOMStorageBlossom,
		Generator:      domain.SBOMGenerator{ID: "import"},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := h.importSBOM(context.Background(), ContextVMRequest{RPC: ContextVMJSONRPCRequest{Params: params}})
	if err != nil {
		t.Fatalf("importSBOM() error = %v", err)
	}
	got, ok := result.(service.SBOMAcceptedAck)
	if !ok {
		t.Fatalf("importSBOM() result type = %T, want service.SBOMAcceptedAck", result)
	}
	if !got.Accepted || got.StatusDTag != "sbom:run:import-package" {
		t.Fatalf("unexpected ack: %#v", got)
	}
	if runner.importCalls != 1 || runner.generateCalls != 0 {
		t.Fatalf("runner calls generate=%d import=%d", runner.generateCalls, runner.importCalls)
	}
	if string(runner.importReq.Payload) != string(payload) {
		t.Fatalf("payload = %q, want %q", string(runner.importReq.Payload), string(payload))
	}
	if runner.importReq.SubjectLocator.Package == nil || runner.importReq.SubjectLocator.Package.PackageName != "utils" {
		t.Fatalf("subjectLocator was not forwarded: %#v", runner.importReq.SubjectLocator)
	}
}

type fakeSBOMRequestRunner struct {
	generateAck   service.SBOMAcceptedAck
	importAck     service.SBOMAcceptedAck
	generateCalls int
	importCalls   int
	generateReq   service.SBOMGenerateRequest
	importReq     service.SBOMImportRequest
}

func (r *fakeSBOMRequestRunner) EnqueueGenerate(_ context.Context, req service.SBOMGenerateRequest) (service.SBOMAcceptedAck, error) {
	r.generateCalls++
	r.generateReq = req
	return r.generateAck, nil
}

func (r *fakeSBOMRequestRunner) EnqueueImport(_ context.Context, req service.SBOMImportRequest) (service.SBOMAcceptedAck, error) {
	r.importCalls++
	r.importReq = req
	return r.importAck, nil
}
