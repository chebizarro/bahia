package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/pkg/client"
)

// callStoreReadTool is the exclusive dispatch for canonical-state reads.
// Non-state tools and pending P2 writes retain their existing path.
func (s *Server) callStoreReadTool(ctx context.Context, name string, args map[string]interface{}) (*ToolResult, bool) {
	var result *ToolResult
	var err error
	switch name {
	case "bahia_list_services":
		var records []stateRecord
		records, err = s.readStateFamily(ctx, nostrpool.KindServiceRegistry)
		if err == nil {
			services := make([]domain.Service, 0, len(records))
			for _, record := range records {
				var svc *domain.Service
				svc, err = client.DecodeService(record.Event)
				if err != nil {
					break
				}
				if svc != nil {
					services = append(services, *svc)
				}
			}
			if err == nil {
				sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
				result, err = jsonResult(map[string]any{"services": servicesToMaps(services), "total": len(services)})
			}
		}
	case "bahia_get_service":
		key, value := "id", stringArg(args, "service_id")
		if value == "" {
			key, value = "name", stringArg(args, "name")
		}
		if value == "" {
			return errorResult("service_id or name is required"), true
		}
		if key == "id" {
			if _, parseErr := uuid.Parse(value); parseErr != nil {
				return errorResult("invalid service_id: " + parseErr.Error()), true
			}
		}
		var record *stateRecord
		record, err = s.readStateOne(ctx, nostrpool.KindServiceRegistry, key, value)
		if err == nil {
			if record == nil {
				return errorResult("service not found"), true
			}
			var svc *domain.Service
			svc, err = client.DecodeService(record.Event)
			if err == nil {
				result, err = jsonResult(serviceToMap(svc))
			}
		}
	case "bahia_list_environments":
		var records []stateRecord
		records, err = s.readStateFamily(ctx, nostrpool.KindEnvironmentRegistry)
		if err == nil {
			environments := make([]domain.Environment, 0, len(records))
			for _, record := range records {
				var env *domain.Environment
				env, err = client.DecodeEnvironment(record.Event)
				if err != nil {
					break
				}
				if env != nil {
					environments = append(environments, *env)
				}
			}
			if err == nil {
				sort.Slice(environments, func(i, j int) bool { return environments[i].Name < environments[j].Name })
				result, err = jsonResult(map[string]any{"environments": environmentsToMaps(environments), "total": len(environments)})
			}
		}
	case "bahia_get_environment":
		key, value := "id", stringArg(args, "environment_id")
		if value == "" {
			key, value = "name", stringArg(args, "name")
		}
		if value == "" {
			return errorResult("environment_id or name is required"), true
		}
		if key == "id" {
			if _, parseErr := uuid.Parse(value); parseErr != nil {
				return errorResult("invalid environment_id: " + parseErr.Error()), true
			}
		}
		var record *stateRecord
		record, err = s.readStateOne(ctx, nostrpool.KindEnvironmentRegistry, key, value)
		if err == nil {
			if record == nil {
				return errorResult("environment not found"), true
			}
			var env *domain.Environment
			env, err = client.DecodeEnvironment(record.Event)
			if err == nil {
				result, err = jsonResult(environmentToMap(env))
			}
		}
	case "bahia_list_states", "bahia_list_drifted":
		var records []stateRecord
		records, err = s.readStateFamily(ctx, nostrpool.KindServiceState)
		if err == nil {
			items := make([]map[string]any, 0, len(records))
			for _, record := range records {
				fields := record.Fields
				if env := stringArg(args, "environment_id"); env != "" && fields["environment_id"] != env {
					continue
				}
				if name == "bahia_list_drifted" && fields["drift_status"] != string(domain.DriftStatusDrifted) {
					continue
				}
				items = append(items, stateResultFields(fields))
			}
			key := "states"
			if name == "bahia_list_drifted" {
				key = "drifted"
			}
			result, err = jsonResult(map[string]any{key: items, "total": len(items)})
		}
	case "bahia_get_deployment_status":
		var records []stateRecord
		records, err = s.readStateFamily(ctx, nostrpool.KindServiceState)
		if err == nil {
			for _, record := range records {
				f := record.Fields
				if f["service_id"] == stringArg(args, "service_id") && f["environment_id"] == stringArg(args, "environment_id") {
					out := stateResultFields(f)
					delete(out, "updated_at")
					delete(out, "current_observation_id")
					result, err = jsonResult(out)
					break
				}
			}
			if result == nil {
				return errorResult("state not found"), true
			}
		}
	case "bahia_list_artifacts", "bahia_get_artifact":
		result, err = s.storeArtifactRead(ctx, name, args)
	case "bahia_list_builds", "bahia_get_build":
		result, err = s.storeBuildRead(ctx, name, args)
	case "bahia_list_intents", "bahia_get_intent", "bahia_list_runs", "bahia_get_run":
		result, err = s.storeDeploymentRead(ctx, name, args)
	case "bahia_list_workers", "bahia_get_worker", "bahia_get_worker_pricing":
		result, err = s.storeWorkerRead(ctx, name, args)
	case "bahia_list_policies", "bahia_get_policy":
		result, err = s.storePolicyRead(ctx, name, args)
	case "bahia_dns_list_endpoints", "bahia_assistant_dns_list_endpoints", "bahia_dns_list_drift", "bahia_assistant_dns_list_drift":
		result, err = s.storeDNSRead(ctx, name, args)
	case "bahia_ml_list_state", "bahia_ml_get_state", "bahia_ml_get_provenance":
		result, err = s.storeMLRead(ctx, name, args)
	case "bahia_llm_list_routes":
		result, err = s.storeLLMRoutes(ctx, args)
	case "bahia_llm_list_releases", "bahia_list_signatures", "bahia_list_verified_signatures", "bahia_has_verified_signature", "bahia_get_signature", "bahia_get_sbom", "bahia_get_sbom_packages", "bahia_search_sbom_packages", "bahia_get_observation":
		result, err = s.storeF74aRead(ctx, name, args)
	case "bahia_estimate_cost", "bahia_get_run_cost", "bahia_get_payment_history":
		result, err = s.storePaymentRead(ctx, name, args)
	case "bahia_worker_get_assignments", "bahia_worker_list_assignments", "bahia_worker_get_drain_status", "bahia_worker_list_drain_status":
		result, err = s.storeWorkerModelRead(ctx, name, args)
	case "bahia_package_list", "bahia_package_get":
		result, err = s.storePackageRead(ctx, name, args)
	case "bahia_list_secrets":
		result, err = s.storeSecretRead(ctx, args)
	case "bahia_list_notification_channels", "bahia_get_notification_channel":
		result, err = s.storeNotificationChannelRead(ctx, name, args)
	default:
		if isBackupToolName(name) {
			base := backupToolBaseName(name)
			if strings.HasPrefix(base, "list_backup_") || strings.HasPrefix(base, "inspect_backup_") {
				result, err = s.storeBackupRead(ctx, base, args)
				break
			}
		}
		return nil, false
	}
	if err != nil {
		return errorResult(err.Error()), true
	}
	return result, true
}

