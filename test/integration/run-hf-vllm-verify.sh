#!/usr/bin/env bash
# =============================================================================
# AI Fabric HF/vLLM Production Verification Runner
# =============================================================================
#
# Usage:
#   ./test/integration/run-hf-vllm-verify.sh [--env FILE] [--dry-run] [--verbose]
#
# Loads prerequisites from .env.integration, validates all required values are
# present, then runs TestAIHFVLLMProductionIntegrations. Results are recorded
# in pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT/verification_report.md.
#
# =============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

ENV_FILE="$SCRIPT_DIR/.env.integration"
DRY_RUN=false
VERBOSE=""
TEST_TIMEOUT="60s"

# --- Parse args ---
while [[ $# -gt 0 ]]; do
  case $1 in
    --env)       ENV_FILE="$2"; shift 2 ;;
    --dry-run)   DRY_RUN=true; shift ;;
    --verbose)   VERBOSE="-v"; shift ;;
    --timeout)   TEST_TIMEOUT="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: $0 [--env FILE] [--dry-run] [--verbose] [--timeout DURATION]"
      echo ""
      echo "Options:"
      echo "  --env FILE     Path to .env.integration file (default: test/integration/.env.integration)"
      echo "  --dry-run      Validate prerequisites without running the test"
      echo "  --verbose      Pass -v to go test"
      echo "  --timeout      Go test timeout (default: 60s)"
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

# --- Load env file ---
if [[ ! -f "$ENV_FILE" ]]; then
  echo "❌ Env file not found: $ENV_FILE"
  echo ""
  echo "Copy the template and fill in your production values:"
  echo "  cp test/integration/.env.integration.example test/integration/.env.integration"
  echo "  \$EDITOR test/integration/.env.integration"
  exit 1
fi

echo "📋 Loading prerequisites from $ENV_FILE"
set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

# --- Required env vars ---
REQUIRED_VARS=(
  BAHIA_HF_ARTIFACT_URL
  BAHIA_HF_ARTIFACT_SHA256
  BAHIA_VLLM_BASE_URL
  BAHIA_ML_EXPECTED_MODEL_ID
  BAHIA_ML_GATEWAY_MODELS_URL
  BAHIA_ML_OCI_MANIFEST_URL
  BAHIA_ML_OCI_ARTIFACT_SHA256
  BAHIA_ML_BLOSSOM_ARTIFACT_URL
  BAHIA_ML_BLOSSOM_ARTIFACT_SHA256
  BAHIA_ML_RELAY_URLS
  BAHIA_ML_RELAY_PRIVATE_KEY
)

SHA256_VARS=(
  BAHIA_HF_ARTIFACT_SHA256
  BAHIA_ML_OCI_ARTIFACT_SHA256
  BAHIA_ML_BLOSSOM_ARTIFACT_SHA256
)

MISSING=()
INVALID=()

for var in "${REQUIRED_VARS[@]}"; do
  val="${!var:-}"
  if [[ -z "$val" ]]; then
    MISSING+=("$var")
  fi
done

for var in "${SHA256_VARS[@]}"; do
  val="${!var:-}"
  # Strip optional sha256: prefix, validate 64-char hex
  val="${val#sha256:}"
  val="${val,,}" # lowercase
  if [[ -n "$val" ]] && ! [[ "$val" =~ ^[0-9a-f]{64}$ ]]; then
    INVALID+=("$var (must be 64-char hex SHA-256, got: ${!var:-})")
  fi
done

# --- Report ---
echo ""
echo "=== Prerequisite Check ==="
echo ""

for var in "${REQUIRED_VARS[@]}"; do
  val="${!var:-}"
  if [[ -z "$val" ]]; then
    echo "  ❌ $var — MISSING"
  elif [[ "$var" == *"PRIVATE_KEY"* ]] || [[ "$var" == *"TOKEN"* ]] || [[ "$var" == *"API_KEY"* ]]; then
    echo "  ✅ $var — (redacted)"
  elif [[ "$var" == *"SHA256"* ]]; then
    echo "  ✅ $var — ${val:0:16}..."
  else
    echo "  ✅ $var — $val"
  fi
done

# Check optional auth tokens
for var in BAHIA_HF_TOKEN BAHIA_VLLM_API_KEY BAHIA_ML_GATEWAY_TOKEN BAHIA_ML_OCI_TOKEN BAHIA_ML_BLOSSOM_TOKEN; do
  val="${!var:-}"
  if [[ -n "$val" ]]; then
    echo "  ✅ $var — (redacted, optional)"
  else
    echo "  ⬚  $var — not set (optional)"
  fi
done

echo ""

