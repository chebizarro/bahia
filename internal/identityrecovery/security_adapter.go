package identityrecovery

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strconv"
	"strings"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

// SecurityRecoveryAdapter preserves accepted fleet/org OCK ciphertext and
// keyed state_hash for targets, runs, findings/details, schedules and
// attribution edges. Production security writers pass nil service-only
// plaintext to EncryptConfidential; this adapter enforces no service_inner in
// every accepted head. The ciphertext is AEAD-bound to the same kind/d/topic,
// independent of the outer service signer, so only that signature changes.
type SecurityRecoveryAdapter struct {
	OldPubkey nostr.PubKey
	Source    EventSource
	Local     RecoveryLocalStore
	Ledger    *Ledger
}

func securityRecoveryFamily(family int) bool {
	switch family {
	case adapter.KindSecurityTargetRecord, adapter.KindSecurityRunRecord, adapter.KindSecurityFindingRecord, adapter.KindSecurityFindingDetailRecord, adapter.KindSecurityScheduleRecord, adapter.KindSecurityAttributionRecord:
		return true
	}
	return false
}

func securityRecoveryTopic(family int) string {
	switch family {
	case adapter.KindSecurityTargetRecord:
		return kinds.CPStateTopicSecurityTarget
	case adapter.KindSecurityRunRecord:
		return kinds.CPStateTopicSecurityRun
	case adapter.KindSecurityFindingRecord:
		return kinds.CPStateTopicSecurityFinding
	case adapter.KindSecurityFindingDetailRecord:
		return kinds.CPStateTopicSecurityFindingDetail
	case adapter.KindSecurityScheduleRecord:
		return kinds.CPStateTopicSecuritySchedule
	case adapter.KindSecurityAttributionRecord:
		return kinds.CPStateTopicSecurityAttribution
	}
	return ""
}

func securityRecoveryTags(old nostr.Event, family int) (nostr.Tags, error) {
	value := func(key string) (nostr.Tag, error) {
		v := tag(old, key)
		if v == "" {
			return nil, errors.New("security public tag missing: " + key)
		}
		return nostr.Tag{key, v}, nil
	}
	appendRequired := func(tags nostr.Tags, keys ...string) (nostr.Tags, error) {
		for _, key := range keys {
			v, err := value(key)
			if err != nil {
				return nil, err
			}
			tags = append(tags, v)
		}
		return tags, nil
	}
	var tags nostr.Tags
	var err error
	switch family {
	case adapter.KindSecurityTargetRecord:
		tags, err = appendRequired(tags, "target_key_hash", "target_type")
	case adapter.KindSecurityRunRecord:
		tags, err = appendRequired(tags, "run_id", "target_key_hash", "status")
		if expiration := tag(old, "expiration"); expiration != "" {
			if _, parseErr := strconv.ParseInt(expiration, 10, 64); parseErr != nil {
				return nil, errors.New("security run expiration invalid")
			}
			tags = append(tags, nostr.Tag{"expiration", expiration})
		}
		if tag(old, "deleted") == "true" && tag(old, "expiration") == "" {
			return nil, errors.New("retired security run expiration missing")
		}
	case adapter.KindSecurityFindingRecord:
		tags, err = appendRequired(tags, "osv_id", "severity", "target_key_hash")
		if runID := tag(old, "run_id"); runID != "" {
			tags = append(tags, nostr.Tag{"run_id", runID})
		}
		if cve := tag(old, "cve"); cve != "" {
			tags = append(tags, nostr.Tag{"cve", cve})
		}
	case adapter.KindSecurityFindingDetailRecord:
		if tag(old, "deleted") == "true" {
			if strings.Contains(tag(old, "d"), ":part:") {
				return nil, errors.New("security detail part tombstone unsupported")
			}
			break
		}
		tags, err = appendRequired(tags, "finding_key_hash")
		if tag(old, "manifest") == "true" {
			tags = append(tags, nostr.Tag{"manifest", "true"})
			part, partErr := value("total_parts")
			if partErr != nil {
				return nil, partErr
			}
			tags = append(tags, part)
			if osv := tag(old, "osv_id"); osv != "" {
				tags = append(tags, nostr.Tag{"osv_id", osv})
			}
		} else {
			if osv := tag(old, "osv_id"); osv != "" {
				tags = append(tags, nostr.Tag{"osv_id", osv})
			}
			if part := tag(old, "part"); part != "" {
				tags = append(tags, nostr.Tag{"part", part})
				total, totalErr := value("total_parts")
				if totalErr != nil {
					return nil, totalErr
				}
				tags = append(tags, total)
			}
		}
	case adapter.KindSecurityScheduleRecord:
		tags, err = appendRequired(tags, "target_key_hash")
		if policy := tag(old, "policy_id"); policy != "" {
			tags = append(tags, nostr.Tag{"policy_id", policy})
		}
		v, validErr := value("enabled")
		if validErr != nil {
			return nil, validErr
		}
		tags = append(tags, v)
	case adapter.KindSecurityAttributionRecord:
		tags, err = appendRequired(tags, "org", "subject_type", "subject_digest")
	}
	if err != nil {
		return nil, err
	}
	hash := tag(old, "state_hash")
	if len(hash) != 64 {
		return nil, errors.New("security keyed state hash missing")
	}
	if strings.ToLower(hash) != hash {
		return nil, errors.New("security keyed state hash invalid")
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return nil, errors.New("security keyed state hash invalid")
	}
	tags = append(tags, nostr.Tag{"state_hash", hash})
	return tags, nil
}