func (s *Server) storePaymentRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	if name == "bahia_estimate_cost" {
		runID, err := parseRequiredUUIDArg(args, "run_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		run, err := s.readStateOne(ctx, nostrpool.KindDeploymentRunRegistry, "id", runID.String())
		if err != nil {
			return nil, err
		}
		if run == nil {
			return errorResult("run not found"), nil
		}
		workerPubkey := stringFromRecord(run.Fields, "worker_pubkey")
		if workerPubkey == "" {
			return errorResult("deployment run has no assigned worker"), nil
		}
		workerRecord, err := s.readStateOne(ctx, nostrpool.KindWorkerState, "pubkey", workerPubkey)
		if err != nil {
			return nil, err
		}
		if workerRecord == nil {
			return errorResult("worker not found"), nil
		}
		var worker domain.Worker
		if err := json.Unmarshal(workerRecord.Content, &worker); err != nil {
			return nil, err
		}
		if len(worker.Pricing) == 0 {
			return errorResult("worker " + worker.PubKey + " has no pricing information"), nil
		}
		duration := optionalIntArg(args, "estimated_duration_secs", 0)
		if duration <= 0 {
			duration = worker.MaxDurationSecs
		}
		if duration <= 0 {
			duration = 300
		}
		estimate := domain.EstimateCost(worker.Pricing[0], duration)
		estimate.WorkerPubkey, estimate.WorkerName = worker.PubKey, worker.Name
		return jsonResult(costEstimateToMap(&estimate))
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindPaymentRecord)
	if err != nil {
		return nil, err
	}
	if name == "bahia_get_run_cost" {
		runID, err := parseRequiredUUIDArg(args, "run_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		payments := make([]domain.PaymentRecord, 0)
		summary := &service.CostSummary{}
		for _, record := range records {
			if record.Fields["deployment_run_id"] != runID.String() {
				continue
			}
			var payment domain.PaymentRecord
			if err := json.Unmarshal(record.Content, &payment); err != nil {
				return nil, err
			}
			payments = append(payments, payment)
			switch payment.Direction {
			case domain.PaymentDirectionPayment:
				summary.TotalPaid += payment.AmountSats
				summary.PaymentCount++
			case domain.PaymentDirectionChange:
				summary.TotalChange += payment.AmountSats
				summary.ChangeCount++
			}
		}
		sort.SliceStable(payments, func(i, j int) bool { return payments[i].CreatedAt.Before(payments[j].CreatedAt) })
		summary.NetCost = summary.TotalPaid - summary.TotalChange
		return jsonResult(map[string]any{"run_id": runID.String(), "summary": costSummaryToMap(summary), "payments": paymentRecordsToMaps(payments)})
	}
	worker := firstNonEmpty(stringArg(args, "worker_pubkey"), stringArg(args, "worker"))
	if worker == "" {
		return errorResult("worker_pubkey is required"), nil
	}
	limit := optionalIntArg(args, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	payments := make([]domain.PaymentRecord, 0)
	for _, record := range records {
		if record.Fields["worker_pubkey"] != worker {
			continue
		}
		var payment domain.PaymentRecord
		if err := json.Unmarshal(record.Content, &payment); err != nil {
			return nil, err
		}
		payments = append(payments, payment)
	}
	sort.SliceStable(payments, func(i, j int) bool { return payments[i].CreatedAt.After(payments[j].CreatedAt) })
	if len(payments) > limit {
		payments = payments[:limit]
	}
	return jsonResult(map[string]any{"worker_pubkey": worker, "payments": paymentRecordsToMaps(payments), "total": len(payments), "limit": limit})
}

func (s *Server) storeLLMRoutes(ctx context.Context, args map[string]any) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindLLMRouteRegistry)
	if err != nil {
		return nil, err
	}
	routes := make([]map[string]any, 0, len(records))
	for _, record := range records {
		var route domain.LLMRoute
		if err := json.Unmarshal(record.Content, &route); err != nil {
			return nil, err
		}
		routes = append(routes, llmRouteToMap(&route))
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i]["name"].(string) < routes[j]["name"].(string) })
	limit, offset := limitOffsetArgs(args, 100)
	if offset > len(routes) {
		offset = len(routes)
	}
	routes = routes[offset:]
	if limit > 0 && len(routes) > limit {
		routes = routes[:limit]
	}
	return jsonResult(map[string]any{"routes": routes, "total": len(routes), "registry_kind": nostrpool.KindLLMRouteRegistry})
}

