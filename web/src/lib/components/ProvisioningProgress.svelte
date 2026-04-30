<script>
  let {
    run = null,
    onComplete = null
  } = $props();

  const steps = [
    { id: 'generate', label: 'Generate Soul', icon: '🧠' },
    { id: 'signet', label: 'Create Identity', icon: '🔐' },
    { id: 'avatar', label: 'Generate Avatar', icon: '🎨' },
    { id: 'profile', label: 'Publish Profile', icon: '📝' },
    { id: 'qdrant', label: 'Setup Memory', icon: '💾' },
    { id: 'memory', label: 'Seed Context', icon: '🌱' },
    { id: 'workspace', label: 'Init Workspace', icon: '📁' },
    { id: 'deploy', label: 'Deploy Agent', icon: '🚀' }
  ];

  const currentStepIdx = $derived(steps.findIndex((s) => s.id === run?.step));
  const progressPercent = $derived(
    run?.progress
      ? Math.round((run.progress.current / run.progress.total) * 100)
      : 0
  );
</script>

<div class="provisioning-progress">
  {#if !run}
    <div class="empty">
      <span class="icon">⏳</span>
      <p>Waiting for provisioning to start...</p>
    </div>
  {:else if run.status === 'completed'}
    <div class="completed">
      <span class="icon">✅</span>
      <h3>Provisioning Complete!</h3>
      <p>Your agent soul has been created successfully.</p>
      {#if run.result?.data?.npub}
        <div class="result-info">
          <span class="label">npub:</span>
          <code>{run.result.data.npub}</code>
        </div>
      {/if}
      {#if onComplete}
        <button class="btn-primary" onclick={onComplete}>
          View Soul
        </button>
      {/if}
    </div>
  {:else if run.status === 'failed'}
    <div class="failed">
      <span class="icon">❌</span>
      <h3>Provisioning Failed</h3>
      <p>{run.result?.error || run.message || 'An error occurred'}</p>
    </div>
  {:else}
    <div class="progress-header">
      <h3>Provisioning Soul</h3>
      <span class="percent">{progressPercent}%</span>
    </div>
    
    <div class="progress-bar">
      <div class="progress-fill" style="width: {progressPercent}%"></div>
    </div>
    
    <div class="current-step">
      <span class="spinner"></span>
      {run.message || 'Processing...'}
    </div>
    
    <div class="steps-list">
      {#each steps as step, idx}
        <div 
          class="step"
          class:completed={idx < currentStepIdx}
          class:active={idx === currentStepIdx}
          class:pending={idx > currentStepIdx}
        >
          <span class="step-icon">
            {#if idx < currentStepIdx}
              ✓
            {:else}
              {step.icon}
            {/if}
          </span>
          <span class="step-label">{step.label}</span>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .provisioning-progress {
    background: var(--card-bg);
    border-radius: 12px;
    padding: 1.5rem;
    border: 1px solid var(--border-color);
  }
  
  .empty, .completed, .failed {
    text-align: center;
    padding: 2rem 1rem;
  }
  
  .empty .icon, .completed .icon, .failed .icon {
    font-size: 3rem;
    display: block;
    margin-bottom: 1rem;
  }
  
  .completed h3, .failed h3 {
    margin: 0 0 0.5rem 0;
    font-size: 1.25rem;
  }
  
  .completed p, .failed p {
    color: var(--text-muted);
    margin: 0 0 1rem 0;
  }
  
  .completed {
    color: var(--success);
  }
  
  .completed h3 {
    color: var(--text-primary);
  }
  
  .failed {
    color: var(--error);
  }
  
  .failed h3 {
    color: var(--text-primary);
  }
  
  .result-info {
    background: rgba(16, 185, 129, 0.1);
    padding: 0.75rem 1rem;
    border-radius: 8px;
    margin-bottom: 1rem;
    display: inline-block;
  }
  
  .result-info .label {
    font-size: 0.75rem;
    color: var(--text-muted);
    margin-right: 0.5rem;
  }
  
  .result-info code {
    font-size: 0.8rem;
    color: var(--text-primary);
  }
  
  .btn-primary {
    background: var(--primary);
    color: white;
    border: none;
    padding: 0.75rem 1.5rem;
    border-radius: 8px;
    cursor: pointer;
    font-weight: 500;
    transition: background 0.15s;
  }
  
  .btn-primary:hover {
    background: #5558e3;
  }
  
  .progress-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    margin-bottom: 0.75rem;
  }
  
  .progress-header h3 {
    margin: 0;
    font-size: 1.1rem;
  }
  
  .percent {
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--primary);
  }
  
  .progress-bar {
    height: 8px;
    background: var(--border-color);
    border-radius: 4px;
    overflow: hidden;
    margin-bottom: 1rem;
  }
  
  .progress-fill {
    height: 100%;
    background: linear-gradient(90deg, var(--primary) 0%, #8b5cf6 100%);
    border-radius: 4px;
    transition: width 0.3s ease;
  }
  
  .current-step {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem;
    background: rgba(99, 102, 241, 0.1);
    border-radius: 8px;
    margin-bottom: 1.5rem;
    font-size: 0.9rem;
    color: var(--primary);
  }
  
  .spinner {
    width: 16px;
    height: 16px;
    border: 2px solid rgba(99, 102, 241, 0.3);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  
  .steps-list {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    gap: 0.5rem;
  }
  
  .step {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    border-radius: 6px;
    font-size: 0.8rem;
    transition: all 0.2s;
  }
  
  .step.pending {
    color: var(--text-muted);
    opacity: 0.5;
  }
  
  .step.active {
    background: rgba(99, 102, 241, 0.15);
    color: var(--primary);
  }
  
  .step.completed {
    color: var(--success);
  }
  
  .step-icon {
    font-size: 1rem;
    width: 24px;
    text-align: center;
  }
  
  .step.completed .step-icon {
    font-size: 0.8rem;
  }
  
  .step-label {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
</style>
