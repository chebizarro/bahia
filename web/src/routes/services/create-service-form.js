export function buildArtifactRepo({ selectedRegistry, repoPath, availableRegistries }) {
  const trimmedPath = (repoPath || '').trim();
  if (!trimmedPath) return '';
  if (selectedRegistry === 'custom') return trimmedPath;

  const registry = (availableRegistries || []).find((r) => r.id === selectedRegistry);
  if (!registry?.base_url) return trimmedPath;

  return `${registry.base_url}/${trimmedPath}`;
}

export function validateCreateServiceForm({ name, artifactRepo, runtimeType }) {
  if (!(name || '').trim()) return 'Name is required';
  if (!(artifactRepo || '').trim()) return 'Artifact repository is required';
  if (!runtimeType) return 'Runtime type is required';
  return null;
}

export function buildCreateServicePayload(form) {
  return {
    name: (form.name || '').trim(),
    repo_url: form.repositorySelection?.repoUrl || '',
    artifact_repo: (form.artifact_repo || '').trim(),
    runtime_type: form.runtime_type,
    default_branch: (form.default_branch || '').trim() || 'main'
  };
}