func (s *Server) storeSecretRead(ctx context.Context, args map[string]any) (*ToolResult, error) {
	serviceID := stringArg(args, "service_id")
	if _, err := uuid.Parse(serviceID); err != nil {
		return errorResult("invalid service_id: " + err.Error()), nil
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindSecretRegistry)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0)
	for _, rec := range records {
		if rec.Fields["service_id"] == serviceID {
			var ref domain.SecretRef
			if err := json.Unmarshal(rec.Content, &ref); err != nil {
				return nil, err
			}
			item := map[string]any{"id": ref.ID.String(), "service_id": ref.ServiceID.String(), "name": ref.Name, "encryption_method": string(ref.EncryptionMethod), "version": ref.Version, "created_by": ref.CreatedBy, "created_at": ref.CreatedAt.Format("2006-01-02T15:04:05Z"), "updated_at": ref.UpdatedAt.Format("2006-01-02T15:04:05Z")}
			if ref.EnvironmentID != nil {
				item["environment_id"] = ref.EnvironmentID.String()
			}
			items = append(items, item)
		}
	}
	return jsonResult(map[string]any{"secrets": items, "total": len(items)})
}

func (s *Server) storeNotificationChannelRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindNotificationChannelRegistry)
	if err != nil {
		return nil, err
	}
	if name == "bahia_get_notification_channel" {
		id, err := parseRequiredUUIDArg(args, "channel_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		for _, rec := range records {
			if rec.Fields["id"] == id.String() {
				var channel domain.NotificationChannel
				if err := json.Unmarshal(rec.Content, &channel); err != nil {
					return nil, err
				}
				return jsonResult(notificationChannelToMap(&channel))
			}
		}
		return errorResult("notification channel not found"), nil
	}
	items := make([]domain.NotificationChannel, 0)
	for _, rec := range records {
		var channel domain.NotificationChannel
		if err := json.Unmarshal(rec.Content, &channel); err != nil {
			return nil, err
		}
		if enabled, ok := args["enabled"].(bool); ok && enabled && !channel.Enabled {
			continue
		}
		items = append(items, channel)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return jsonResult(map[string]any{"channels": notificationChannelsToMaps(items), "total": len(items)})
}

