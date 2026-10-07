// Command bahia-assistant-e2e-provider is the deterministic OpenAI-compatible
// model provider behind the joined assistant Playwright harness
// (web/tests/e2e/harnesses/assistant-joined.js). It speaks the same
// /v1/chat/completions wire format the production ChatClient (batch planner)
// and OpenAIAgentClient (iterative loop) use, and answers only the two
// fixture prompts it publishes on GET /fixtures:
//
//   - the batch prompt always yields the same three-step read-only plan over
//     the backend's real bahia_assistant_dns_* tools;
//   - the iterative prompt is held open until the caller goes away, so the
//     run stays in `proposing` until the operator cancels it.
//
// Any other request is refused so an unexpected model call fails loudly
// instead of being answered with invented content.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/strutil"
)

const (
	// BatchPrompt is the operator prompt answered with BatchPlan.
	BatchPrompt = "joined-e2e batch: inspect DNS endpoints and drift"
	// IterativePrompt is the operator prompt whose model turn never completes.
	IterativePrompt = "joined-e2e iterative: keep proposing until cancelled"
)

// BatchPlan is the deterministic batch proposal: exactly three steps, each a
// read-only assistant tool that succeeds against an empty DNS read model.
func BatchPlan() domain.AssistantPlan {
	return domain.AssistantPlan{
		Summary:   "List DNS endpoints, list DNS drift, then list endpoints again.",
		RiskLevel: "low",
		Steps: []domain.AssistantPlanStep{
			{StepID: "step-1", Title: "List DNS endpoints", Description: "Read the DNS endpoint projection.", ToolName: "bahia_assistant_dns_list_endpoints", ToolArgs: map[string]any{"limit": 10, "offset": 0}},
			{StepID: "step-2", Title: "List DNS drift", Description: "Read endpoints whose DNS state drifted.", ToolName: "bahia_assistant_dns_list_drift", ToolArgs: map[string]any{"limit": 10, "offset": 0}},
			{StepID: "step-3", Title: "Re-list DNS endpoints", Description: "Confirm the endpoint projection is unchanged.", ToolName: "bahia_assistant_dns_list_endpoints", ToolArgs: map[string]any{"limit": 5, "offset": 0}},
		},
	}
}

// EditedFirstStepArgs are valid replacement tool_args for the plan's first step.
const EditedFirstStepArgs = `{"limit":3,"offset":0}`

// Fixtures is the contract the harness passes to the Playwright spec.
type Fixtures struct {
	BatchPrompt         string `json:"batch_prompt"`
	IterativePrompt     string `json:"iterative_prompt"`
	EditedFirstStepArgs string `json:"edited_first_step_args_json"`
	BatchStepCount      int    `json:"batch_step_count"`
}

type provider struct {
	mu     sync.Mutex
	counts map[string]int
	// held, when set, receives one value each time an iterative turn is held.
	held chan<- struct{}
}

func newProvider() *provider { return &provider{counts: map[string]int{}} }

func (p *provider) count(key string) {
	p.mu.Lock()
	p.counts[key]++
	p.mu.Unlock()
}

func (p *provider) snapshot() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.counts))
	for k, v := range p.counts {
		out[k] = v
	}
	return out
}

func (p *provider) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /fixtures", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, Fixtures{BatchPrompt: BatchPrompt, IterativePrompt: IterativePrompt, EditedFirstStepArgs: EditedFirstStepArgs, BatchStepCount: len(BatchPlan().Steps)})
	})
	mux.HandleFunc("GET /requests", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, p.snapshot())
	})
	mux.HandleFunc("POST /v1/chat/completions", p.chatCompletions)
	return mux
}

func (p *provider) chatCompletions(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Stream         bool              `json:"stream"`
		ResponseFormat json.RawMessage   `json:"response_format"`
		Messages       []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	userText := userMessages(body.Messages)
	switch {
	case len(body.ResponseFormat) > 0 && strings.Contains(userText, BatchPrompt):
		if body.Stream {
			p.count("batch_refused_stream")
			http.Error(w, "fixture provider answers the batch planner without streaming", http.StatusBadRequest)
			return
		}
		p.count("batch")
		plan, err := json.Marshal(BatchPlan())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("batch plan served")
		writeJSON(w, http.StatusOK, completion(map[string]any{"role": "assistant", "content": string(plan)}, "stop"))
	case len(body.ResponseFormat) == 0 && strings.Contains(userText, IterativePrompt):
		// Hold the model turn open: the run stays `proposing` until the
		// assistant cancels it (or shuts down), which closes this request.
		p.count("iterative_held")
		log.Printf("iterative turn held open")
		if p.held != nil {
			p.held <- struct{}{}
		}
		<-r.Context().Done()
		p.count("iterative_released")
		log.Printf("iterative turn released: %v", r.Context().Err())
	default:
		p.count("refused")
		log.Printf("refused unscripted model request (planner=%t)", len(body.ResponseFormat) > 0)
		http.Error(w, "no fixture for this prompt", http.StatusUnprocessableEntity)
	}
}

// userMessages concatenates the text of user-role messages; content may be a
// string or an array of OpenAI content parts.
func userMessages(messages []json.RawMessage) string {
	var b strings.Builder
	for _, raw := range messages {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Role != "user" {
			continue
		}
		var text string
		if json.Unmarshal(m.Content, &text) == nil {
			b.WriteString(text)
			b.WriteByte('\n')
			continue
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, part := range parts {
				b.WriteString(part.Text)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

func completion(message map[string]any, finish string) map[string]any {
	return map[string]any{
		"id":      "joined-e2e",
		"object":  "chat.completion",
		"model":   "joined-e2e",
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func main() {
	addr := flag.String("addr", strutil.Env("BAHIA_ASSISTANT_E2E_PROVIDER_ADDR", "127.0.0.1:0"), "HTTP listen address")
	flag.Parse()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen on %s: %v", *addr, err)
	}
	// The harness waits for this exact line before configuring the backend.
	fmt.Printf("bahia assistant e2e provider listening on %s\n", listener.Addr().String())
	if err := http.Serve(listener, newProvider().routes()); err != nil {
		log.Fatal(err)
	}
}
