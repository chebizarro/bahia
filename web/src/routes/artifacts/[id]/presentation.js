export function artifactVersionLabel(artifact, displayName) {
  const candidates = [artifact?.version, artifact?.image_tag, artifact?.tag, artifact?.metadata?.version];
  for (const candidate of candidates) {
    const value = String(candidate || '').trim();
    if (value && value !== String(displayName || '').trim()) return value;
  }
  return '-';
}

function signingError(message) {
  return /NIP-0?7|NIP-46|signer.first|signing extension|signer unavailable/i.test(message);
}

export function userFacingSBOMError(err) {
  const message = String(err?.message || '').trim();
  if (signingError(message)) return 'Connect a signing extension and try again.';
  if (/ContextVM|method not found|NIP-\d+|\bkind \d+\b|\bintent\b/i.test(message)) {
    return 'Could not start the SBOM request. Please try again or contact your administrator.';
  }
  return message || 'Could not start the SBOM request.';
}

export function userFacingVerifyError(err) {
  const message = String(err?.message || '').trim();
  if (signingError(message)) return 'Connect a signing extension and try again.';
  if (/method not found|not configured|ContextVM|NIP-\d+|\bkind \d+\b|\bintent\b/i.test(message)) {
    return 'Signature verification is unavailable. Please contact your administrator.';
  }
  return message || 'Failed to verify signatures.';
}
