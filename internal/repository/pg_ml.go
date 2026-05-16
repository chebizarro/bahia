package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/openagentsinc/bahia/internal/domain"
)

// PgMLRegistryRepository is a PostgreSQL implementation of MLRegistryRepository.
type PgMLRegistryRepository struct {
	pool pgQueryer
}

func NewPgMLRegistryRepository(pool *pgxpool.Pool) *PgMLRegistryRepository {
	return newPgMLRegistryRepositoryWithDB(pool)
}

func newPgMLRegistryRepositoryWithDB(db pgQueryer) *PgMLRegistryRepository {
	return &PgMLRegistryRepository{pool: db}
}

const mlModelColumns = `id, slug, name, family, summary, description, modalities, task_kinds, capabilities, license, source, card, metadata, created_at, updated_at`

func (r *PgMLRegistryRepository) UpsertModel(ctx context.Context, model *domain.MLModel) error {
	if model.ID == uuid.Nil {
		model.ID = uuid.New()
	}
	now := time.Now().UTC()
	if model.CreatedAt.IsZero() {
		model.CreatedAt = now
	}
	model.UpdatedAt = now
	modalities, err := marshalJSON(model.Modalities, "ML model modalities")
	if err != nil {
		return err
	}
	tasks, err := marshalJSON(model.TaskKinds, "ML model task kinds")
	if err != nil {
		return err
	}
	capabilities, err := marshalJSON(model.Capabilities, "ML model capabilities")
	if err != nil {
		return err
	}
	source, err := marshalJSON(model.Source, "ML model source")
	if err != nil {
		return err
	}
	card, err := marshalJSON(model.Card, "ML model card")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(model.Metadata, "ML model metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO ml_models (`+mlModelColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET
			slug = EXCLUDED.slug, name = EXCLUDED.name, family = EXCLUDED.family,
			summary = EXCLUDED.summary, description = EXCLUDED.description,
			modalities = EXCLUDED.modalities, task_kinds = EXCLUDED.task_kinds,
			capabilities = EXCLUDED.capabilities, license = EXCLUDED.license,
			source = EXCLUDED.source, card = EXCLUDED.card, metadata = EXCLUDED.metadata,
			updated_at = EXCLUDED.updated_at
	`, model.ID, model.Slug, model.Name, model.Family, model.Summary, model.Description, modalities, tasks, capabilities, model.License, source, card, metadata, model.CreatedAt, model.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML model: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanModel(row pgx.Row) (*domain.MLModel, error) {
	model := &domain.MLModel{}
	var modalitiesJSON, tasksJSON, capabilitiesJSON, sourceJSON, cardJSON, metadataJSON []byte
	if err := row.Scan(&model.ID, &model.Slug, &model.Name, &model.Family, &model.Summary, &model.Description, &modalitiesJSON, &tasksJSON, &capabilitiesJSON, &model.License, &sourceJSON, &cardJSON, &metadataJSON, &model.CreatedAt, &model.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(modalitiesJSON, &model.Modalities, "ML model modalities"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(tasksJSON, &model.TaskKinds, "ML model task kinds"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(capabilitiesJSON, &model.Capabilities, "ML model capabilities"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(sourceJSON, &model.Source, "ML model source"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(cardJSON, &model.Card, "ML model card"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &model.Metadata, "ML model metadata"); err != nil {
		return nil, err
	}
	return model, nil
}

func (r *PgMLRegistryRepository) GetModel(ctx context.Context, id uuid.UUID) (*domain.MLModel, error) {
	model, err := r.scanModel(r.pool.QueryRow(ctx, `SELECT `+mlModelColumns+` FROM ml_models WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML model: %w", err)
	}
	return model, nil
}

func (r *PgMLRegistryRepository) GetModelBySlug(ctx context.Context, slug string) (*domain.MLModel, error) {
	model, err := r.scanModel(r.pool.QueryRow(ctx, `SELECT `+mlModelColumns+` FROM ml_models WHERE slug = $1`, slug))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML model by slug: %w", err)
	}
	return model, nil
}

func (r *PgMLRegistryRepository) ListModels(ctx context.Context, taskKind domain.MLTaskKind, limit, offset int) ([]domain.MLModel, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + mlModelColumns + ` FROM ml_models ORDER BY slug ASC LIMIT $1 OFFSET $2`
	args := []any{limit, offset}
	if taskKind != "" {
		query = `SELECT ` + mlModelColumns + ` FROM ml_models WHERE task_kinds ? $1 ORDER BY slug ASC LIMIT $2 OFFSET $3`
		args = []any{string(taskKind), limit, offset}
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing ML models: %w", err)
	}
	defer rows.Close()
	var models []domain.MLModel
	for rows.Next() {
		model, err := r.scanModel(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML model: %w", err)
		}
		models = append(models, *model)
	}
	return models, rows.Err()
}

