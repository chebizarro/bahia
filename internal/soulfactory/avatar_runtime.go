package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/adapters/llm"
	"github.com/openagentsinc/bahia/internal/domain"
)

// AvatarRuntimeGenerator is the avatar generation surface used by runtime
// control handlers. *llm.AvatarGenerator satisfies this interface.
type AvatarRuntimeGenerator interface {
	GenerateWithSpec(context.Context, domain.SoulAvatarGenerationSpec, llm.AvatarProgressFunc) (*llm.AvatarResult, error)
	ProviderInfos() []llm.AvatarProviderInfo
}

// AvatarRuntimeStore persists generated avatar bytes and returns the ref that
// SoulFactory read models should store. *blossom.Client satisfies this interface.
type AvatarRuntimeStore interface {
	StoreAvatar(context.Context, []byte, string, string) (*blossom.AvatarStoreResult, error)
}

type AvatarRuntimeControlDriver struct {
	Backend   OpenClawControlDriver
	Generator AvatarRuntimeGenerator
	Store     AvatarRuntimeStore
	Now       func() time.Time

	mu     sync.Mutex
	status map[string]map[string]interface{}
}

func (d *AvatarRuntimeControlDriver) Methods() []string {
	methods := []string{RuntimeMethodAvatarGenerate, RuntimeMethodAvatarSet, RuntimeMethodAvatarList, RuntimeMethodAvatarStatus}
	if d != nil && d.Backend != nil {
		methods = append(methods, d.Backend.Methods()...)
	}
	return uniqueStrings(methods)
}

func (d *AvatarRuntimeControlDriver) Execute(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	switch invocation.Method {
	case RuntimeMethodAvatarGenerate:
		return d.generate(ctx, invocation)
	case RuntimeMethodAvatarSet:
		return d.set(invocation)
	case RuntimeMethodAvatarList:
		return d.list(invocation), nil
	case RuntimeMethodAvatarStatus:
		return d.getStatus(invocation), nil
	default:
		if d != nil && d.Backend != nil {
			return d.Backend.Execute(ctx, invocation)
		}
		return nil, fmt.Errorf("unsupported OpenClaw method %s", invocation.Method)
	}
}

func (d *AvatarRuntimeControlDriver) generate(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	if d == nil || d.Generator == nil {
		return nil, fmt.Errorf("avatar generator is not configured")
	}
	spec, err := avatarGenerationSpecFromParams(invocation.Params)
	if err != nil {
		return nil, err
	}
	progress := []map[string]interface{}{}
	result, err := d.Generator.GenerateWithSpec(ctx, spec, func(event llm.AvatarProgressEvent) {
		progress = append(progress, avatarProgressMap(event))
	})
	if err != nil {
		status := map[string]interface{}{
			"agent_id":        invocation.AgentID,
			"soul_id":         invocation.SoulID,
			"state":           "failed",
			"progress_events": progress,
			"error":           err.Error(),
			"observed_at":     d.now().Unix(),
		}
		d.remember(invocation, status)
		return &OpenClawControlOutcome{Status: "failed", Result: status, Error: &RuntimeControlError{Code: "avatar_generation_failed", Message: err.Error(), Retryable: true}}, nil
	}

	stored, err := d.storeAvatar(ctx, result)
	if err != nil {
		status := map[string]interface{}{
			"agent_id":        invocation.AgentID,
			"soul_id":         invocation.SoulID,
			"state":           "failed",
			"progress_events": progress,
			"error":           err.Error(),
			"observed_at":     d.now().Unix(),
		}
		d.remember(invocation, status)
		return &OpenClawControlOutcome{Status: "failed", Result: status, Error: &RuntimeControlError{Code: "avatar_store_failed", Message: err.Error(), Retryable: true}}, nil
	}

	avatar := map[string]interface{}{
		"generation":    spec,
		"generated_ref": stored.Ref,
		"current":       "generated",
	}
	out := map[string]interface{}{
		"agent_id":         invocation.AgentID,
		"soul_id":          invocation.SoulID,
		"state":            "completed",
		"avatar_ref":       stored.Ref,
		"avatar":           avatar,
		"read_model_patch": map[string]interface{}{"assets": map[string]interface{}{"avatar_ref": stored.Ref}, "avatar": avatar},
		"provider":         result.Provider,
		"seed":             result.Seed,
		"content_type":     stored.ContentType,
		"size":             stored.Size,
		"fallback":         stored.Fallback,
		"progress_events":  progress,
		"observed_at":      d.now().Unix(),
	}
	d.remember(invocation, out)
	return &OpenClawControlOutcome{Status: "success", Result: out}, nil
}

