package controlplane

import (
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	KindWorkerStatus              = kinds.WorkerStatus
	KindWorkerResult              = kinds.WorkerResult
	KindArtifactRegister          = kinds.ArtifactRegister
	KindPackageStatus             = kinds.PackageStatus
	KindAdoptionScanResult        = kinds.AdoptionScanResult
	KindAdoptionImportResult      = kinds.AdoptionImportResult
	KindPackageResult             = kinds.PackageResult
	KindPackageDriftEvent         = kinds.PackageDriftEvent
	KindPackageRepositoryRegistry = nostrpool.KindPackageRepositoryRegistry
	KindPackageArtifactRegistry   = nostrpool.KindPackageArtifactRegistry
	KindPackagePromotionRegistry  = nostrpool.KindPackagePromotionRegistry
)