const mlModelVersionColumns = `id, model_id, version, source, runtime_requirements, aliases, artifact_ids, metadata, created_at`

func (r *PgMLRegistryRepository) UpsertModelVersion(ctx context.Context, version *domain.MLModelVersion) error {
	if version.ID == uuid.Nil {
		version.ID = uuid.New()
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = time.Now().UTC()
	}
	source, err := marshalJSON(version.Source, "ML model version source")
	if err != nil {
		return err
	}
	req, err := marshalJSON(version.RuntimeRequirements, "ML model version runtime requirements")
	if err != nil {
		return err
	}
	aliases, err := marshalJSON(version.Aliases, "ML model version aliases")
	if err != nil {
		return err
	}
	artifacts, err := marshalJSON(version.ArtifactIDs, "ML model version artifact ids")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(version.Metadata, "ML model version metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO ml_model_versions (`+mlModelVersionColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			model_id = EXCLUDED.model_id, version = EXCLUDED.version, source = EXCLUDED.source,
			runtime_requirements = EXCLUDED.runtime_requirements, aliases = EXCLUDED.aliases,
			artifact_ids = EXCLUDED.artifact_ids, metadata = EXCLUDED.metadata
	`, version.ID, version.ModelID, version.Version, source, req, aliases, artifacts, metadata, version.CreatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML model version: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanModelVersion(row pgx.Row) (*domain.MLModelVersion, error) {
	version := &domain.MLModelVersion{}
	var sourceJSON, reqJSON, aliasesJSON, artifactsJSON, metadataJSON []byte
	if err := row.Scan(&version.ID, &version.ModelID, &version.Version, &sourceJSON, &reqJSON, &aliasesJSON, &artifactsJSON, &metadataJSON, &version.CreatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(sourceJSON, &version.Source, "ML model version source"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(reqJSON, &version.RuntimeRequirements, "ML model version runtime requirements"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(aliasesJSON, &version.Aliases, "ML model version aliases"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(artifactsJSON, &version.ArtifactIDs, "ML model version artifact ids"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &version.Metadata, "ML model version metadata"); err != nil {
		return nil, err
	}
	return version, nil
}

func (r *PgMLRegistryRepository) GetModelVersion(ctx context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	version, err := r.scanModelVersion(r.pool.QueryRow(ctx, `SELECT `+mlModelVersionColumns+` FROM ml_model_versions WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML model version: %w", err)
	}
	return version, nil
}

func (r *PgMLRegistryRepository) GetModelVersionByModelVersion(ctx context.Context, modelID uuid.UUID, versionString string) (*domain.MLModelVersion, error) {
	version, err := r.scanModelVersion(r.pool.QueryRow(ctx, `SELECT `+mlModelVersionColumns+` FROM ml_model_versions WHERE model_id = $1 AND version = $2`, modelID, versionString))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML model version by model/version: %w", err)
	}
	return version, nil
}

