package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MLTaskKind identifies a generic inference/training task family.
type MLTaskKind string

const (
	MLTaskKindChatCompletions MLTaskKind = "chat_completions"
	MLTaskKindEmbeddings      MLTaskKind = "embeddings"
	MLTaskKindReranking       MLTaskKind = "reranking"
	MLTaskKindImageGeneration MLTaskKind = "image_generation"
	MLTaskKindVisionInference MLTaskKind = "vision_inference"
	MLTaskKindSpeechToText    MLTaskKind = "speech_to_text"
	MLTaskKindTextToSpeech    MLTaskKind = "text_to_speech"
	MLTaskKindONNXInference   MLTaskKind = "onnx_inference"
)

// MLArtifactKind identifies what role an artifact plays in a model lifecycle.
type MLArtifactKind string

const (
	MLArtifactKindModel            MLArtifactKind = "model"
	MLArtifactKindAdapter          MLArtifactKind = "adapter"
	MLArtifactKindDataset          MLArtifactKind = "dataset"
	MLArtifactKindTokenizer        MLArtifactKind = "tokenizer"
	MLArtifactKindPreprocessor     MLArtifactKind = "preprocessor"
	MLArtifactKindPostprocessor    MLArtifactKind = "postprocessor"
	MLArtifactKindContainer        MLArtifactKind = "container"
	MLArtifactKindEvaluationReport MLArtifactKind = "evaluation_report"
)

// MLArtifactFormat identifies the concrete storage/package format for an artifact.
type MLArtifactFormat string

const (
	MLArtifactFormatHuggingFaceSnapshot MLArtifactFormat = "huggingface_snapshot"
	MLArtifactFormatSafeTensors         MLArtifactFormat = "safetensors"
	MLArtifactFormatGGUF                MLArtifactFormat = "gguf"
	MLArtifactFormatONNX                MLArtifactFormat = "onnx"
	MLArtifactFormatRKNN                MLArtifactFormat = "rknn"
	MLArtifactFormatOCIImage            MLArtifactFormat = "oci_image"
	MLArtifactFormatOCIArtifact         MLArtifactFormat = "oci_artifact"
	MLArtifactFormatBlossomBlob         MLArtifactFormat = "blossom_blob"
	MLArtifactFormatTensorRTEngine      MLArtifactFormat = "tensorrt_engine"
	MLArtifactFormatOpenVINOIR          MLArtifactFormat = "openvino_ir"
	MLArtifactFormatTFLite              MLArtifactFormat = "tflite"
)

// MLRuntimeKind identifies a runtime family that can serve or process ML artifacts.
type MLRuntimeKind string

const (
	MLRuntimeKindExternalAPI       MLRuntimeKind = "external_api"
	MLRuntimeKindVLLM              MLRuntimeKind = "vllm"
	MLRuntimeKindOllama            MLRuntimeKind = "ollama"
	MLRuntimeKindLlamaCPP          MLRuntimeKind = "llama_cpp"
	MLRuntimeKindONNXRuntime       MLRuntimeKind = "onnxruntime"
	MLRuntimeKindRKNNServer        MLRuntimeKind = "rknn_server"
	MLRuntimeKindTriton            MLRuntimeKind = "triton"
	MLRuntimeKindTensorRTLLM       MLRuntimeKind = "tensorrt_llm"
	MLRuntimeKindTorchServe        MLRuntimeKind = "torchserve"
	MLRuntimeKindMLServer          MLRuntimeKind = "mlserver"
	MLRuntimeKindTensorflowServing MLRuntimeKind = "tensorflow_serving"
	MLRuntimeKindCustomContainer   MLRuntimeKind = "custom_container"
)