func (s *Server) storePackageRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	repositories, err := s.readStateFamily(ctx, nostrpool.KindPackageRepositoryRegistry)
	if err != nil {
		return nil, err
	}
	if name == "bahia_package_list" && stringArg(args, "repository_id") == "" {
		items := make([]domain.PackageRepository, 0, len(repositories))
		for _, rec := range repositories {
			var item domain.PackageRepository
			if err := json.Unmarshal(rec.Content, &item); err != nil {
				return nil, err
			}
			if item.Deleted && !boolArg(args, "include_deleted") {
				continue
			}
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		return jsonResult(map[string]any{"repositories": items})
	}
	var repository *domain.PackageRepository
	for _, rec := range repositories {
		if id := stringArg(args, "repository_id"); id != "" && rec.Fields["id"] != id {
			continue
		}
		if stringArg(args, "repository_id") == "" {
			name := firstNonEmpty(stringArg(args, "repository_name"), stringArg(args, "name"))
			if rec.Fields["name"] != name {
				continue
			}
		}
		var item domain.PackageRepository
		if err := json.Unmarshal(rec.Content, &item); err != nil {
			return nil, err
		}
		repository = &item
		break
	}
	if repository == nil {
		return errorResult("package repository not found"), nil
	}
	artifacts, err := s.readStateFamily(ctx, nostrpool.KindPackageArtifactRegistry)
	if err != nil {
		return nil, err
	}
	if name == "bahia_package_list" {
		items := make([]domain.PackageArtifact, 0)
		for _, rec := range artifacts {
			if rec.Fields["repository_id"] == repository.ID.String() {
				var item domain.PackageArtifact
				if err := json.Unmarshal(rec.Content, &item); err != nil {
					return nil, err
				}
				if item.Format == "" {
					item.Format = repository.Format
				}
				if item.Deleted {
					continue
				}
				items = append(items, item)
			}
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].PackageName != items[j].PackageName {
				return items[i].PackageName < items[j].PackageName
			}
			if items[i].Version != items[j].Version {
				return items[i].Version < items[j].Version
			}
			return items[i].Filename < items[j].Filename
		})
		limit, offset := limitOffsetArgs(args, 100)
		if offset > len(items) {
			offset = len(items)
		}
		items = items[offset:]
		if limit > 0 && len(items) > limit {
			items = items[:limit]
		}
		return jsonResult(map[string]any{"artifacts": items})
	}
	if stringArg(args, "package_name") == "" {
		return jsonResult(repository)
	}
	for _, rec := range artifacts {
		f := rec.Fields
		if f["repository_id"] != repository.ID.String() || f["namespace"] != stringArg(args, "namespace") || f["package_name"] != stringArg(args, "package_name") || f["version"] != stringArg(args, "version") || f["filename"] != stringArg(args, "filename") {
			continue
		}
		var item domain.PackageArtifact
		if err := json.Unmarshal(rec.Content, &item); err != nil {
			return nil, err
		}
		if item.Format == "" {
			item.Format = repository.Format
		}
		return jsonResult(item)
	}
	return errorResult("package artifact not found"), nil
}