func (r *PgMLRegistryRepository) ListModelVersions(ctx context.Context, modelID uuid.UUID, limit, offset int) ([]domain.MLModelVersion, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+mlModelVersionColumns+` FROM ml_model_versions WHERE model_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, modelID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing ML model versions: %w", err)
	}
	defer rows.Close()
	var versions []domain.MLModelVersion
	for rows.Next() {
		version, err := r.scanModelVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML model version: %w", err)
		}
		versions = append(versions, *version)
	}
	return versions, rows.Err()
}

const mlArtifactColumns = `id, model_version_id, kind, format, uri, sha256, size_bytes, media_type, source, metadata, created_at`

func (r *PgMLRegistryRepository) UpsertArtifactRef(ctx context.Context, artifact *domain.MLArtifactRef) error {
	if artifact.ID == uuid.Nil {
		artifact.ID = uuid.New()
	}
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = time.Now().UTC()
	}
	source, err := marshalJSON(artifact.Source, "ML artifact source")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(artifact.Metadata, "ML artifact metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO ml_artifact_refs (`+mlArtifactColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE SET
			model_version_id = EXCLUDED.model_version_id, kind = EXCLUDED.kind, format = EXCLUDED.format,
			uri = EXCLUDED.uri, sha256 = EXCLUDED.sha256, size_bytes = EXCLUDED.size_bytes,
			media_type = EXCLUDED.media_type, source = EXCLUDED.source, metadata = EXCLUDED.metadata
	`, artifact.ID, artifact.ModelVersionID, artifact.Kind, artifact.Format, artifact.URI, artifact.SHA256, artifact.SizeBytes, artifact.MediaType, source, metadata, artifact.CreatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML artifact ref: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanArtifact(row pgx.Row) (*domain.MLArtifactRef, error) {
	artifact := &domain.MLArtifactRef{}
	var sourceJSON, metadataJSON []byte
	if err := row.Scan(&artifact.ID, &artifact.ModelVersionID, &artifact.Kind, &artifact.Format, &artifact.URI, &artifact.SHA256, &artifact.SizeBytes, &artifact.MediaType, &sourceJSON, &metadataJSON, &artifact.CreatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(sourceJSON, &artifact.Source, "ML artifact source"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &artifact.Metadata, "ML artifact metadata"); err != nil {
		return nil, err
	}
	return artifact, nil
}

func (r *PgMLRegistryRepository) ListArtifactRefsByModelVersion(ctx context.Context, modelVersionID uuid.UUID) ([]domain.MLArtifactRef, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mlArtifactColumns+` FROM ml_artifact_refs WHERE model_version_id = $1 ORDER BY created_at ASC`, modelVersionID)
	if err != nil {
		return nil, fmt.Errorf("listing ML artifacts: %w", err)
	}
	defer rows.Close()
	var artifacts []domain.MLArtifactRef
	for rows.Next() {
		artifact, err := r.scanArtifact(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML artifact: %w", err)
		}
		artifacts = append(artifacts, *artifact)
	}
	return artifacts, rows.Err()
}

const mlProvenanceColumns = `id, from_artifact_id, to_artifact_id, model_version_id, edge_kind, evidence, verified, defect, created_at`

func (r *PgMLRegistryRepository) UpsertProvenanceEdge(ctx context.Context, edge *domain.MLProvenanceEdge) error {
	if edge.ID == uuid.Nil {
		edge.ID = uuid.New()
	}
	if edge.CreatedAt.IsZero() {
		edge.CreatedAt = time.Now().UTC()
	}
	evidence, err := marshalJSON(edge.Evidence, "ML provenance evidence")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO ml_provenance_edges (`+mlProvenanceColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET
			from_artifact_id = EXCLUDED.from_artifact_id, to_artifact_id = EXCLUDED.to_artifact_id,
			model_version_id = EXCLUDED.model_version_id, edge_kind = EXCLUDED.edge_kind,
			evidence = EXCLUDED.evidence, verified = EXCLUDED.verified, defect = EXCLUDED.defect
	`, edge.ID, edge.FromArtifactID, edge.ToArtifactID, edge.ModelVersionID, edge.EdgeKind, evidence, edge.Verified, edge.Defect, edge.CreatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML provenance edge: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanProvenance(row pgx.Row) (*domain.MLProvenanceEdge, error) {
	edge := &domain.MLProvenanceEdge{}
	var evidenceJSON []byte
	if err := row.Scan(&edge.ID, &edge.FromArtifactID, &edge.ToArtifactID, &edge.ModelVersionID, &edge.EdgeKind, &evidenceJSON, &edge.Verified, &edge.Defect, &edge.CreatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(evidenceJSON, &edge.Evidence, "ML provenance evidence"); err != nil {
		return nil, err
	}
	return edge, nil
}

func (r *PgMLRegistryRepository) ListProvenanceEdgesByArtifact(ctx context.Context, artifactID uuid.UUID) ([]domain.MLProvenanceEdge, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mlProvenanceColumns+` FROM ml_provenance_edges WHERE from_artifact_id = $1 OR to_artifact_id = $1 ORDER BY created_at ASC`, artifactID)
	if err != nil {
		return nil, fmt.Errorf("listing ML provenance edges: %w", err)
	}
	defer rows.Close()
	var edges []domain.MLProvenanceEdge
	for rows.Next() {
		edge, err := r.scanProvenance(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML provenance edge: %w", err)
		}
		edges = append(edges, *edge)
	}
	return edges, rows.Err()
}

const mlRecipeColumns = `id, name, version, description, yaml, normalized_json, inputs, steps, outputs, metadata, created_at, updated_at`

func (r *PgMLRegistryRepository) UpsertRecipe(ctx context.Context, recipe *domain.MLRecipe) error {
	if recipe.ID == uuid.Nil {
		recipe.ID = uuid.New()
	}
	now := time.Now().UTC()
	if recipe.CreatedAt.IsZero() {
		recipe.CreatedAt = now
	}
	recipe.UpdatedAt = now
	normalized, err := marshalJSON(recipe.NormalizedJSON, "ML recipe normalized JSON")
	if err != nil {
		return err
	}
	inputs, err := marshalJSON(recipe.Inputs, "ML recipe inputs")
	if err != nil {
		return err
	}
	steps, err := marshalJSON(recipe.Steps, "ML recipe steps")
	if err != nil {
		return err
	}
	outputs, err := marshalJSON(recipe.Outputs, "ML recipe outputs")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(recipe.Metadata, "ML recipe metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ml_recipes (`+mlRecipeColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, version=EXCLUDED.version, description=EXCLUDED.description, yaml=EXCLUDED.yaml, normalized_json=EXCLUDED.normalized_json, inputs=EXCLUDED.inputs, steps=EXCLUDED.steps, outputs=EXCLUDED.outputs, metadata=EXCLUDED.metadata, updated_at=EXCLUDED.updated_at`, recipe.ID, recipe.Name, recipe.Version, recipe.Description, recipe.YAML, normalized, inputs, steps, outputs, metadata, recipe.CreatedAt, recipe.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML recipe: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanRecipe(row pgx.Row) (*domain.MLRecipe, error) {
	recipe := &domain.MLRecipe{}
	var normalizedJSON, inputsJSON, stepsJSON, outputsJSON, metadataJSON []byte
	if err := row.Scan(&recipe.ID, &recipe.Name, &recipe.Version, &recipe.Description, &recipe.YAML, &normalizedJSON, &inputsJSON, &stepsJSON, &outputsJSON, &metadataJSON, &recipe.CreatedAt, &recipe.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(normalizedJSON, &recipe.NormalizedJSON, "ML recipe normalized JSON"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(inputsJSON, &recipe.Inputs, "ML recipe inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(stepsJSON, &recipe.Steps, "ML recipe steps"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(outputsJSON, &recipe.Outputs, "ML recipe outputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &recipe.Metadata, "ML recipe metadata"); err != nil {
		return nil, err
	}
	return recipe, nil
}

func (r *PgMLRegistryRepository) GetRecipe(ctx context.Context, id uuid.UUID) (*domain.MLRecipe, error) {
	recipe, err := r.scanRecipe(r.pool.QueryRow(ctx, `SELECT `+mlRecipeColumns+` FROM ml_recipes WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML recipe: %w", err)
	}
	return recipe, nil
}