func (d *AvatarRuntimeControlDriver) set(invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	ref, _ := invocation.Params["avatar_ref"].(string)
	avatar := map[string]interface{}{"current": "uploaded", "uploaded_ref": ref}
	out := map[string]interface{}{
		"agent_id":         invocation.AgentID,
		"soul_id":          invocation.SoulID,
		"state":            "completed",
		"avatar_ref":       ref,
		"avatar":           avatar,
		"read_model_patch": map[string]interface{}{"assets": map[string]interface{}{"avatar_ref": ref}, "avatar": avatar},
		"progress_events":  []map[string]interface{}{{"stage": "completed", "percent": 100, "message": "avatar ref set"}},
		"observed_at":      d.now().Unix(),
	}
	d.remember(invocation, out)
	return &OpenClawControlOutcome{Status: "success", Result: out}, nil
}

func (d *AvatarRuntimeControlDriver) list(invocation OpenClawControlInvocation) *OpenClawControlOutcome {
	providers := []llm.AvatarProviderInfo{}
	if d != nil && d.Generator != nil {
		providers = d.Generator.ProviderInfos()
	}
	return &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{
		"agent_id":      invocation.AgentID,
		"providers":     providers,
		"style_presets": llm.AvatarStylePresets(),
	}}
}

func (d *AvatarRuntimeControlDriver) getStatus(invocation OpenClawControlInvocation) *OpenClawControlOutcome {
	key := avatarStatusKey(invocation)
	d.mu.Lock()
	status := d.status[key]
	d.mu.Unlock()
	if status == nil {
		status = map[string]interface{}{"agent_id": invocation.AgentID, "soul_id": invocation.SoulID, "state": "idle", "progress_events": []map[string]interface{}{}}
	}
	return &OpenClawControlOutcome{Status: "success", Result: status}
}

func (d *AvatarRuntimeControlDriver) storeAvatar(ctx context.Context, result *llm.AvatarResult) (*blossom.AvatarStoreResult, error) {
	if result == nil {
		return nil, fmt.Errorf("avatar generation returned no result")
	}
	if d.Store != nil {
		return d.Store.StoreAvatar(ctx, result.ImageData, result.ContentType, result.SourceURL)
	}
	if result.SourceURL != "" {
		return (*blossom.Client)(nil).StoreAvatar(ctx, result.ImageData, result.ContentType, result.SourceURL)
	}
	return nil, fmt.Errorf("avatar storage is not configured")
}

func (d *AvatarRuntimeControlDriver) remember(invocation OpenClawControlInvocation, status map[string]interface{}) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.status == nil {
		d.status = map[string]map[string]interface{}{}
	}
	d.status[avatarStatusKey(invocation)] = status
}

func (d *AvatarRuntimeControlDriver) now() time.Time {
	if d != nil && d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func avatarGenerationSpecFromParams(params map[string]interface{}) (domain.SoulAvatarGenerationSpec, error) {
	if params == nil {
		params = map[string]interface{}{}
	}
	value := params["generation"]
	if value == nil {
		value = params
	}
	data, err := json.Marshal(value)
	if err != nil {
		return domain.SoulAvatarGenerationSpec{}, fmt.Errorf("marshal avatar generation params: %w", err)
	}
	var spec domain.SoulAvatarGenerationSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return domain.SoulAvatarGenerationSpec{}, fmt.Errorf("parse avatar generation params: %w", err)
	}
	return spec, nil
}

func avatarProgressMap(event llm.AvatarProgressEvent) map[string]interface{} {
	out := map[string]interface{}{"provider": event.Provider, "stage": string(event.Stage), "message": event.Message, "percent": event.Percent}
	if event.Error != "" {
		out["error"] = event.Error
	}
	if event.Result != nil {
		out["seed"] = event.Result.Seed
		out["content_type"] = event.Result.ContentType
	}
	return out
}

func avatarStatusKey(invocation OpenClawControlInvocation) string {
	return invocation.SoulID + "\x00" + invocation.AgentID
}