func storeBackupItems[T any](s *Server, ctx context.Context, family int, args map[string]any, list bool, listKey, itemKey, idArg, nameArg string) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, family)
	if err != nil {
		return nil, err
	}
	if list {
		sort.SliceStable(records, func(i, j int) bool {
			left, right := records[i].Fields, records[j].Fields
			switch itemKey {
			case "run", "restore", "retention_run":
				if left["created_at"] != right["created_at"] {
					return stringFromRecord(left, "created_at") > stringFromRecord(right, "created_at")
				}
				return stringFromRecord(left, "id") > stringFromRecord(right, "id")
			case "recipe":
				if left["name"] != right["name"] {
					return stringFromRecord(left, "name") < stringFromRecord(right, "name")
				}
				return stringFromRecord(left, "version") < stringFromRecord(right, "version")
			default:
				return stringFromRecord(left, "name") < stringFromRecord(right, "name")
			}
		})
	}
	if !list {
		id := stringArg(args, idArg)
		name := stringArg(args, nameArg)
		if name == "" {
			name = stringArg(args, "name")
		}
		if id == "" && name == "" {
			return errorResult(idArg + " or name is required"), nil
		}
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return errorResult("invalid " + idArg + ": " + err.Error()), nil
			}
		}
		for _, rec := range records {
			if rec.Fields["id"] != id && (id != "" || rec.Fields["name"] != name) {
				continue
			}
			if itemKey == "recipe" && id == "" && rec.Fields["version"] != stringArg(args, "version") {
				continue
			}
			var item T
			if err := json.Unmarshal(rec.Content, &item); err != nil {
				return nil, err
			}
			out := map[string]any{itemKey: item}
			return jsonResult(out)
		}
		return errorResult("backup " + strings.ReplaceAll(itemKey, "_", " ") + " not found"), nil
	}
	items := make([]T, 0, len(records))
	for _, rec := range records {
		if status := stringArg(args, "status"); status != "" && rec.Fields["status"] != status {
			continue
		}
		var item T
		if err := json.Unmarshal(rec.Content, &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	limit, offset := limitOffsetArgs(args, 100)
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return jsonResult(map[string]any{listKey: items, "total": len(items)})
}

func (s *Server) storeBackupRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	list := strings.HasPrefix(name, "list_")
	switch name {
	case "list_backup_repositories", "inspect_backup_repository":
		return storeBackupItems[domain.BackupRepository](s, ctx, nostrpool.KindBackupRepositoryRegistry, args, list, "repositories", "repository", "repository_id", "repository")
	case "list_backup_policies", "inspect_backup_policy":
		return storeBackupItems[domain.BackupPolicy](s, ctx, nostrpool.KindBackupPolicyRegistry, args, list, "policies", "policy", "policy_id", "policy")
	case "list_backup_recipes", "inspect_backup_recipe":
		return storeBackupItems[domain.BackupRecipe](s, ctx, nostrpool.KindBackupRecipeRegistry, args, list, "recipes", "recipe", "recipe_id", "recipe")
	case "list_backup_definitions", "inspect_backup_definition":
		return storeBackupItems[domain.BackupDefinition](s, ctx, nostrpool.KindBackupDefinitionRegistry, args, list, "definitions", "definition", "definition_id", "name")
	case "inspect_backup_run":
		id, err := parseRequiredUUIDArg(args, "run_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		runRecord, err := s.readStateOne(ctx, nostrpool.KindBackupRunState, "id", id.String())
		if err != nil {
			return nil, err
		}
		if runRecord == nil {
			return errorResult("backup run not found"), nil
		}
		var run domain.BackupRun
		if err := json.Unmarshal(runRecord.Content, &run); err != nil {
			return nil, err
		}
		out := map[string]any{"run": run}
		verifications, err := s.readStateFamily(ctx, nostrpool.KindBackupVerificationState)
		if err != nil {
			return nil, err
		}
		for _, record := range verifications {
			if record.Fields["backup_run_id"] != id.String() {
				continue
			}
			var verification domain.BackupVerificationRecord
			if err := json.Unmarshal(record.Content, &verification); err != nil {
				return nil, err
			}
			out["verification"] = verification
			break
		}
		return jsonResult(out)
	case "list_backup_runs":
		return storeBackupItems[domain.BackupRun](s, ctx, nostrpool.KindBackupRunState, args, list, "runs", "run", "run_id", "name")
	case "list_backup_restores", "inspect_backup_restore":
		return storeBackupItems[domain.BackupRestoreRun](s, ctx, nostrpool.KindBackupRestoreState, args, list, "restores", "restore", "restore_id", "name")
	case "list_backup_retention_runs", "inspect_backup_retention_run":
		return storeBackupItems[domain.BackupRetentionRun](s, ctx, nostrpool.KindBackupRetentionRegistry, args, list, "retention_runs", "retention_run", "retention_run_id", "name")
	default:
		return errorResult("unknown backup read tool: " + name), nil
	}
}

func resultFields(fields map[string]any, required []string, optional ...string) map[string]any {
	out := make(map[string]any, len(required)+len(optional))
	for _, key := range required {
		out[key] = fields[key]
	}
	for _, key := range optional {
		if fields[key] != nil && fields[key] != "" {
			out[key] = fields[key]
		}
	}
	return out
}

func stateResultFields(fields map[string]any) map[string]any {
	out := resultFields(fields, []string{"service_id", "environment_id", "drift_status", "updated_at"}, "desired_artifact_id", "desired_intent_id", "last_successful_run_id", "current_observation_id", "last_reconciled_at")
	for _, key := range []string{"updated_at", "last_reconciled_at"} {
		if value, ok := out[key].(string); ok {
			out[key] = mcpRecordTime(value)
		}
	}
	return out
}

func mcpRecordTime(value string) string {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func (s *Server) storeArtifactRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindArtifactRegistry)
	if err != nil {
		return nil, err
	}
	if name == "bahia_get_artifact" {
		id := stringArg(args, "artifact_id")
		if _, err := uuid.Parse(id); err != nil {
			return errorResult("invalid artifact_id: " + err.Error()), nil
		}
		for _, rec := range records {
			if rec.Fields["id"] == id {
				f := rec.Fields
				out := resultFields(f, []string{"id", "build_id", "service_id", "image_repo", "image_tag", "image_digest", "scan_status", "created_at"})
				out["created_at"] = mcpRecordTime(fmt.Sprint(out["created_at"]))
				return jsonResult(out)
			}
		}
		return errorResult("artifact not found"), nil
	}
	serviceID := stringArg(args, "service_id")
	if _, err := uuid.Parse(serviceID); err != nil {
		return errorResult("invalid service_id: " + err.Error()), nil
	}
	items := make([]map[string]any, 0)
	for _, rec := range records {
		if rec.Fields["service_id"] == serviceID {
			f := rec.Fields
			out := resultFields(f, []string{"id", "build_id", "service_id", "image_repo", "image_tag", "image_digest", "scan_status", "created_at"})
			out["created_at"] = mcpRecordTime(fmt.Sprint(out["created_at"]))
			items = append(items, out)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["created_at"]) > fmt.Sprint(items[j]["created_at"]) })
	limit := optionalIntArg(args, "limit", 20)
	if limit >= 0 && len(items) > limit {
		items = items[:limit]
	}
	return jsonResult(map[string]any{"artifacts": items, "total": len(items)})
}

