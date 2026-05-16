package service

#NonEmptyString: string & !=""

#Action: "fetch_source" | "validate_provenance" | "convert_model" | "quantize_model" | "package_artifact" | "publish_artifact" | "deploy_endpoint" | "run_smoke_eval" | "promote" | "rollback"

#Runtime: "external_api" | "vllm" | "ollama" | "llama_cpp" | "onnxruntime" | "rknn_server" | "triton" | "tensorrt_llm" | "torchserve" | "mlserver" | "tensorflow_serving" | "custom_container"

#ArtifactKind: "model" | "adapter" | "dataset" | "tokenizer" | "preprocessor" | "postprocessor" | "container" | "evaluation_report"

#ArtifactFormat: "huggingface_snapshot" | "safetensors" | "gguf" | "onnx" | "rknn" | "oci_image" | "oci_artifact" | "blossom_blob" | "tensorrt_engine" | "openvino_ir" | "tflite"

#ValueType: "string" | "integer" | "number" | "boolean" | "object" | "array" | "artifact_ref" | "endpoint" | "image"

#ArtifactRef: {
	uri!: #NonEmptyString
	kind?: #ArtifactKind
	format!: #ArtifactFormat
	sha256?: =~"^[a-fA-F0-9]{64}$"
	size_bytes?: int & >=0
	media_type?: #NonEmptyString
	...
}

#RetryPolicy: {
	max_attempts?: int & >=0 & <=10
	backoff?: "none" | "fixed" | "exponential"
	initial_delay_ms?: int & >0
	max_delay_ms?: int & >0
	...
}

#RuntimeRequirements: {
	runtimes?: [...#Runtime]
	artifact_formats?: [...#ArtifactFormat]
	accelerators?: [...#NonEmptyString]
	toolchains?: [...#NonEmptyString]
	min_vram_gb?: int & >=0
	min_ram_gb?: int & >=0
	...
}

#StepBase: {
	name?: #NonEmptyString
	action!: #Action
	inputs?: [string]: #ValueType
	outputs?: [string]: #ValueType
	artifacts?: [...#ArtifactRef]
	runtime?: #Runtime
	retry_policy?: #RetryPolicy
	requires?: #RuntimeRequirements
	target?: {...}
	targets?: [...#NonEmptyString]
	...
}

#FetchSourceStep: #StepBase & {action: "fetch_source", outputs: {source: "artifact_ref", ...}}
#ValidateProvenanceStep: #StepBase & {action: "validate_provenance", inputs: {artifact: "artifact_ref", ...}, outputs: {provenance: "object", ...}}
#ConvertModelStep: #StepBase & {action: "convert_model", inputs: {model: "artifact_ref", ...}, outputs: {model: "artifact_ref", ...}, requires: {toolchains: [...#NonEmptyString], ...}}
#QuantizeModelStep: #StepBase & {action: "quantize_model", inputs: {model: "artifact_ref", ...}, outputs: {model: "artifact_ref", ...}}
#PackageArtifactStep: #StepBase & {action: "package_artifact", inputs: {artifact: "artifact_ref", ...}, outputs: {artifact: "artifact_ref", ...}}
#PublishArtifactStep: #StepBase & {action: "publish_artifact", inputs: {artifact: "artifact_ref", ...}, outputs: {artifact: "artifact_ref", ...}, targets: [...#NonEmptyString]}
#DeployEndpointStep: #StepBase & {action: "deploy_endpoint", inputs: {artifact: "artifact_ref", ...}, outputs: {endpoint: "endpoint", ...}, runtime: #Runtime, requires: {runtimes: [...#Runtime], artifact_formats: [...#ArtifactFormat], accelerators: [...#NonEmptyString], ...}, target: {accelerator: #NonEmptyString, ...}}
#RunSmokeEvalStep: #StepBase & {action: "run_smoke_eval", inputs: {endpoint: "endpoint", ...}, outputs: {report: "object", ...}}
#PromoteStep: #StepBase & {action: "promote", inputs: {endpoint: "endpoint", ...}}
#RollbackStep: #StepBase & {action: "rollback", inputs: {endpoint: "endpoint", ...}}

#Step: #FetchSourceStep | #ValidateProvenanceStep | #ConvertModelStep | #QuantizeModelStep | #PackageArtifactStep | #PublishArtifactStep | #DeployEndpointStep | #RunSmokeEvalStep | #PromoteStep | #RollbackStep

#Recipe: {
	name!: #NonEmptyString
	version!: int & >0 | #NonEmptyString
	description?: string
	inputs!: [string]: {...}
	steps!: [...#Step] & [_, ...]
	outputs!: [string]: {...}
	retry_policy?: #RetryPolicy
	runtime_requirements?: #RuntimeRequirements
	...
}