if [[ ${#MISSING[@]} -gt 0 ]]; then
  echo "❌ Missing ${#MISSING[@]} required prerequisite(s):"
  for var in "${MISSING[@]}"; do
    echo "   - $var"
  done
  echo ""
  echo "Fill in all required values in: $ENV_FILE"
  exit 1
fi

if [[ ${#INVALID[@]} -gt 0 ]]; then
  echo "❌ Invalid SHA-256 format:"
  for msg in "${INVALID[@]}"; do
    echo "   - $msg"
  done
  echo ""
  echo "SHA-256 values must be 64-char lowercase hex. Compute with:"
  echo "  curl -sL <URL> | sha256sum | cut -d' ' -f1"
  exit 1
fi

echo "✅ All ${#REQUIRED_VARS[@]} prerequisites present"
echo ""

if $DRY_RUN; then
  echo "🔍 Dry run — skipping test execution"
  echo ""
  echo "To run the actual test:"
  echo "  $0 --env $ENV_FILE $VERBOSE"
  exit 0
fi

# --- Run the test ---
export BAHIA_HF_VLLM_PROD_VERIFY=1

echo "🚀 Running TestAIHFVLLMProductionIntegrations..."
echo ""

cd "$PROJECT_ROOT"

TEST_OUTPUT=$(mktemp)
TEST_EXIT=0

go test -tags=integration \
  ./test/integration/ml_hf_vllm_production_verification_test.go \
  -run TestAIHFVLLMProductionIntegrations \
  -count=1 \
  -timeout "$TEST_TIMEOUT" \
  $VERBOSE 2>&1 | tee "$TEST_OUTPUT" || TEST_EXIT=$?

echo ""

if [[ $TEST_EXIT -eq 0 ]]; then
  echo "✅ PASS — Production verification succeeded"
  RESULT="PASS"
else
  echo "❌ FAIL — Production verification failed (exit $TEST_EXIT)"
  RESULT="FAIL"
fi

# --- Record evidence ---
PSTF_DIR="$PROJECT_ROOT/pstf/features/AI_FABRIC_HF_VLLM_DEPLOYMENT"
EVIDENCE_FILE="$PSTF_DIR/production_evidence.md"

mkdir -p "$PSTF_DIR"

cat > "$EVIDENCE_FILE" <<EVIDENCE
# Production Verification Evidence — AI_FABRIC_HF_VLLM_DEPLOYMENT

## Result: $RESULT
## Date: $(date -u +"%Y-%m-%dT%H:%M:%SZ")
## Bead: bahia-wiw9

## Prerequisites Used

| Variable | Value |
|----------|-------|
| BAHIA_HF_ARTIFACT_URL | $BAHIA_HF_ARTIFACT_URL |
| BAHIA_HF_ARTIFACT_SHA256 | ${BAHIA_HF_ARTIFACT_SHA256:0:16}... |
| BAHIA_VLLM_BASE_URL | $BAHIA_VLLM_BASE_URL |
| BAHIA_ML_EXPECTED_MODEL_ID | $BAHIA_ML_EXPECTED_MODEL_ID |
| BAHIA_ML_GATEWAY_MODELS_URL | $BAHIA_ML_GATEWAY_MODELS_URL |
| BAHIA_ML_OCI_MANIFEST_URL | $BAHIA_ML_OCI_MANIFEST_URL |
| BAHIA_ML_OCI_ARTIFACT_SHA256 | ${BAHIA_ML_OCI_ARTIFACT_SHA256:0:16}... |
| BAHIA_ML_BLOSSOM_ARTIFACT_URL | $BAHIA_ML_BLOSSOM_ARTIFACT_URL |
| BAHIA_ML_BLOSSOM_ARTIFACT_SHA256 | ${BAHIA_ML_BLOSSOM_ARTIFACT_SHA256:0:16}... |
| BAHIA_ML_RELAY_URLS | $BAHIA_ML_RELAY_URLS |
| BAHIA_ML_RELAY_PRIVATE_KEY | (redacted) |
| BAHIA_HF_TOKEN | ${BAHIA_HF_TOKEN:+(set)} ${BAHIA_HF_TOKEN:-(unset)} |
| BAHIA_VLLM_API_KEY | ${BAHIA_VLLM_API_KEY:+(set)} ${BAHIA_VLLM_API_KEY:-(unset)} |
| BAHIA_ML_GATEWAY_TOKEN | ${BAHIA_ML_GATEWAY_TOKEN:+(set)} ${BAHIA_ML_GATEWAY_TOKEN:-(unset)} |
| BAHIA_ML_OCI_TOKEN | ${BAHIA_ML_OCI_TOKEN:+(set)} ${BAHIA_ML_OCI_TOKEN:-(unset)} |
| BAHIA_ML_BLOSSOM_TOKEN | ${BAHIA_ML_BLOSSOM_TOKEN:+(set)} ${BAHIA_ML_BLOSSOM_TOKEN:-(unset)} |

## Test Output

\`\`\`
$(cat "$TEST_OUTPUT")
\`\`\`

## Command

\`\`\`bash
BAHIA_HF_VLLM_PROD_VERIFY=1 go test -tags=integration \\
  ./test/integration/ml_hf_vllm_production_verification_test.go \\
  -run TestAIHFVLLMProductionIntegrations -count=1 -timeout $TEST_TIMEOUT $VERBOSE
\`\`\`
EVIDENCE

echo ""
echo "📝 Evidence recorded in: $EVIDENCE_FILE"
rm -f "$TEST_OUTPUT"

exit $TEST_EXIT