func (s *Server) storeBuildRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	principal := auth.GetPrincipal(ctx)
	if s.rbac == nil && (principal == nil || principal.Method != auth.MethodSystem || !principal.HasRole(string(domain.RoleAdmin))) {
		return errorResult("service authorization is not configured"), nil
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindBuildRegistry)
	if err != nil {
		return nil, err
	}
	if name == "bahia_get_build" {
		id := stringArg(args, "build_id")
		if _, err := uuid.Parse(id); err != nil {
			return errorResult("invalid build_id: " + err.Error()), nil
		}
		for _, rec := range records {
			if rec.Fields["id"] == id {
				f := rec.Fields
				serviceID, parseErr := uuid.Parse(fmt.Sprint(f["service_id"]))
				if parseErr != nil {
					return nil, parseErr
				}
				if denied := s.authorizeServicePermission(ctx, serviceID, domain.PermReadServices, "service"); denied != nil {
					return denied, nil
				}
				out := resultFields(f, []string{"id", "service_id", "git_sha", "git_ref", "status", "ci_system", "ci_run_id", "created_at"})
				out["created_at"] = mcpRecordTime(fmt.Sprint(out["created_at"]))
				return jsonResult(out)
			}
		}
		return errorResult("build not found"), nil
	}
	serviceID := stringArg(args, "service_id")
	parsedServiceID, err := uuid.Parse(serviceID)
	if err != nil {
		return errorResult("invalid service_id: " + err.Error()), nil
	}
	if denied := s.authorizeServicePermission(ctx, parsedServiceID, domain.PermReadServices, "service"); denied != nil {
		return denied, nil
	}
	items := make([]map[string]any, 0)
	for _, rec := range records {
		if rec.Fields["service_id"] == serviceID {
			f := rec.Fields
			out := resultFields(f, []string{"id", "service_id", "git_sha", "git_ref", "status", "ci_system", "ci_run_id", "created_at"}, "loom_job_id", "started_at", "finished_at")
			out["created_at"] = mcpRecordTime(fmt.Sprint(out["created_at"]))
			items = append(items, out)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["created_at"]) > fmt.Sprint(items[j]["created_at"]) })
	limit := optionalIntArg(args, "limit", 20)
	if limit >= 0 && len(items) > limit {
		items = items[:limit]
	}
	return jsonResult(map[string]any{"builds": items, "total": len(items)})
}

func (s *Server) storeDeploymentRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	if name == "bahia_list_intents" || name == "bahia_get_intent" {
		records, err := s.readStateFamily(ctx, nostrpool.KindDeploymentIntentRegistry)
		if err != nil {
			return nil, err
		}
		if name == "bahia_get_intent" {
			id := stringArg(args, "intent_id")
			if id == "" {
				return errorResult("intent_id is required"), nil
			}
			if _, err := uuid.Parse(id); err != nil {
				return errorResult("invalid intent_id: " + err.Error()), nil
			}
			for _, rec := range records {
				if rec.Fields["id"] == id {
					return jsonResult(intentResultFields(rec.Fields))
				}
			}
			return errorResult("intent not found"), nil
		}
		serviceID, err := uuid.Parse(stringArg(args, "service_id"))
		if err != nil {
			return errorResult("invalid service_id: " + err.Error()), nil
		}
		envID, err := uuid.Parse(stringArg(args, "environment_id"))
		if err != nil {
			return errorResult("invalid environment_id: " + err.Error()), nil
		}
		items := make([]map[string]any, 0)
		for _, rec := range records {
			if rec.Fields["service_id"] == serviceID.String() && rec.Fields["environment_id"] == envID.String() {
				items = append(items, intentResultFields(rec.Fields))
			}
		}
		sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["created_at"]) > fmt.Sprint(items[j]["created_at"]) })
		limit := optionalIntArg(args, "limit", 20)
		if limit >= 0 && len(items) > limit {
			items = items[:limit]
		}
		return jsonResult(map[string]any{"intents": items, "total": len(items)})
	}
	records, err := s.readStateFamily(ctx, nostrpool.KindDeploymentRunRegistry)
	if err != nil {
		return nil, err
	}
	if name == "bahia_get_run" {
		id := stringArg(args, "run_id")
		if _, err := uuid.Parse(id); err != nil {
			return errorResult("invalid run_id: " + err.Error()), nil
		}
		for _, rec := range records {
			if rec.Fields["id"] == id {
				return jsonResult(runResultFields(rec.Fields))
			}
		}
		return errorResult("run not found"), nil
	}
	intentID, err := uuid.Parse(stringArg(args, "intent_id"))
	if err != nil {
		return errorResult("invalid intent_id: " + err.Error()), nil
	}
	items := make([]map[string]any, 0)
	for _, rec := range records {
		if rec.Fields["deployment_intent_id"] == intentID.String() {
			items = append(items, runResultFields(rec.Fields))
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return fmt.Sprint(items[i]["created_at"]) > fmt.Sprint(items[j]["created_at"]) })
	return jsonResult(map[string]any{"runs": items, "total": len(items)})
}