func validateSecurityRecoveryHead(old nostr.Event) (int, nostr.Tags, error) {
	family, err := strconv.Atoi(tag(old, "legacy_kind"))
	if err != nil || !securityRecoveryFamily(family) || old.Kind != nostr.Kind(kinds.CASControlState) {
		return 0, nil, errors.New("unsupported security recovery family")
	}
	d := tag(old, "d")
	if d == "" {
		return 0, nil, errors.New("security coordinate missing")
	}
	deletedTag := tag(old, "deleted")
	if deletedTag != "true" && deletedTag != "false" {
		return 0, nil, errors.New("security deletion marker invalid")
	}
	deleted := deletedTag == "true"
	if deleted && family != adapter.KindSecurityRunRecord && family != adapter.KindSecurityFindingDetailRecord && family != adapter.KindSecurityAttributionRecord {
		return 0, nil, errors.New("security family has no tombstone publisher")
	}
	extra, err := securityRecoveryTags(old, family)
	if err != nil {
		return 0, nil, err
	}
	kind, base := adapter.ControlStateEnvelope(family, d, deleted)
	if kind != int(old.Kind) || !reflect.DeepEqual(old.Tags, append(base, extra...)) {
		return 0, nil, errors.New("typed security public envelope cannot reproduce accepted head")
	}
	switch family {
	case adapter.KindSecurityTargetRecord:
		if d != "security:target:"+tag(old, "target_key_hash") {
			return 0, nil, errors.New("security target coordinate mismatch")
		}
	case adapter.KindSecurityRunRecord:
		if d != "security:run:"+tag(old, "run_id") {
			return 0, nil, errors.New("security run coordinate mismatch")
		}
	case adapter.KindSecurityFindingRecord:
		if !strings.HasPrefix(d, "security:finding:") {
			return 0, nil, errors.New("security finding coordinate invalid")
		}
	case adapter.KindSecurityFindingDetailRecord:
		base := "security:finding-detail:" + tag(old, "finding_key_hash")
		if deleted {
			base = d
		}
		if part := tag(old, "part"); part != "" {
			base += ":part:" + part
		}
		if !strings.HasPrefix(d, "security:finding-detail:") || d != base {
			return 0, nil, errors.New("security finding-detail coordinate invalid")
		}
	case adapter.KindSecurityScheduleRecord:
		if !strings.HasPrefix(d, "security:schedule:") {
			return 0, nil, errors.New("security schedule coordinate invalid")
		}
	case adapter.KindSecurityAttributionRecord:
		if d != "security:attribution:"+tag(old, "org")+":"+tag(old, "subject_type")+":"+tag(old, "subject_digest") {
			return 0, nil, errors.New("security attribution coordinate mismatch")
		}
		org, parseErr := uuid.Parse(tag(old, "org"))
		if parseErr != nil || domain.ValidateSecurityAttribution(&domain.SecurityAttribution{OrgID: org, SubjectType: tag(old, "subject_type"), SubjectDigest: tag(old, "subject_digest"), Visible: !deleted}) != nil {
			return 0, nil, errors.New("security attribution tags invalid")
		}
	}
	envelope, err := controlplane.ParseConfidentialEnvelope(old.Content)
	if err != nil {
		return 0, nil, errors.New("security OCK envelope invalid")
	}
	if envelope.Algorithm != controlplane.ConfidentialAlgorithm || envelope.ServiceInner != "" || envelope.KeyOrg == "" || envelope.KeyRef == "" || envelope.KeyVersion == "" || envelope.Ciphertext == "" {
		return 0, nil, errors.New("security OCK envelope or service_inner invalid")
	}
	if family == adapter.KindSecurityAttributionRecord && envelope.KeyOrg != tag(old, "org") || family != adapter.KindSecurityAttributionRecord && envelope.KeyOrg != kinds.FleetOCKScope {
		return 0, nil, errors.New("security OCK scope mismatch")
	}
	ad := envelope.AssociatedData
	if ad["schema"] != envelope.Schema || ad["key_org"] != envelope.KeyOrg || ad["key_ref"] != envelope.KeyRef || ad["key_version"] != envelope.KeyVersion || ad["legacy_kind"] != strconv.Itoa(family) || ad["d"] != d || ad["t"] != securityRecoveryTopic(family) {
		return 0, nil, errors.New("security OCK associated-data coordinate mismatch")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != 24 {
		return 0, nil, errors.New("security OCK nonce invalid")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) < 16 {
		return 0, nil, errors.New("security OCK ciphertext invalid")
	}
	return family, extra, nil
}

func (a *SecurityRecoveryAdapter) core() canonicalHeadAdapter {
	return canonicalHeadAdapter{
		old: a.OldPubkey, source: a.Source, local: a.Local, ledger: a.Ledger,
		kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, name: "security-canonical",
		belongs: func(ev nostr.Event) bool {
			family, _ := strconv.Atoi(tag(ev, "legacy_kind"))
			return securityRecoveryFamily(family) || strings.HasPrefix(tag(ev, "d"), "security:")
		},
		preflight: func(ev nostr.Event) error { _, _, err := validateSecurityRecoveryHead(ev); return err },
		publish: func(ctx context.Context, signer nostr.Keyer, old nostr.Event, sink registryRecoverySink) error {
			family, extra, err := validateSecurityRecoveryHead(old)
			if err != nil {
				return err
			}
			pub, err := signer.GetPublicKey(ctx)
			if err != nil {
				return err
			}
			projector := adapter.NewProjector(config.NostrConfig{PublishEnabled: true}, nil, sink, nil, zap.NewNop(), adapter.WithProjectorSigner(signer, pub.Hex()))
			publisher := adapter.NewSecurityCanonicalPublisher(projector, nil, zap.NewNop())
			return publisher.PublishRecoveredCiphertext(ctx, family, tag(old, "d"), tag(old, "deleted") == "true", extra, old.Content, sink)
		},
	}
}

func (a *SecurityRecoveryAdapter) Inventory(ctx context.Context) ([]string, error) {
	return a.core().Inventory(ctx)
}
func (a *SecurityRecoveryAdapter) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	return a.core().Reassert(ctx, signer)
}
func (a *SecurityRecoveryAdapter) Verify(ctx context.Context) ([]string, error) {
	return a.core().Verify(ctx)
}
