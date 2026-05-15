package soulfactory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

type VoiceRuntimeControlDriver struct {
	Backend OpenClawControlDriver
	Service VoicePersonaService
	Now     func() time.Time
}

func (d *VoiceRuntimeControlDriver) Methods() []string {
	methods := []string{RuntimeMethodVoiceConfigure, RuntimeMethodVoicePreview, RuntimeMethodVoiceSample, RuntimeMethodVoiceList}
	if d != nil && d.Backend != nil {
		methods = append(methods, d.Backend.Methods()...)
	}
	return uniqueStrings(methods)
}

func (d *VoiceRuntimeControlDriver) Execute(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	switch invocation.Method {
	case RuntimeMethodVoiceConfigure:
		return d.configure(ctx, invocation)
	case RuntimeMethodVoicePreview, RuntimeMethodVoiceSample:
		return d.preview(ctx, invocation)
	case RuntimeMethodVoiceList:
		return d.list(ctx, invocation)
	default:
		if d != nil && d.Backend != nil {
			return d.Backend.Execute(ctx, invocation)
		}
		return nil, fmt.Errorf("unsupported OpenClaw method %s", invocation.Method)
	}
}

func (d *VoiceRuntimeControlDriver) configure(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	params, err := d.configureParams(ctx, invocation.Params)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{
		"agent_id":         invocation.AgentID,
		"soul_id":          invocation.SoulID,
		"state":            "voice_configured",
		"voice_config":     params,
		"read_model_patch": map[string]interface{}{"voice": params},
		"hot_reload":       true,
		"observed_at":      d.now().Unix(),
	}
	if provider, ok := params["provider"]; ok {
		out["provider"] = provider
	}
	if personaID, ok := params["persona_id"]; ok {
		out["persona_id"] = personaID
	}
	return &OpenClawControlOutcome{Status: "success", Result: out}, nil
}

func (d *VoiceRuntimeControlDriver) preview(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	params, err := d.previewParams(ctx, invocation.Params)
	if err != nil {
		return nil, err
	}
	sampleText, _ := params["sample_text"].(string)
	ref := voiceSampleRef(invocation.AgentID, sampleText, params)
	out := map[string]interface{}{
		"agent_id":         invocation.AgentID,
		"soul_id":          invocation.SoulID,
		"state":            "voice_preview_ready",
		"sample_audio_ref": ref,
		"preview_ref":      ref,
		"sample_text":      sampleText,
		"voice_config":     params,
		"observed_at":      d.now().Unix(),
	}
	if provider, ok := params["provider"]; ok {
		out["provider"] = provider
	}
	if personaID, ok := params["persona_id"]; ok {
		out["persona_id"] = personaID
	}
	return &OpenClawControlOutcome{Status: "success", Result: out}, nil
}

func (d *VoiceRuntimeControlDriver) list(ctx context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	registry := d.service().registry
	providers, err := registry.AllCapabilities(ctx)
	if err != nil {
		return nil, err
	}
	return &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{
		"agent_id":  invocation.AgentID,
		"soul_id":   invocation.SoulID,
		"providers": providers,
		"voices":    providers,
	}}, nil
}

func (d *VoiceRuntimeControlDriver) configureParams(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	if openclaw, ok := params["openclaw"].(map[string]interface{}); ok {
		return map[string]interface{}{"schema": VoicePersonaRuntimeParamsSchema, "openclaw": openclaw}, nil
	}
	if voiceConfig, ok := params["voice_config"].(map[string]interface{}); ok {
		return voiceConfig, nil
	}
	spec, err := voiceSpecFromRuntimeParams(params)
	if err != nil {
		return nil, err
	}
	return d.service().BuildConfigureRuntimeParams(ctx, spec)
}

func (d *VoiceRuntimeControlDriver) previewParams(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	spec, err := voiceSpecFromRuntimeParams(params)
	if err == nil {
		return d.service().BuildSampleRuntimeParams(ctx, spec)
	}
	configured, cfgErr := d.configureParams(ctx, params)
	if cfgErr != nil {
		return nil, err
	}
	if _, ok := configured["sample_text"]; !ok {
		configured["sample_text"] = DefaultVoiceSampleText
	}
	return configured, nil
}

func (d *VoiceRuntimeControlDriver) service() VoicePersonaService {
	if d != nil && d.Service.registry != nil {
		return d.Service
	}
	return NewVoicePersonaService(nil)
}

func (d *VoiceRuntimeControlDriver) now() time.Time {
	if d != nil && d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func voiceSpecFromRuntimeParams(params map[string]interface{}) (domain.SoulVoiceSpec, error) {
	if params == nil {
		params = map[string]interface{}{}
	}
	value := any(params["voice"])
	if value == nil {
		if proposed, ok := params["proposed"].(map[string]interface{}); ok {
			value = proposed["voice"]
		}
	}
	if value == nil {
		value = params
	}
	data, err := json.Marshal(value)
	if err != nil {
		return domain.SoulVoiceSpec{}, fmt.Errorf("marshal voice runtime params: %w", err)
	}
	var spec domain.SoulVoiceSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		return domain.SoulVoiceSpec{}, fmt.Errorf("parse voice runtime params: %w", err)
	}
	return spec, nil
}

func voiceSampleRef(agentID, sampleText string, params map[string]interface{}) string {
	data, _ := json.Marshal(params)
	digest := sha256.Sum256(append([]byte(agentID+"\x00"+sampleText+"\x00"), data...))
	return "openclaw://agents/" + agentID + "/voice-samples/" + hex.EncodeToString(digest[:])[:16]
}
