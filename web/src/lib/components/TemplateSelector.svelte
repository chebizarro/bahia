<script>
  import { templates, templatesByTier, loading, loadTemplates } from '$lib/stores/souls.js';
  import {
    HeavyIcon,
    LightweightIcon,
    StandardIcon,
    SuccessIcon,
    TemplateIcon
  } from '$lib/icons/domain-icons.js';

  let {
    selected = null,
    onSelect
  } = $props();

  const tierInfo = {
    lightweight: {
      icon: LightweightIcon,
      label: 'Lightweight',
      description: 'Fast, minimal resources. Good for simple tasks.'
    },
    standard: {
      icon: StandardIcon,
      label: 'Standard',
      description: 'Balanced capabilities for most use cases.'
    },
    heavy: {
      icon: HeavyIcon,
      label: 'Heavy',
      description: 'Maximum resources for complex workloads.'
    }
  };

  const groupedTemplates = $derived(templatesByTier());

  $effect(() => {
    if (templates.length === 0) {
      loadTemplates();
    }
  });

  function selectTemplate(template) {
    selected = template;
    onSelect?.(template);
  }
</script>

<div class="template-selector">
  <h3>Choose a Template</h3>
  <p class="hint">Select a template to start with or create a custom soul from scratch.</p>
  
  {#if loading.templates}
    <div class="loading">
      <div class="spinner"></div>
      Loading templates...
    </div>
  {:else}
    <div class="template-section">
      <button 
        class="template-card custom"
        class:selected={selected === null}
        onclick={() => selectTemplate(null)}
      >
        <div class="template-icon" aria-hidden="true"><TemplateIcon size={24} strokeWidth={1.75} /></div>
        <div class="template-info">
          <h4>Custom Soul</h4>
          <p>Start from scratch with your own brief</p>
        </div>
        {#if selected === null}
          <span class="check" aria-hidden="true"><SuccessIcon size={14} strokeWidth={2} /></span>
        {/if}
      </button>
    </div>
    
    {#each Object.entries(tierInfo) as [tier, info]}
      {#if groupedTemplates[tier]?.length > 0}
        {@const TierIcon = info.icon}
        <div class="template-section">
          <h4 class="tier-header">
            <span class="tier-icon" aria-hidden="true"><TierIcon size={16} strokeWidth={1.75} /></span>
            {info.label}
            <span class="tier-desc">{info.description}</span>
          </h4>
          
          <div class="template-grid">
            {#each groupedTemplates[tier] as template}
              <button 
                class="template-card"
                class:selected={selected?.identifier === template.identifier}
                onclick={() => selectTemplate(template)}
              >
                <div class="template-icon" aria-hidden="true"><TierIcon size={24} strokeWidth={1.75} /></div>
                <div class="template-info">
                  <h4>{template.name}</h4>
                  <p>{template.description || 'No description'}</p>
                  {#if template.tags?.length > 0}
                    <div class="template-tags">
                      {#each template.tags.slice(0, 3) as tag}
                        <span class="tag">{tag}</span>
                      {/each}
                    </div>
                  {/if}
                </div>
                {#if selected?.identifier === template.identifier}
                  <span class="check" aria-hidden="true"><SuccessIcon size={14} strokeWidth={2} /></span>
                {/if}
              </button>
            {/each}
          </div>
        </div>
      {/if}
    {/each}
    
    {#if templates.length === 0}
      <div class="empty-state">
        <p>No templates available. You can still create a custom soul.</p>
      </div>
    {/if}
  {/if}
</div>

<style>
  .template-selector {
    width: 100%;
  }
  
  .template-selector > h3 {
    font-size: 1.25rem;
    margin: 0 0 0.25rem 0;
  }
  
  .hint {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin: 0 0 1.5rem 0;
  }
  
  .loading {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    color: var(--text-muted);
    padding: 2rem;
    justify-content: center;
  }
  
  .spinner {
    width: 20px;
    height: 20px;
    border: 2px solid var(--border-color);
    border-top-color: var(--primary);
    border-radius: 50%;
    animation: spin 1s linear infinite;
  }
  
  @keyframes spin {
    to { transform: rotate(360deg); }
  }
  
  .template-section {
    margin-bottom: 1.5rem;
  }
  
  .tier-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.9rem;
    color: var(--text-primary);
    margin: 0 0 0.75rem 0;
  }
  
  .tier-icon {
    display: inline-flex;
    align-items: center;
  }
  
  .tier-desc {
    font-weight: normal;
    color: var(--text-muted);
    font-size: 0.8rem;
    margin-left: auto;
  }
  
  .template-grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 0.75rem;
  }
  
  .template-card {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 10px;
    padding: 1rem;
    text-align: left;
    cursor: pointer;
    transition: all 0.15s;
    width: 100%;
  }
  
  .template-card:hover {
    border-color: var(--primary);
    background: var(--hover-bg);
  }
  
  .template-card.selected {
    border-color: var(--primary);
    background: rgba(99, 102, 241, 0.1);
  }
  
  .template-card.custom {
    border-style: dashed;
  }
  
  .template-icon {
    flex-shrink: 0;
    width: 40px;
    height: 40px;
    display: flex;
    align-items: center;
    justify-content: center;
    background: rgba(99, 102, 241, 0.1);
    border-radius: 8px;
  }
  
  .template-info {
    flex: 1;
    min-width: 0;
  }
  
  .template-info h4 {
    font-size: 0.9rem;
    font-weight: 600;
    margin: 0 0 0.25rem 0;
    color: var(--text-primary);
  }
  
  .template-info p {
    font-size: 0.8rem;
    color: var(--text-muted);
    margin: 0;
    display: -webkit-box;
    line-clamp: 2;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
  }
  
  .template-tags {
    display: flex;
    gap: 0.25rem;
    margin-top: 0.5rem;
    flex-wrap: wrap;
  }
  
  .tag {
    font-size: 0.65rem;
    padding: 0.15rem 0.4rem;
    background: rgba(99, 102, 241, 0.15);
    color: var(--primary);
    border-radius: 4px;
  }
  
  .check {
    width: 24px;
    height: 24px;
    border-radius: 50%;
    background: var(--primary);
    color: white;
    display: flex;
    align-items: center;
    justify-content: center;
    font-size: 0.75rem;
    flex-shrink: 0;
  }
  
  .empty-state {
    text-align: center;
    padding: 2rem;
    color: var(--text-muted);
  }
</style>