func intentResultFields(fields map[string]any) map[string]any {
	out := resultFields(fields, []string{"id", "service_id", "environment_id", "artifact_id", "requested_by", "source_kind", "approval_status", "status", "created_at", "updated_at"}, "supersedes_intent_id", "approved_at")
	for _, key := range []string{"created_at", "updated_at", "approved_at"} {
		if value, ok := out[key].(string); ok {
			out[key] = mcpRecordTime(value)
		}
	}
	return out
}

func runResultFields(fields map[string]any) map[string]any {
	out := resultFields(fields, []string{"id", "deployment_intent_id", "status", "created_at", "updated_at"}, "loom_job_id", "worker_pubkey", "worker_name", "exit_code", "started_at", "finished_at")
	for _, key := range []string{"created_at", "updated_at", "started_at", "finished_at"} {
		if value, ok := out[key].(string); ok {
			out[key] = mcpRecordTime(value)
		}
	}
	return out
}

func (s *Server) storeWorkerRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindWorkerState)
	if err != nil {
		return nil, err
	}
	workers := make([]domain.Worker, 0, len(records))
	for _, rec := range records {
		var worker domain.Worker
		if err := json.Unmarshal(rec.Content, &worker); err != nil {
			return nil, err
		}
		workers = append(workers, worker)
	}
	sort.SliceStable(workers, func(i, j int) bool { return workers[i].LastAdvertisementAt.After(workers[j].LastAdvertisementAt) })
	if name == "bahia_get_worker" || name == "bahia_get_worker_pricing" {
		pubkey := stringArg(args, "pubkey")
		if pubkey == "" {
			return errorResult("pubkey is required"), nil
		}
		for _, worker := range workers {
			if worker.PubKey == pubkey {
				if name == "bahia_get_worker_pricing" {
					return jsonResult(map[string]any{"pubkey": pubkey, "name": worker.Name, "pricing": worker.Pricing})
				}
				return jsonResult(workerToMap(&worker))
			}
		}
		return errorResult("worker not found"), nil
	}
	limit := optionalIntArg(args, "limit", 50)
	if limit >= 0 && len(workers) > limit {
		workers = workers[:limit]
	}
	items := make([]domain.Worker, 0)
	for _, worker := range workers {
		if capability := stringArg(args, "capability"); capability != "" && !worker.HasSoftware(capability) {
			continue
		}
		if available, ok := args["available"].(bool); ok && (worker.ComputeStatus(time.Now()) == domain.WorkerStatusOnline) != available {
			continue
		}
		items = append(items, worker)
	}
	return jsonResult(map[string]any{"workers": workersToMaps(items), "total": len(items)})
}

func (s *Server) storePolicyRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	records, err := s.readStateFamily(ctx, nostrpool.KindPolicyRegistry)
	if err != nil {
		return nil, err
	}
	policies := make([]domain.DeploymentPolicy, 0, len(records))
	for _, rec := range records {
		var policy domain.DeploymentPolicy
		if err := json.Unmarshal(rec.Content, &policy); err != nil {
			return nil, err
		}
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].Name < policies[j].Name })
	if name == "bahia_get_policy" {
		id := stringArg(args, "policy_id")
		if id == "" {
			return errorResult("policy_id is required"), nil
		}
		if _, err := uuid.Parse(id); err != nil {
			return errorResult("invalid policy_id: " + err.Error()), nil
		}
		for _, policy := range policies {
			if policy.ID.String() == id {
				return jsonResult(policyToMap(&policy))
			}
		}
		return errorResult("policy not found"), nil
	}
	items := make([]domain.DeploymentPolicy, 0)
	for _, policy := range policies {
		if enabled, ok := args["enabled"].(bool); ok && enabled && !policy.Enabled {
			continue
		}
		if env := stringArg(args, "environment_id"); env != "" && (policy.EnvironmentID == nil || policy.EnvironmentID.String() != env) {
			continue
		}
		items = append(items, policy)
	}
	return jsonResult(map[string]any{"policies": policiesToMaps(items), "total": len(items)})
}

