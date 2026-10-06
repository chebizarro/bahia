package hiveci

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
)

// Reasons a signed Hive-CI event does not parse into a run or result. They
// are the decision reasons the subscriber logs.
const (
	parseReasonInvalid       = "envelope_validation_failed"
	parseReasonContent       = "envelope_parse_failure"
	parseReasonMissingParams = "missing canonical workflow params"
)

// parseError is why a signed event is not a usable run or result.
type parseError struct {
	reason string
	err    error
}

func (e *parseError) Error() string { return e.err.Error() }
func (e *parseError) Unwrap() error { return e.err }

// parseWorkflowRunEvent derives the run a signed kind-5401 describes. It is
// the one decoding of a run: the subscriber ingests with it and the canonical
// repository reads the stored event back through it, so both see the same run.
func parseWorkflowRunEvent(ev *nostr.Event) (domain.HiveCIWorkflowRun, error) {
	var run domain.HiveCIWorkflowRun
	if ev == nil || int(ev.Kind) != kinds.HiveCIWorkflowRun {
		return run, &parseError{parseReasonInvalid, fmt.Errorf("event is not a kind-%d workflow run", kinds.HiveCIWorkflowRun)}
	}
	repoCoordinate, err := requiredTag(ev, "a")
	if err != nil {
		return run, &parseError{parseReasonInvalid, err}
	}
	var envelope struct {
		Params struct {
			Commit      string `json:"commit"`
			Branch      string `json:"branch"`
			Workflow    string `json:"workflow"`
			TriggeredBy string `json:"triggered_by"`
		} `json:"params"`
	}
	if strings.TrimSpace(ev.Content) != "" {
		if err := json.Unmarshal([]byte(ev.Content), &envelope); err != nil {
			return run, &parseError{parseReasonContent, err}
		}
	}
	commit := firstNonEmpty(optionalTag(ev, "commit"), envelope.Params.Commit)
	branch := firstNonEmpty(optionalTag(ev, "branch"), envelope.Params.Branch)
	workflow := firstNonEmpty(optionalTag(ev, "workflow"), envelope.Params.Workflow)
	triggeredBy := firstNonEmpty(optionalTag(ev, "triggered-by"), envelope.Params.TriggeredBy)
	if commit == "" || branch == "" || workflow == "" || triggeredBy == "" {
		return run, &parseError{parseReasonMissingParams, fmt.Errorf("%s", parseReasonMissingParams)}
	}
	publisher, err := requiredTag(ev, "publisher")
	if err != nil {
		return run, &parseError{parseReasonInvalid, err}
	}
	return domain.HiveCIWorkflowRun{
		RunEventID:      nostrutil.EventIDHex(ev),
		RepoCoordinate:  repoCoordinate,
		CommitSHA:       commit,
		Branch:          branch,
		WorkflowPath:    workflow,
		TriggerType:     optionalTag(ev, "trigger"),
		TriggeredBy:     triggeredBy,
		PublisherPubkey: publisher,
		EventCreatedAt:  ev.CreatedAt.Time(),
		ProcessingState: domain.HiveCIProcessingStatePendingResult,
	}, nil
}

// parseWorkflowResultEvent derives the result a signed kind-5402 describes.
// contentErr reports result content that was neither JSON nor a Loom marker;
// the signed tags still make a usable result, so it is not a parse failure.
func parseWorkflowResultEvent(ev *nostr.Event) (result domain.HiveCIWorkflowResult, contentErr, err error) {
	if ev == nil || int(ev.Kind) != kinds.HiveCIWorkflowResult {
		return result, nil, &parseError{parseReasonInvalid, fmt.Errorf("event is not a kind-%d workflow result", kinds.HiveCIWorkflowResult)}
	}
	runEventID, err := requiredTag(ev, "e")
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, err}
	}
	logURL, err := requiredTag(ev, "log_url")
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, err}
	}
	status, err := requiredTag(ev, "status")
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, err}
	}
	if status != "success" && status != "failure" {
		return result, nil, &parseError{parseReasonInvalid, fmt.Errorf("invalid Hive-CI workflow result status %q", status)}
	}
	exitCodeStr, err := requiredTag(ev, "exit_code")
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, err}
	}
	exitCode, err := strconv.Atoi(exitCodeStr)
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, fmt.Errorf("invalid Hive-CI workflow result exit_code %q", exitCodeStr)}
	}
	durationStr, err := requiredTag(ev, "duration")
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, err}
	}
	duration, err := strconv.Atoi(durationStr)
	if err != nil {
		return result, nil, &parseError{parseReasonInvalid, fmt.Errorf("invalid Hive-CI workflow result duration %q", durationStr)}
	}
	var content workflowResultContent
	if strings.TrimSpace(ev.Content) != "" {
		content, contentErr = parseWorkflowResultContent(ev.Content)
	}
	return domain.HiveCIWorkflowResult{
		ResultEventID:   nostrutil.EventIDHex(ev),
		RunEventID:      runEventID,
		Status:          status,
		ExitCode:        exitCode,
		DurationSeconds: duration,
		LogURL:          logURL,
		Error:           optionalTag(ev, "error"),
		ImageRepo:       firstNonEmpty(optionalTag(ev, "image_repo"), content.ImageRepo),
		ImageTag:        firstNonEmpty(optionalTag(ev, "image_tag"), content.ImageTag),
		ImageDigest:     firstNonEmpty(optionalTag(ev, "image_digest"), content.ImageDigest),
		PSTFGateName:    firstNonEmpty(optionalTag(ev, "pstf_gate_name"), optionalTag(ev, "gate_name"), content.PSTFGateName),
		PSTFGateStatus:  firstNonEmpty(optionalTag(ev, "pstf_gate_status"), optionalTag(ev, "gate_status"), optionalTag(ev, "pstf_status"), content.PSTFGateStatus),
		PublisherPubkey: nostrutil.EventPubKeyHex(ev),
		EventCreatedAt:  ev.CreatedAt.Time(),
	}, contentErr, nil
}