const mlRecipeRunColumns = `id, recipe_id, requested_by, status, inputs, parameters, step_states, result, error, metadata, started_at, finished_at, created_at, updated_at`

func (r *PgMLRegistryRepository) UpsertRecipeRun(ctx context.Context, run *domain.MLRecipeRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	inputs, err := marshalJSON(run.Inputs, "ML recipe run inputs")
	if err != nil {
		return err
	}
	params, err := marshalJSON(run.Parameters, "ML recipe run parameters")
	if err != nil {
		return err
	}
	steps, err := marshalJSON(run.StepStates, "ML recipe run step states")
	if err != nil {
		return err
	}
	result, err := marshalJSON(run.Result, "ML recipe run result")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(run.Metadata, "ML recipe run metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ml_recipe_runs (`+mlRecipeRunColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (id) DO UPDATE SET recipe_id=EXCLUDED.recipe_id, requested_by=EXCLUDED.requested_by, status=EXCLUDED.status, inputs=EXCLUDED.inputs, parameters=EXCLUDED.parameters, step_states=EXCLUDED.step_states, result=EXCLUDED.result, error=EXCLUDED.error, metadata=EXCLUDED.metadata, started_at=EXCLUDED.started_at, finished_at=EXCLUDED.finished_at, updated_at=EXCLUDED.updated_at`, run.ID, run.RecipeID, run.RequestedBy, run.Status, inputs, params, steps, result, run.Error, metadata, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML recipe run: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) GetRecipeRun(ctx context.Context, id uuid.UUID) (*domain.MLRecipeRun, error) {
	run := &domain.MLRecipeRun{}
	var inputsJSON, paramsJSON, stepsJSON, resultJSON, metadataJSON []byte
	err := r.pool.QueryRow(ctx, `SELECT `+mlRecipeRunColumns+` FROM ml_recipe_runs WHERE id = $1`, id).Scan(&run.ID, &run.RecipeID, &run.RequestedBy, &run.Status, &inputsJSON, &paramsJSON, &stepsJSON, &resultJSON, &run.Error, &metadataJSON, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML recipe run: %w", err)
	}
	if err := unmarshalJSON(inputsJSON, &run.Inputs, "ML recipe run inputs"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(paramsJSON, &run.Parameters, "ML recipe run parameters"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(stepsJSON, &run.StepStates, "ML recipe run step states"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(resultJSON, &run.Result, "ML recipe run result"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &run.Metadata, "ML recipe run metadata"); err != nil {
		return nil, err
	}
	return run, nil
}

const mlEndpointColumns = `id, name, environment_id, task_kinds, protocol, gateway, placement_policy, metadata, created_at, updated_at`

func (r *PgMLRegistryRepository) UpsertInferenceEndpoint(ctx context.Context, endpoint *domain.MLInferenceEndpoint) error {
	if endpoint.ID == uuid.Nil {
		endpoint.ID = uuid.New()
	}
	now := time.Now().UTC()
	if endpoint.CreatedAt.IsZero() {
		endpoint.CreatedAt = now
	}
	endpoint.UpdatedAt = now
	tasks, err := marshalJSON(endpoint.TaskKinds, "ML endpoint task kinds")
	if err != nil {
		return err
	}
	gateway, err := marshalJSON(endpoint.Gateway, "ML endpoint gateway")
	if err != nil {
		return err
	}
	placement, err := marshalJSON(endpoint.PlacementPolicy, "ML endpoint placement policy")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(endpoint.Metadata, "ML endpoint metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ml_inference_endpoints (`+mlEndpointColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (id) DO UPDATE SET name=EXCLUDED.name, environment_id=EXCLUDED.environment_id, task_kinds=EXCLUDED.task_kinds, protocol=EXCLUDED.protocol, gateway=EXCLUDED.gateway, placement_policy=EXCLUDED.placement_policy, metadata=EXCLUDED.metadata, updated_at=EXCLUDED.updated_at`, endpoint.ID, endpoint.Name, endpoint.EnvironmentID, tasks, endpoint.Protocol, gateway, placement, metadata, endpoint.CreatedAt, endpoint.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML inference endpoint: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanEndpoint(row pgx.Row) (*domain.MLInferenceEndpoint, error) {
	endpoint := &domain.MLInferenceEndpoint{}
	var tasksJSON, gatewayJSON, placementJSON, metadataJSON []byte
	if err := row.Scan(&endpoint.ID, &endpoint.Name, &endpoint.EnvironmentID, &tasksJSON, &endpoint.Protocol, &gatewayJSON, &placementJSON, &metadataJSON, &endpoint.CreatedAt, &endpoint.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(tasksJSON, &endpoint.TaskKinds, "ML endpoint task kinds"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(gatewayJSON, &endpoint.Gateway, "ML endpoint gateway"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(placementJSON, &endpoint.PlacementPolicy, "ML endpoint placement policy"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &endpoint.Metadata, "ML endpoint metadata"); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func (r *PgMLRegistryRepository) GetInferenceEndpoint(ctx context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	endpoint, err := r.scanEndpoint(r.pool.QueryRow(ctx, `SELECT `+mlEndpointColumns+` FROM ml_inference_endpoints WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML endpoint: %w", err)
	}
	return endpoint, nil
}

func (r *PgMLRegistryRepository) GetInferenceEndpointByNameEnv(ctx context.Context, name string, envID uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	endpoint, err := r.scanEndpoint(r.pool.QueryRow(ctx, `SELECT `+mlEndpointColumns+` FROM ml_inference_endpoints WHERE name = $1 AND environment_id = $2`, name, envID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML endpoint by name/env: %w", err)
	}
	return endpoint, nil
}

func (r *PgMLRegistryRepository) ListInferenceEndpoints(ctx context.Context, envID uuid.UUID, limit, offset int) ([]domain.MLInferenceEndpoint, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT ` + mlEndpointColumns + ` FROM ml_inference_endpoints ORDER BY name ASC LIMIT $1 OFFSET $2`
	args := []any{limit, offset}
	if envID != uuid.Nil {
		query = `SELECT ` + mlEndpointColumns + ` FROM ml_inference_endpoints WHERE environment_id = $1 ORDER BY name ASC LIMIT $2 OFFSET $3`
		args = []any{envID, limit, offset}
	}
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing ML endpoints: %w", err)
	}
	defer rows.Close()
	var endpoints []domain.MLInferenceEndpoint
	for rows.Next() {
		endpoint, err := r.scanEndpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML endpoint: %w", err)
		}
		endpoints = append(endpoints, *endpoint)
	}
	return endpoints, rows.Err()
}

const mlIntentColumns = `id, endpoint_id, environment_id, model_version_id, requested_by, source_kind, approval_status, status, runtime_preference, supersedes_intent_id, approval_metadata, metadata, created_at, approved_at, updated_at`

func (r *PgMLRegistryRepository) UpsertDeploymentIntent(ctx context.Context, intent *domain.MLDeploymentIntent) error {
	if intent.ID == uuid.Nil {
		intent.ID = uuid.New()
	}
	now := time.Now().UTC()
	if intent.CreatedAt.IsZero() {
		intent.CreatedAt = now
	}
	intent.UpdatedAt = now
	approval, err := marshalJSON(intent.ApprovalMetadata, "ML deployment intent approval metadata")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(intent.Metadata, "ML deployment intent metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ml_deployment_intents (`+mlIntentColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT (id) DO UPDATE SET endpoint_id=EXCLUDED.endpoint_id, environment_id=EXCLUDED.environment_id, model_version_id=EXCLUDED.model_version_id, requested_by=EXCLUDED.requested_by, source_kind=EXCLUDED.source_kind, approval_status=EXCLUDED.approval_status, status=EXCLUDED.status, runtime_preference=EXCLUDED.runtime_preference, supersedes_intent_id=EXCLUDED.supersedes_intent_id, approval_metadata=EXCLUDED.approval_metadata, metadata=EXCLUDED.metadata, approved_at=EXCLUDED.approved_at, updated_at=EXCLUDED.updated_at`, intent.ID, intent.EndpointID, intent.EnvironmentID, intent.ModelVersionID, intent.RequestedBy, intent.SourceKind, intent.ApprovalStatus, intent.Status, intent.RuntimePreference, intent.SupersedesIntentID, approval, metadata, intent.CreatedAt, intent.ApprovedAt, intent.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML deployment intent: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanIntent(row pgx.Row) (*domain.MLDeploymentIntent, error) {
	intent := &domain.MLDeploymentIntent{}
	var approvalJSON, metadataJSON []byte
	if err := row.Scan(&intent.ID, &intent.EndpointID, &intent.EnvironmentID, &intent.ModelVersionID, &intent.RequestedBy, &intent.SourceKind, &intent.ApprovalStatus, &intent.Status, &intent.RuntimePreference, &intent.SupersedesIntentID, &approvalJSON, &metadataJSON, &intent.CreatedAt, &intent.ApprovedAt, &intent.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(approvalJSON, &intent.ApprovalMetadata, "ML deployment intent approval metadata"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &intent.Metadata, "ML deployment intent metadata"); err != nil {
		return nil, err
	}
	return intent, nil
}

func (r *PgMLRegistryRepository) GetDeploymentIntent(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentIntent, error) {
	intent, err := r.scanIntent(r.pool.QueryRow(ctx, `SELECT `+mlIntentColumns+` FROM ml_deployment_intents WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML deployment intent: %w", err)
	}
	return intent, nil
}

func (r *PgMLRegistryRepository) ListDeploymentIntents(ctx context.Context, endpointID, envID uuid.UUID, limit, offset int) ([]domain.MLDeploymentIntent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT `+mlIntentColumns+` FROM ml_deployment_intents WHERE endpoint_id = $1 AND environment_id = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4`, endpointID, envID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("listing ML deployment intents: %w", err)
	}
	defer rows.Close()
	var intents []domain.MLDeploymentIntent
	for rows.Next() {
		intent, err := r.scanIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML deployment intent: %w", err)
		}
		intents = append(intents, *intent)
	}
	return intents, rows.Err()
}

const mlRunColumns = `id, deployment_intent_id, runtime_kind, endpoint_ref, worker_pubkey, worker_name, backend_endpoint, status, exit_code, stdout_ref, stderr_ref, verified_digests, metadata, started_at, finished_at, created_at, updated_at`

func (r *PgMLRegistryRepository) UpsertDeploymentRun(ctx context.Context, run *domain.MLDeploymentRun) error {
	if run.ID == uuid.Nil {
		run.ID = uuid.New()
	}
	now := time.Now().UTC()
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	run.UpdatedAt = now
	digests, err := marshalJSON(run.VerifiedDigests, "ML deployment run verified digests")
	if err != nil {
		return err
	}
	metadata, err := marshalJSON(run.Metadata, "ML deployment run metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ml_deployment_runs (`+mlRunColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT (id) DO UPDATE SET deployment_intent_id=EXCLUDED.deployment_intent_id, runtime_kind=EXCLUDED.runtime_kind, endpoint_ref=EXCLUDED.endpoint_ref, worker_pubkey=EXCLUDED.worker_pubkey, worker_name=EXCLUDED.worker_name, backend_endpoint=EXCLUDED.backend_endpoint, status=EXCLUDED.status, exit_code=EXCLUDED.exit_code, stdout_ref=EXCLUDED.stdout_ref, stderr_ref=EXCLUDED.stderr_ref, verified_digests=EXCLUDED.verified_digests, metadata=EXCLUDED.metadata, started_at=EXCLUDED.started_at, finished_at=EXCLUDED.finished_at, updated_at=EXCLUDED.updated_at`, run.ID, run.DeploymentIntentID, run.RuntimeKind, run.EndpointRef, run.WorkerPubkey, run.WorkerName, run.BackendEndpoint, run.Status, run.ExitCode, run.StdoutRef, run.StderrRef, digests, metadata, run.StartedAt, run.FinishedAt, run.CreatedAt, run.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML deployment run: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanRun(row pgx.Row) (*domain.MLDeploymentRun, error) {
	run := &domain.MLDeploymentRun{}
	var digestsJSON, metadataJSON []byte
	if err := row.Scan(&run.ID, &run.DeploymentIntentID, &run.RuntimeKind, &run.EndpointRef, &run.WorkerPubkey, &run.WorkerName, &run.BackendEndpoint, &run.Status, &run.ExitCode, &run.StdoutRef, &run.StderrRef, &digestsJSON, &metadataJSON, &run.StartedAt, &run.FinishedAt, &run.CreatedAt, &run.UpdatedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(digestsJSON, &run.VerifiedDigests, "ML deployment run verified digests"); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &run.Metadata, "ML deployment run metadata"); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *PgMLRegistryRepository) GetDeploymentRun(ctx context.Context, id uuid.UUID) (*domain.MLDeploymentRun, error) {
	run, err := r.scanRun(r.pool.QueryRow(ctx, `SELECT `+mlRunColumns+` FROM ml_deployment_runs WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML deployment run: %w", err)
	}
	return run, nil
}

func (r *PgMLRegistryRepository) ListDeploymentRuns(ctx context.Context, intentID uuid.UUID) ([]domain.MLDeploymentRun, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mlRunColumns+` FROM ml_deployment_runs WHERE deployment_intent_id = $1 ORDER BY created_at DESC`, intentID)
	if err != nil {
		return nil, fmt.Errorf("listing ML deployment runs: %w", err)
	}
	defer rows.Close()
	var runs []domain.MLDeploymentRun
	for rows.Next() {
		run, err := r.scanRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML deployment run: %w", err)
		}
		runs = append(runs, *run)
	}
	return runs, rows.Err()
}

const mlObservationColumns = `id, endpoint_id, environment_id, observed_model_version_id, observed_run_id, runtime_kind, backend_endpoint, backend_health, gateway_status, gateway_target, gateway_config_hash, source, metadata, observed_at`

func (r *PgMLRegistryRepository) UpsertInferenceObservation(ctx context.Context, obs *domain.MLInferenceObservation) error {
	if obs.ID == uuid.Nil {
		obs.ID = uuid.New()
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}
	metadata, err := marshalJSON(obs.Metadata, "ML inference observation metadata")
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO ml_inference_observations (`+mlObservationColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (id) DO UPDATE SET endpoint_id=EXCLUDED.endpoint_id, environment_id=EXCLUDED.environment_id, observed_model_version_id=EXCLUDED.observed_model_version_id, observed_run_id=EXCLUDED.observed_run_id, runtime_kind=EXCLUDED.runtime_kind, backend_endpoint=EXCLUDED.backend_endpoint, backend_health=EXCLUDED.backend_health, gateway_status=EXCLUDED.gateway_status, gateway_target=EXCLUDED.gateway_target, gateway_config_hash=EXCLUDED.gateway_config_hash, source=EXCLUDED.source, metadata=EXCLUDED.metadata, observed_at=EXCLUDED.observed_at`, obs.ID, obs.EndpointID, obs.EnvironmentID, obs.ObservedModelVersionID, obs.ObservedRunID, obs.RuntimeKind, obs.BackendEndpoint, obs.BackendHealth, obs.GatewayStatus, obs.GatewayTarget, obs.GatewayConfigHash, obs.Source, metadata, obs.ObservedAt)
	if err != nil {
		return fmt.Errorf("upserting ML inference observation: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanObservation(row pgx.Row) (*domain.MLInferenceObservation, error) {
	obs := &domain.MLInferenceObservation{}
	var metadataJSON []byte
	if err := row.Scan(&obs.ID, &obs.EndpointID, &obs.EnvironmentID, &obs.ObservedModelVersionID, &obs.ObservedRunID, &obs.RuntimeKind, &obs.BackendEndpoint, &obs.BackendHealth, &obs.GatewayStatus, &obs.GatewayTarget, &obs.GatewayConfigHash, &obs.Source, &metadataJSON, &obs.ObservedAt); err != nil {
		return nil, err
	}
	if err := unmarshalJSON(metadataJSON, &obs.Metadata, "ML inference observation metadata"); err != nil {
		return nil, err
	}
	return obs, nil
}

func (r *PgMLRegistryRepository) GetLatestInferenceObservation(ctx context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceObservation, error) {
	obs, err := r.scanObservation(r.pool.QueryRow(ctx, `SELECT `+mlObservationColumns+` FROM ml_inference_observations WHERE endpoint_id = $1 AND environment_id = $2 ORDER BY observed_at DESC LIMIT 1`, endpointID, envID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying latest ML inference observation: %w", err)
	}
	return obs, nil
}

const mlStateColumns = `endpoint_id, environment_id, desired_model_version_id, desired_intent_id, active_run_id, current_observation_id, drift_status, gateway_status, runtime_kind, backend_endpoint, backend_health, gateway_target, last_reconciled_at, updated_at`

func (r *PgMLRegistryRepository) UpsertInferenceState(ctx context.Context, state *domain.MLInferenceState) error {
	state.UpdatedAt = time.Now().UTC()
	_, err := r.pool.Exec(ctx, `INSERT INTO ml_inference_state (`+mlStateColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (endpoint_id, environment_id) DO UPDATE SET desired_model_version_id=EXCLUDED.desired_model_version_id, desired_intent_id=EXCLUDED.desired_intent_id, active_run_id=EXCLUDED.active_run_id, current_observation_id=EXCLUDED.current_observation_id, drift_status=EXCLUDED.drift_status, gateway_status=EXCLUDED.gateway_status, runtime_kind=EXCLUDED.runtime_kind, backend_endpoint=EXCLUDED.backend_endpoint, backend_health=EXCLUDED.backend_health, gateway_target=EXCLUDED.gateway_target, last_reconciled_at=EXCLUDED.last_reconciled_at, updated_at=EXCLUDED.updated_at`, state.EndpointID, state.EnvironmentID, state.DesiredModelVersionID, state.DesiredIntentID, state.ActiveRunID, state.CurrentObservationID, state.DriftStatus, state.GatewayStatus, state.RuntimeKind, state.BackendEndpoint, state.BackendHealth, state.GatewayTarget, state.LastReconciledAt, state.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upserting ML inference state: %w", err)
	}
	return nil
}

func (r *PgMLRegistryRepository) scanState(row pgx.Row) (*domain.MLInferenceState, error) {
	state := &domain.MLInferenceState{}
	if err := row.Scan(&state.EndpointID, &state.EnvironmentID, &state.DesiredModelVersionID, &state.DesiredIntentID, &state.ActiveRunID, &state.CurrentObservationID, &state.DriftStatus, &state.GatewayStatus, &state.RuntimeKind, &state.BackendEndpoint, &state.BackendHealth, &state.GatewayTarget, &state.LastReconciledAt, &state.UpdatedAt); err != nil {
		return nil, err
	}
	return state, nil
}

func (r *PgMLRegistryRepository) GetInferenceState(ctx context.Context, endpointID, envID uuid.UUID) (*domain.MLInferenceState, error) {
	state, err := r.scanState(r.pool.QueryRow(ctx, `SELECT `+mlStateColumns+` FROM ml_inference_state WHERE endpoint_id = $1 AND environment_id = $2`, endpointID, envID))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("querying ML inference state: %w", err)
	}
	return state, nil
}

func (r *PgMLRegistryRepository) ListInferenceStates(ctx context.Context) ([]domain.MLInferenceState, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+mlStateColumns+` FROM ml_inference_state`)
	if err != nil {
		return nil, fmt.Errorf("listing ML inference states: %w", err)
	}
	defer rows.Close()
	var states []domain.MLInferenceState
	for rows.Next() {
		state, err := r.scanState(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning ML inference state: %w", err)
		}
		states = append(states, *state)
	}
	return states, rows.Err()
}