// MLSourceRef describes where a model, version, dataset, or artifact came from.
type MLSourceRef struct {
	Kind     string         `json:"kind"`
	URI      string         `json:"uri"`
	Revision string         `json:"revision,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// MLRuntimeRequirements captures coarse compatibility requirements used by Bucket-B registry/backfill.
type MLRuntimeRequirements struct {
	PreferredRuntimes []MLRuntimeKind    `json:"preferred_runtimes,omitempty"`
	RequiredFormats   []MLArtifactFormat `json:"required_formats,omitempty"`
	Accelerators      []string           `json:"accelerators,omitempty"`
	MinVRAMGB         int                `json:"min_vram_gb,omitempty"`
	MinSystemMemoryGB int                `json:"min_system_memory_gb,omitempty"`
	Toolchains        []string           `json:"toolchains,omitempty"`
	Metadata          map[string]any     `json:"metadata,omitempty"`
}

// MLRecipeStep is one ordered linear recipe step. Bucket C owns execution semantics.
type MLRecipeStep struct {
	Name        string         `json:"name,omitempty"`
	Action      string         `json:"action"`
	Inputs      map[string]any `json:"inputs,omitempty"`
	Outputs     map[string]any `json:"outputs,omitempty"`
	Runtime     MLRuntimeKind  `json:"runtime,omitempty"`
	RetryPolicy map[string]any `json:"retry_policy,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

// MLRecipeRunStepState is a checkpoint for one linear recipe step.
type MLRecipeRunStepState struct {
	Index           int            `json:"index"`
	Name            string         `json:"name,omitempty"`
	Action          string         `json:"action"`
	Status          string         `json:"status"`
	InputDigestSet  []string       `json:"input_digest_set,omitempty"`
	InputArtifacts  []uuid.UUID    `json:"input_artifacts,omitempty"`
	OutputArtifacts []uuid.UUID    `json:"output_artifacts,omitempty"`
	StartedAt       *time.Time     `json:"started_at,omitempty"`
	FinishedAt      *time.Time     `json:"finished_at,omitempty"`
	Error           string         `json:"error,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
}

// MLModel is the generic model/catalog entry.
type MLModel struct {
	ID           uuid.UUID      `json:"id"`
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Family       string         `json:"family,omitempty"`
	Summary      string         `json:"summary,omitempty"`
	Description  string         `json:"description,omitempty"`
	Modalities   []string       `json:"modalities,omitempty"`
	TaskKinds    []MLTaskKind   `json:"task_kinds,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	License      string         `json:"license,omitempty"`
	Source       *MLSourceRef   `json:"source,omitempty"`
	Card         map[string]any `json:"card,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

// MLModelVersion is an immutable deployable model revision.
type MLModelVersion struct {
	ID                  uuid.UUID             `json:"id"`
	ModelID             uuid.UUID             `json:"model_id"`
	Version             string                `json:"version"`
	Source              MLSourceRef           `json:"source"`
	RuntimeRequirements MLRuntimeRequirements `json:"runtime_requirements,omitempty"`
	Aliases             []string              `json:"aliases,omitempty"`
	ArtifactIDs         []uuid.UUID           `json:"artifact_ids,omitempty"`
	Metadata            map[string]any        `json:"metadata,omitempty"`
	CreatedAt           time.Time             `json:"created_at"`
}

// MLArtifactRef is a digest-addressed artifact reference.
type MLArtifactRef struct {
	ID             uuid.UUID        `json:"id"`
	ModelVersionID *uuid.UUID       `json:"model_version_id,omitempty"`
	Kind           MLArtifactKind   `json:"kind"`
	Format         MLArtifactFormat `json:"format"`
	URI            string           `json:"uri"`
	SHA256         string           `json:"sha256,omitempty"`
	SizeBytes      int64            `json:"size_bytes,omitempty"`
	MediaType      string           `json:"media_type,omitempty"`
	Source         *MLSourceRef     `json:"source,omitempty"`
	Metadata       map[string]any   `json:"metadata,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
}

// MLProvenanceEdge describes artifact/model lineage. Bucket C owns fail-closed evaluation.
type MLProvenanceEdge struct {
	ID             uuid.UUID      `json:"id"`
	FromArtifactID *uuid.UUID     `json:"from_artifact_id,omitempty"`
	ToArtifactID   *uuid.UUID     `json:"to_artifact_id,omitempty"`
	ModelVersionID *uuid.UUID     `json:"model_version_id,omitempty"`
	EdgeKind       string         `json:"edge_kind"`
	Evidence       map[string]any `json:"evidence,omitempty"`
	Verified       bool           `json:"verified"`
	Defect         string         `json:"defect,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

// MLRecipe is a first-class, ordered linear workflow definition.
type MLRecipe struct {
	ID             uuid.UUID      `json:"id"`
	Name           string         `json:"name"`
	Version        string         `json:"version"`
	Description    string         `json:"description,omitempty"`
	YAML           string         `json:"yaml,omitempty"`
	NormalizedJSON map[string]any `json:"normalized_json,omitempty"`
	Inputs         map[string]any `json:"inputs,omitempty"`
	Steps          []MLRecipeStep `json:"steps,omitempty"`
	Outputs        map[string]any `json:"outputs,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// MLRecipeRun is a checkpointed recipe execution read model. Bucket C executes it.
type MLRecipeRun struct {
	ID          uuid.UUID              `json:"id"`
	RecipeID    uuid.UUID              `json:"recipe_id"`
	RequestedBy string                 `json:"requested_by"`
	Status      DeploymentRunStatus    `json:"status"`
	Inputs      map[string]any         `json:"inputs,omitempty"`
	Parameters  map[string]any         `json:"parameters,omitempty"`
	StepStates  []MLRecipeRunStepState `json:"step_states,omitempty"`
	Result      map[string]any         `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Metadata    map[string]any         `json:"metadata,omitempty"`
	StartedAt   *time.Time             `json:"started_at,omitempty"`
	FinishedAt  *time.Time             `json:"finished_at,omitempty"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

// MLInferenceEndpoint is a generic endpoint registry entry.
type MLInferenceEndpoint struct {
	ID              uuid.UUID      `json:"id"`
	Name            string         `json:"name"`
	EnvironmentID   uuid.UUID      `json:"environment_id"`
	TaskKinds       []MLTaskKind   `json:"task_kinds,omitempty"`
	Protocol        string         `json:"protocol,omitempty"`
	Gateway         map[string]any `json:"gateway,omitempty"`
	PlacementPolicy map[string]any `json:"placement_policy,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// MLDeploymentIntent is desired deployment state for one endpoint/environment.
type MLDeploymentIntent struct {
	ID                 uuid.UUID              `json:"id"`
	EndpointID         uuid.UUID              `json:"endpoint_id"`
	EnvironmentID      uuid.UUID              `json:"environment_id"`
	ModelVersionID     uuid.UUID              `json:"model_version_id"`
	RequestedBy        string                 `json:"requested_by"`
	SourceKind         SourceKind             `json:"source_kind"`
	ApprovalStatus     ApprovalStatus         `json:"approval_status"`
	Status             DeploymentIntentStatus `json:"status"`
	RuntimePreference  MLRuntimeKind          `json:"runtime_preference,omitempty"`
	SupersedesIntentID *uuid.UUID             `json:"supersedes_intent_id,omitempty"`
	ApprovalMetadata   map[string]any         `json:"approval_metadata,omitempty"`
	Metadata           map[string]any         `json:"metadata,omitempty"`
	CreatedAt          time.Time              `json:"created_at"`
	ApprovedAt         *time.Time             `json:"approved_at,omitempty"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

// MLDeploymentRun is one provisioning attempt for an ML deployment intent.
type MLDeploymentRun struct {
	ID                 uuid.UUID           `json:"id"`
	DeploymentIntentID uuid.UUID           `json:"deployment_intent_id"`
	RuntimeKind        MLRuntimeKind       `json:"runtime_kind,omitempty"`
	EndpointRef        string              `json:"endpoint_ref,omitempty"`
	WorkerPubkey       string              `json:"worker_pubkey,omitempty"`
	WorkerName         string              `json:"worker_name,omitempty"`
	BackendEndpoint    string              `json:"backend_endpoint,omitempty"`
	Status             DeploymentRunStatus `json:"status"`
	ExitCode           *int                `json:"exit_code,omitempty"`
	StdoutRef          string              `json:"stdout_ref,omitempty"`
	StderrRef          string              `json:"stderr_ref,omitempty"`
	VerifiedDigests    map[string]string   `json:"verified_digests,omitempty"`
	Metadata           map[string]any      `json:"metadata,omitempty"`
	StartedAt          *time.Time          `json:"started_at,omitempty"`
	FinishedAt         *time.Time          `json:"finished_at,omitempty"`
	CreatedAt          time.Time           `json:"created_at"`
	UpdatedAt          time.Time           `json:"updated_at"`
}

// MLInferenceObservation captures observed backend/gateway state for an endpoint.
type MLInferenceObservation struct {
	ID                     uuid.UUID          `json:"id"`
	EndpointID             uuid.UUID          `json:"endpoint_id"`
	EnvironmentID          uuid.UUID          `json:"environment_id"`
	ObservedModelVersionID *uuid.UUID         `json:"observed_model_version_id,omitempty"`
	ObservedRunID          *uuid.UUID         `json:"observed_run_id,omitempty"`
	RuntimeKind            MLRuntimeKind      `json:"runtime_kind,omitempty"`
	BackendEndpoint        string             `json:"backend_endpoint,omitempty"`
	BackendHealth          HealthStatus       `json:"backend_health"`
	GatewayStatus          GatewayRouteStatus `json:"gateway_status"`
	GatewayTarget          string             `json:"gateway_target,omitempty"`
	GatewayConfigHash      string             `json:"gateway_config_hash,omitempty"`
	Source                 string             `json:"source"`
	Metadata               map[string]any     `json:"metadata,omitempty"`
	ObservedAt             time.Time          `json:"observed_at"`
}

// MLInferenceState is desired-vs-observed state for an endpoint/environment.
type MLInferenceState struct {
	EndpointID            uuid.UUID          `json:"endpoint_id"`
	EnvironmentID         uuid.UUID          `json:"environment_id"`
	DesiredModelVersionID *uuid.UUID         `json:"desired_model_version_id,omitempty"`
	DesiredIntentID       *uuid.UUID         `json:"desired_intent_id,omitempty"`
	ActiveRunID           *uuid.UUID         `json:"active_run_id,omitempty"`
	CurrentObservationID  *uuid.UUID         `json:"current_observation_id,omitempty"`
	DriftStatus           DriftStatus        `json:"drift_status"`
	GatewayStatus         GatewayRouteStatus `json:"gateway_status"`
	RuntimeKind           MLRuntimeKind      `json:"runtime_kind,omitempty"`
	BackendEndpoint       string             `json:"backend_endpoint,omitempty"`
	BackendHealth         HealthStatus       `json:"backend_health"`
	GatewayTarget         string             `json:"gateway_target,omitempty"`
	LastReconciledAt      *time.Time         `json:"last_reconciled_at,omitempty"`
	UpdatedAt             time.Time          `json:"updated_at"`
}

// MLEvaluationSpec describes a repeatable evaluation contract.
type MLEvaluationSpec struct {
	ID         uuid.UUID      `json:"id"`
	Name       string         `json:"name"`
	Version    string         `json:"version"`
	TaskKinds  []MLTaskKind   `json:"task_kinds,omitempty"`
	DatasetRef string         `json:"dataset_ref,omitempty"`
	Metrics    []string       `json:"metrics,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

// MLEvaluationRun is an evaluation/experiment read model.
type MLEvaluationRun struct {
	ID             uuid.UUID           `json:"id"`
	SpecID         uuid.UUID           `json:"spec_id"`
	ModelVersionID uuid.UUID           `json:"model_version_id"`
	EndpointID     *uuid.UUID          `json:"endpoint_id,omitempty"`
	Status         DeploymentRunStatus `json:"status"`
	Metrics        map[string]float64  `json:"metrics,omitempty"`
	Artifacts      []uuid.UUID         `json:"artifacts,omitempty"`
	Metadata       map[string]any      `json:"metadata,omitempty"`
	StartedAt      *time.Time          `json:"started_at,omitempty"`
	FinishedAt     *time.Time          `json:"finished_at,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

func (k MLTaskKind) IsValid() bool {
	switch k {
	case MLTaskKindChatCompletions, MLTaskKindEmbeddings, MLTaskKindReranking, MLTaskKindImageGeneration, MLTaskKindVisionInference, MLTaskKindSpeechToText, MLTaskKindTextToSpeech, MLTaskKindONNXInference:
		return true
	default:
		return false
	}
}

func (k MLArtifactKind) IsValid() bool {
	switch k {
	case MLArtifactKindModel, MLArtifactKindAdapter, MLArtifactKindDataset, MLArtifactKindTokenizer, MLArtifactKindPreprocessor, MLArtifactKindPostprocessor, MLArtifactKindContainer, MLArtifactKindEvaluationReport:
		return true
	default:
		return false
	}
}

func (f MLArtifactFormat) IsValid() bool {
	switch f {
	case MLArtifactFormatHuggingFaceSnapshot, MLArtifactFormatSafeTensors, MLArtifactFormatGGUF, MLArtifactFormatONNX, MLArtifactFormatRKNN, MLArtifactFormatOCIImage, MLArtifactFormatOCIArtifact, MLArtifactFormatBlossomBlob, MLArtifactFormatTensorRTEngine, MLArtifactFormatOpenVINOIR, MLArtifactFormatTFLite:
		return true
	default:
		return false
	}
}

func (k MLRuntimeKind) IsValid() bool {
	switch k {
	case MLRuntimeKindExternalAPI, MLRuntimeKindVLLM, MLRuntimeKindOllama, MLRuntimeKindLlamaCPP, MLRuntimeKindONNXRuntime, MLRuntimeKindRKNNServer, MLRuntimeKindTriton, MLRuntimeKindTensorRTLLM, MLRuntimeKindTorchServe, MLRuntimeKindMLServer, MLRuntimeKindTensorflowServing, MLRuntimeKindCustomContainer:
		return true
	default:
		return false
	}
}

func ValidateMLModel(model *MLModel) error {
	if model == nil {
		return fmt.Errorf("%w: ML model must not be nil", ErrInvalidValue)
	}
	if err := ValidateRequiredString(model.Slug, "slug"); err != nil {
		return err
	}
	if err := ValidateRequiredString(model.Name, "name"); err != nil {
		return err
	}
	for _, task := range model.TaskKinds {
		if !task.IsValid() {
			return fmt.Errorf("%w: ML task kind %q is not valid", ErrInvalidValue, task)
		}
	}
	return nil
}

func ValidateMLModelVersion(version *MLModelVersion) error {
	if version == nil {
		return fmt.Errorf("%w: ML model version must not be nil", ErrInvalidValue)
	}
	if err := ValidateRequiredUUID(version.ModelID, "model_id"); err != nil {
		return err
	}
	if err := ValidateRequiredString(version.Version, "version"); err != nil {
		return err
	}
	if strings.TrimSpace(version.Source.URI) == "" {
		return fmt.Errorf("%w: source.uri must not be empty", ErrEmptyField)
	}
	for _, runtime := range version.RuntimeRequirements.PreferredRuntimes {
		if !runtime.IsValid() {
			return fmt.Errorf("%w: ML runtime kind %q is not valid", ErrInvalidValue, runtime)
		}
	}
	for _, format := range version.RuntimeRequirements.RequiredFormats {
		if !format.IsValid() {
			return fmt.Errorf("%w: ML artifact format %q is not valid", ErrInvalidValue, format)
		}
	}
	return nil
}

func ValidateMLArtifactRef(artifact *MLArtifactRef) error {
	if artifact == nil {
		return fmt.Errorf("%w: ML artifact ref must not be nil", ErrInvalidValue)
	}
	if !artifact.Kind.IsValid() {
		return fmt.Errorf("%w: ML artifact kind %q is not valid", ErrInvalidValue, artifact.Kind)
	}
	if !artifact.Format.IsValid() {
		return fmt.Errorf("%w: ML artifact format %q is not valid", ErrInvalidValue, artifact.Format)
	}
	if err := ValidateRequiredString(artifact.URI, "uri"); err != nil {
		return err
	}
	return nil
}