func (s *Server) storeDNSRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	endpoints, err := s.readDNSEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	drift := strings.Contains(name, "drift")
	items := make([]domain.DNSEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if drift && (endpoint.DriftStatus == "" || endpoint.DriftStatus == domain.DriftStatusInSync) {
			continue
		}
		items = append(items, endpoint)
	}
	total := len(items)
	if drift {
		sort.SliceStable(items, func(i, j int) bool { return items[i].MaterializedAt.After(items[j].MaterializedAt) })
	}
	page := pageDNSEndpointsForMCP(items, args)
	key := "endpoints"
	if drift {
		key = "drift"
	}
	return jsonResult(map[string]any{key: page, "total": total})
}

func (s *Server) readDNSEndpoints(ctx context.Context) ([]domain.DNSEndpoint, error) {
	records, err := s.readStateFamily(ctx, kinds.CPStateFamilyDNSEndpoint.LegacyKind())
	if err != nil {
		return nil, err
	}
	items := make([]domain.DNSEndpoint, 0, len(records))
	for _, rec := range records {
		var endpoint domain.DNSEndpoint
		if err := json.Unmarshal(rec.Content, &endpoint); err != nil {
			return nil, err
		}
		items = append(items, endpoint)
	}
	return items, nil
}

func (s *Server) storeMLRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	family := nostrpool.KindMLInferenceEndpointState
	if name == "bahia_ml_get_provenance" {
		family = nostrpool.KindMLArtifactProvenanceGraph
	}
	records, err := s.readStateFamily(ctx, family)
	if err != nil {
		return nil, err
	}
	if name == "bahia_ml_list_state" {
		items := make([]domain.MLInferenceState, 0, len(records))
		for _, rec := range records {
			var state domain.MLInferenceState
			if err := json.Unmarshal(rec.Content, &state); err != nil {
				return nil, err
			}
			items = append(items, state)
		}
		return jsonResult(map[string]any{"states": items, "total": len(items), "read_model_kind": family})
	}
	if name == "bahia_ml_get_state" {
		endpointID, err := parseRequiredUUIDArg(args, "endpoint_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		envID, err := parseRequiredUUIDArg(args, "environment_id")
		if err != nil {
			return errorResult(err.Error()), nil
		}
		for _, rec := range records {
			if rec.Fields["endpoint_id"] == endpointID.String() && rec.Fields["environment_id"] == envID.String() {
				var state domain.MLInferenceState
				if err := json.Unmarshal(rec.Content, &state); err != nil {
					return nil, err
				}
				return jsonResult(map[string]any{"state": state, "read_model_kind": family})
			}
		}
		return errorResult("ML state not found"), nil
	}
	artifactID, err := parseRequiredUUIDArg(args, "artifact_id")
	if err != nil {
		return errorResult(err.Error()), nil
	}
	for _, rec := range records {
		var graph struct {
			Artifact domain.MLArtifactRef      `json:"artifact"`
			Edges    []domain.MLProvenanceEdge `json:"edges"`
		}
		if err := json.Unmarshal(rec.Content, &graph); err != nil {
			return nil, err
		}
		if graph.Artifact.ID == artifactID {
			return jsonResult(map[string]any{"artifact": graph.Artifact, "edges": graph.Edges, "read_model_kind": family})
		}
	}
	return errorResult("ML artifact not found"), nil
}

func (s *Server) storeWorkerModelRead(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	family, key, listKey, topic := nostrpool.KindWorkerAssignmentState, "assignment_state", "assignment_states", kinds.WorkerAssignmentTopic
	if strings.Contains(name, "drain") {
		family, key, listKey, topic = nostrpool.KindWorkerDrainStatus, "drain_status", "drain_statuses", kinds.WorkerDrainTopic
	}
	records, err := s.readStateFamily(ctx, family)
	if err != nil {
		return nil, err
	}
	if strings.Contains(name, "list") {
		items := make([]map[string]any, 0, len(records))
		for _, rec := range records {
			items = append(items, rec.Fields)
		}
		return jsonResult(map[string]any{listKey: items, "total": len(items), "read_model_kind": nostrpool.KindCASControlState, "read_model_topic": topic})
	}
	for _, rec := range records {
		if rec.Fields["worker_pubkey"] == stringArg(args, "worker_pubkey") {
			return jsonResult(map[string]any{key: rec.Fields, "read_model_kind": nostrpool.KindCASControlState, "read_model_topic": topic})
		}
	}
	return errorResult("worker not found"), nil
}
