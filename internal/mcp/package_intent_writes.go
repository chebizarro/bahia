package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

func (s *Server) packageRepositoryID(ctx context.Context, args map[string]any, idKey, nameKey string) (uuid.UUID, error) {
	if id, err := optionalUUIDArgStrict(args, idKey); err != nil {
		return uuid.Nil, err
	} else if id != uuid.Nil {
		return id, nil
	}
	name := strings.TrimSpace(stringArg(args, nameKey))
	if name == "" {
		return uuid.Nil, fmt.Errorf("%s or %s is required", idKey, nameKey)
	}
	record, err := s.readStateOne(ctx, nostrpool.KindPackageRepositoryRegistry, "name", name)
	if err != nil {
		return uuid.Nil, err
	}
	if record == nil {
		return uuid.Nil, fmt.Errorf("canonical package repository %q not found", name)
	}
	return uuid.Parse(stringFromRecord(record.Fields, "id"))
}

func (s *Server) packageIntentWrite(ctx context.Context, name string, args map[string]any, intentID string) (intentWrite, error) {
	w := intentWrite{domain: "package", content: map[string]any{}}
	var err error
	w.orgID, err = s.intentOrgFromState(ctx, args, 0, "", "")
	if err != nil {
		return w, err
	}
	switch name {
	case "bahia_package_repository_apply":
		id, e := optionalUUIDArgStrict(args, "repository_id")
		if e != nil {
			return w, e
		}
		if id == uuid.Nil {
			id, e = uuid.Parse(intentID)
			if e != nil {
				return w, e
			}
			if existing, e := s.readStateOne(ctx, nostrpool.KindPackageRepositoryRegistry, "name", stringArg(args, "name")); e != nil {
				return w, e
			} else if existing != nil {
				id, e = uuid.Parse(stringFromRecord(existing.Fields, "id"))
				if e != nil {
					return w, e
				}
			}
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "repository-apply", id.String(), nostrpool.KindPackageRepositoryRegistry, "id", id.String()
		w.content["id"] = id.String()
		for _, key := range []string{"name", "format", "backend_ref", "backend_type", "external_repository_name", "description", "namespace_prefix", "policy", "metadata", "expected_updated_at"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
	case "bahia_package_repository_delete":
		id, e := s.packageRepositoryID(ctx, args, "repository_id", "repository_name")
		if e != nil {
			return w, e
		}
		w.op, w.coordinate, w.family, w.stateKey, w.stateValue = "repository-delete", id.String(), nostrpool.KindPackageRepositoryRegistry, "id", id.String()
		w.content = map[string]any{"repository_id": id.String(), "force": boolArg(args, "force")}
		if reason := stringArg(args, "reason"); reason != "" {
			w.content["reason"] = reason
		}
		if value, ok := args["expected_updated_at"]; ok {
			w.content["expected_updated_at"] = value
		}
	case "bahia_package_upload", "bahia_package_promote", "bahia_package_yank":
		idKey, nameKey := "repository_id", "repository_name"
		if name == "bahia_package_promote" {
			idKey, nameKey = "target_repository_id", "target_repository_name"
		}
		repoID, e := s.packageRepositoryID(ctx, args, idKey, nameKey)
		if e != nil {
			return w, e
		}
		w.family = nostrpool.KindPackageArtifactRegistry
		w.stateMatch = map[string]string{"repository_id": repoID.String()}
		for _, key := range []string{"namespace", "package_name", "version", "filename"} {
			value := strings.TrimSpace(stringArg(args, key))
			if key == "namespace" {
				value = strings.Trim(value, "/")
			}
			if value == "" && key != "namespace" {
				return w, fmt.Errorf("%s is required", key)
			}
			w.stateMatch[key] = value
		}
		w.coordinate = fmt.Sprintf("package:%s:%s:%s:%s:%s", repoID, w.stateMatch["namespace"], w.stateMatch["package_name"], w.stateMatch["version"], w.stateMatch["filename"])
		switch name {
		case "bahia_package_upload":
			w.op = "publish"
		case "bahia_package_promote":
			w.op = "promote"
		case "bahia_package_yank":
			w.op = "yank"
		}
		for _, key := range []string{"repository_id", "repository_name", "source_repository_id", "source_repository_name", "target_repository_id", "target_repository_name", "namespace", "package_name", "version", "filename", "source_url", "sha256", "size_bytes", "content_type", "environment", "channel", "policy_ref", "approval_id", "reason", "deprecated", "metadata"} {
			if value, ok := args[key]; ok {
				w.content[key] = value
			}
		}
		if name == "bahia_package_promote" {
			w.content["target_repository_id"] = repoID.String()
			if w.content["source_repository_id"] == nil {
				sourceArgs := args
				if stringArg(args, "source_repository_name") == "" {
					sourceArgs = copyIntentFields(args)
					sourceArgs["source_repository_name"] = stringArg(args, "repository_name")
				}
				if sourceID, e := s.packageRepositoryID(ctx, sourceArgs, "source_repository_id", "source_repository_name"); e == nil {
					w.content["source_repository_id"] = sourceID.String()
				} else {
					return w, e
				}
			}
		} else {
			w.content["repository_id"] = repoID.String()
		}
	case "bahia_package_drift_detect":
		repoID, e := s.packageRepositoryID(ctx, args, "repository_id", "repository_name")
		if e != nil {
			return w, e
		}
		w.op, w.coordinate = "drift-detect", "package-repository:"+repoID.String()
		w.content = map[string]any{"repository_id": repoID.String(), "include_artifacts": boolArg(args, "include_artifacts")}
	default:
		return w, fmt.Errorf("unsupported package tool %s", name)
	}
	return w, nil
}
