<script>
  import Badge from './Badge.svelte';
  import Input from './Input.svelte';
  import Select from './Select.svelte';
  import Checkbox from './Checkbox.svelte';

  let { 
    rules = $bindable([]),
    disabled = false
  } = $props();

  // All available rule types grouped by category
  const ruleCategories = [
    {
      name: 'Signatures & Security',
      rules: [
        { type: 'require_signature', label: 'Require Signature', description: 'Artifact must have a verified signature', params: [] },
        { type: 'require_approval', label: 'Require Approval', description: 'Deployment requires manual approval', params: [] }
      ]
    },
    {
      name: 'SBOM Requirements',
      rules: [
        { type: 'require_sbom', label: 'Require SBOM', description: 'Artifact must have an SBOM attached', params: [] },
        { 
          type: 'sbom_format', 
          label: 'SBOM Format', 
          description: 'Require specific SBOM formats',
          params: [
            { key: 'formats', label: 'Allowed Formats', type: 'multiselect', options: ['spdx', 'cyclonedx'] }
          ]
        },
        { type: 'sbom_parseability', label: 'SBOM Parseability', description: 'SBOM must be valid and parseable', params: [] },
        { 
          type: 'sbom_ntia_min_fields', 
          label: 'NTIA Minimum Fields', 
          description: 'SBOM must have NTIA minimum elements',
          params: []
        },
        { 
          type: 'sbom_trusted_generator', 
          label: 'Trusted Generator', 
          description: 'SBOM must be from a trusted generator',
          params: [
            { key: 'trusted_generators', label: 'Trusted Generators', type: 'text', placeholder: 'syft, trivy, cdxgen' }
          ]
        },
        { 
          type: 'sbom_subject_digest_match', 
          label: 'Subject Digest Match', 
          description: 'SBOM subject must match artifact digest',
          params: [
            { key: 'artifact_digest', label: 'Artifact Digest', type: 'text', placeholder: 'sha256:...' }
          ]
        }
      ]
    },
    {
      name: 'Vulnerability Policies',
      rules: [
        { 
          type: 'max_critical_vulns', 
          label: 'Max Critical Vulns', 
          description: 'Maximum critical vulnerabilities allowed',
          params: [
            { key: 'max', label: 'Maximum', type: 'number', default: 0 }
          ]
        },
        { 
          type: 'max_high_vulns', 
          label: 'Max High Vulns', 
          description: 'Maximum high vulnerabilities allowed',
          params: [
            { key: 'max', label: 'Maximum', type: 'number', default: 0 }
          ]
        },
        { 
          type: 'require_scan_status', 
          label: 'Require Scan Status', 
          description: 'Require specific scan status',
          params: [
            { key: 'status', label: 'Required Status', type: 'select', options: ['clean', 'warning', 'any'], default: 'clean' }
          ]
        }
      ]
    },
    {
      name: 'Package Restrictions',
      rules: [
        { 
          type: 'block_package', 
          label: 'Block Package', 
          description: 'Block artifacts containing specific packages',
          params: [
            { key: 'package', label: 'Package Name', type: 'text', placeholder: 'lodash' }
          ]
        },
        { 
          type: 'package_min_age', 
          label: 'Package Min Age', 
          description: 'Require packages to be at least N days old',
          params: [
            { key: 'days', label: 'Minimum Days', type: 'number', default: 7 }
          ]
        },
        { 
          type: 'package_min_downloads', 
          label: 'Package Min Downloads', 
          description: 'Require minimum download count',
          params: [
            { key: 'min', label: 'Minimum Downloads', type: 'number', default: 1000 }
          ]
        },
        { type: 'typosquat_check', label: 'Typosquat Check', description: 'Check for typosquatting packages', params: [] }
      ]
    }
  ];

  // Flat map of all rules for lookup
  const allRules = ruleCategories.flatMap(cat => cat.rules);
  const ruleMap = new Map(allRules.map(r => [r.type, r]));

  // Modal state
  let showAddModal = $state(false);
  let selectedCategory = $state(null);
  let selectedRuleType = $state(null);
  let newRuleParams = $state({});

  function getRuleInfo(type) {
    return ruleMap.get(type) || { label: type, description: '', params: [] };
  }

  function addRule() {
    if (!selectedRuleType) return;
    
    const ruleInfo = getRuleInfo(selectedRuleType);
    const rule = { type: selectedRuleType };
    
    // Add params if any
    if (ruleInfo.params.length > 0) {
      rule.params = {};
      for (const param of ruleInfo.params) {
        if (newRuleParams[param.key] !== undefined && newRuleParams[param.key] !== '') {
          let value = newRuleParams[param.key];
          // Convert comma-separated to array for multiselect/list params
          if (param.type === 'multiselect' || param.key.includes('generators') || param.key.includes('formats')) {
            if (typeof value === 'string') {
              value = value.split(',').map(s => s.trim()).filter(Boolean);
            }
          }
          // Convert to number for number params
          if (param.type === 'number') {
            value = parseInt(value, 10);
          }
          rule.params[param.key] = value;
        }
      }
    }
    
    rules = [...rules, rule];
    closeAddModal();
  }

  function removeRule(index) {
    rules = rules.filter((_, i) => i !== index);
  }

  function openAddModal() {
    showAddModal = true;
    selectedCategory = null;
    selectedRuleType = null;
    newRuleParams = {};
  }

  function closeAddModal() {
    showAddModal = false;
    selectedCategory = null;
    selectedRuleType = null;
    newRuleParams = {};
  }

  function selectCategory(catName) {
    selectedCategory = catName;
    selectedRuleType = null;
    newRuleParams = {};
  }

  function selectRuleType(type) {
    selectedRuleType = type;
    newRuleParams = {};
    // Initialize defaults
    const ruleInfo = getRuleInfo(type);
    for (const param of ruleInfo.params) {
      if (param.default !== undefined) {
        newRuleParams[param.key] = param.default;
      }
    }
  }

  let currentRuleInfo = $derived(selectedRuleType ? getRuleInfo(selectedRuleType) : null);
  let currentCategoryRules = $derived(
    selectedCategory 
      ? ruleCategories.find(c => c.name === selectedCategory)?.rules || []
      : []
  );
</script>

<div class="rule-builder">
  <!-- Current Rules List -->
  <div class="rules-list">
    {#if rules.length === 0}
      <p class="no-rules">No rules configured. Add rules to define policy requirements.</p>
    {:else}
      {#each rules as rule, index}
        {@const info = getRuleInfo(rule.type)}
        <div class="rule-item">
          <div class="rule-content">
            <div class="rule-header">
              <span class="rule-label">{info.label}</span>
              <Badge variant="default" size="sm">{rule.type}</Badge>
            </div>
            {#if rule.params && Object.keys(rule.params).length > 0}
              <div class="rule-params">
                {#each Object.entries(rule.params) as [key, value]}
                  <span class="param">
                    <span class="param-key">{key}:</span>
                    <span class="param-value">{Array.isArray(value) ? value.join(', ') : value}</span>
                  </span>
                {/each}
              </div>
            {/if}
          </div>
          <button 
            type="button"
            class="remove-btn" 
            onclick={() => removeRule(index)}
            {disabled}
            title="Remove rule"
          >
            ✕
          </button>
        </div>
      {/each}
    {/if}
  </div>

  <!-- Add Rule Button -->
  <button type="button" class="add-rule-btn" onclick={openAddModal} {disabled}>
    + Add Rule
  </button>

  <!-- Add Rule Modal -->
  {#if showAddModal}
    <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
    <div class="modal-backdrop" onclick={closeAddModal}>
      <!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
      <div class="modal" onclick={(e) => e.stopPropagation()}>
        <div class="modal-header">
          <h3>Add Policy Rule</h3>
          <button type="button" class="close-btn" onclick={closeAddModal}>✕</button>
        </div>
        
        <div class="modal-content">
          {#if !selectedCategory}
            <!-- Category Selection -->
            <p class="step-hint">Select a category:</p>
            <div class="category-list">
              {#each ruleCategories as category}
                <button 
                  type="button"
                  class="category-btn"
                  onclick={() => selectCategory(category.name)}
                >
                  <span class="category-name">{category.name}</span>
                  <span class="category-count">{category.rules.length} rules</span>
                </button>
              {/each}
            </div>
          {:else if !selectedRuleType}
            <!-- Rule Type Selection -->
            <button type="button" class="back-btn" onclick={() => selectedCategory = null}>
              ← Back to categories
            </button>
            <p class="step-hint">Select a rule from {selectedCategory}:</p>
            <div class="rule-type-list">
              {#each currentCategoryRules as ruleType}
                <button 
                  type="button"
                  class="rule-type-btn"
                  onclick={() => selectRuleType(ruleType.type)}
                >
                  <span class="rule-type-name">{ruleType.label}</span>
                  <span class="rule-type-desc">{ruleType.description}</span>
                </button>
              {/each}
            </div>
          {:else}
            <!-- Parameter Configuration -->
            <button type="button" class="back-btn" onclick={() => selectedRuleType = null}>
              ← Back to rules
            </button>
            <div class="param-config">
              <h4>{currentRuleInfo.label}</h4>
              <p class="rule-desc">{currentRuleInfo.description}</p>
              
              {#if currentRuleInfo.params.length > 0}
                <div class="param-fields">
                  {#each currentRuleInfo.params as param}
                    <div class="param-field">
                      <label for="param-{param.key}">{param.label}</label>
                      {#if param.type === 'select'}
                        <select 
                          id="param-{param.key}"
                          bind:value={newRuleParams[param.key]}
                        >
                          {#each param.options as opt}
                            <option value={opt}>{opt}</option>
                          {/each}
                        </select>
                      {:else if param.type === 'multiselect'}
                        <div class="multiselect">
                          {#each param.options as opt}
                            <label class="checkbox-label">
                              <input 
                                type="checkbox"
                                checked={newRuleParams[param.key]?.includes?.(opt)}
                                onchange={(e) => {
                                  if (!newRuleParams[param.key]) newRuleParams[param.key] = [];
                                  if (e.target.checked) {
                                    newRuleParams[param.key] = [...newRuleParams[param.key], opt];
                                  } else {
                                    newRuleParams[param.key] = newRuleParams[param.key].filter(v => v !== opt);
                                  }
                                }}
                              />
                              {opt}
                            </label>
                          {/each}
                        </div>
                      {:else if param.type === 'number'}
                        <input 
                          id="param-{param.key}"
                          type="number"
                          bind:value={newRuleParams[param.key]}
                          placeholder={param.placeholder || ''}
                        />
                      {:else}
                        <input 
                          id="param-{param.key}"
                          type="text"
                          bind:value={newRuleParams[param.key]}
                          placeholder={param.placeholder || ''}
                        />
                      {/if}
                    </div>
                  {/each}
                </div>
              {:else}
                <p class="no-params">This rule has no configurable parameters.</p>
              {/if}
              
              <button type="button" class="add-btn" onclick={addRule}>
                Add Rule
              </button>
            </div>
          {/if}
        </div>
      </div>
    </div>
  {/if}
</div>

<style>
  .rule-builder {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  .rules-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    min-height: 60px;
  }

  .no-rules {
    color: var(--text-muted);
    font-size: 0.875rem;
    font-style: italic;
    padding: 1rem;
    text-align: center;
    background: var(--hover-bg, rgba(255,255,255,0.03));
    border-radius: 6px;
  }

  .rule-item {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    padding: 0.75rem;
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 6px;
  }

  .rule-content {
    flex: 1;
  }

  .rule-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    margin-bottom: 0.25rem;
  }

  .rule-label {
    font-weight: 500;
    font-size: 0.875rem;
  }

  .rule-params {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.25rem;
  }

  .param {
    font-size: 0.75rem;
    background: var(--hover-bg, rgba(255,255,255,0.05));
    padding: 0.125rem 0.375rem;
    border-radius: 3px;
  }

  .param-key {
    color: var(--text-muted);
  }

  .param-value {
    color: var(--text-primary);
    font-family: monospace;
  }

  .remove-btn {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0.25rem;
    font-size: 0.875rem;
    opacity: 0.6;
    transition: opacity 0.15s, color 0.15s;
  }

  .remove-btn:hover:not(:disabled) {
    opacity: 1;
    color: var(--error);
  }

  .remove-btn:disabled {
    cursor: not-allowed;
  }

  .add-rule-btn {
    background: var(--card-bg);
    border: 1px dashed var(--border-color);
    border-radius: 6px;
    padding: 0.75rem;
    color: var(--primary);
    cursor: pointer;
    font-size: 0.875rem;
    transition: background 0.15s, border-color 0.15s;
  }

  .add-rule-btn:hover:not(:disabled) {
    background: var(--hover-bg);
    border-color: var(--primary);
  }

  .add-rule-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  /* Modal */
  .modal-backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.7);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
  }

  .modal {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    width: 90%;
    max-width: 500px;
    max-height: 80vh;
    overflow: hidden;
    display: flex;
    flex-direction: column;
  }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 1rem;
    border-bottom: 1px solid var(--border-color);
  }

  .modal-header h3 {
    margin: 0;
    font-size: 1rem;
  }

  .close-btn {
    background: none;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0.25rem;
    font-size: 1.25rem;
  }

  .close-btn:hover {
    color: var(--text-primary);
  }

  .modal-content {
    padding: 1rem;
    overflow-y: auto;
  }

  .step-hint {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin: 0 0 0.75rem;
  }

  .back-btn {
    background: none;
    border: none;
    color: var(--primary);
    cursor: pointer;
    padding: 0;
    font-size: 0.875rem;
    margin-bottom: 0.75rem;
  }

  .back-btn:hover {
    text-decoration: underline;
  }

  .category-list,
  .rule-type-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  .category-btn,
  .rule-type-btn {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 0.25rem;
    padding: 0.75rem;
    background: var(--hover-bg, rgba(255,255,255,0.03));
    border: 1px solid var(--border-color);
    border-radius: 6px;
    cursor: pointer;
    text-align: left;
    transition: background 0.15s, border-color 0.15s;
  }

  .category-btn:hover,
  .rule-type-btn:hover {
    background: var(--hover-bg, rgba(255,255,255,0.06));
    border-color: var(--primary);
  }

  .category-name,
  .rule-type-name {
    font-weight: 500;
    font-size: 0.875rem;
    color: var(--text-primary);
  }

  .category-count {
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  .rule-type-desc {
    font-size: 0.75rem;
    color: var(--text-muted);
  }

  /* Param Config */
  .param-config h4 {
    margin: 0 0 0.25rem;
    font-size: 1rem;
  }

  .rule-desc {
    color: var(--text-muted);
    font-size: 0.875rem;
    margin: 0 0 1rem;
  }

  .param-fields {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    margin-bottom: 1rem;
  }

  .param-field {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  .param-field label {
    font-size: 0.75rem;
    color: var(--text-muted);
    font-weight: 600;
  }

  .param-field input,
  .param-field select {
    padding: 0.5rem;
    border: 1px solid var(--border-color);
    border-radius: 4px;
    background: var(--input-bg, #1a1a2e);
    color: var(--text-primary);
    font-size: 0.875rem;
  }

  .multiselect {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
  }

  .checkbox-label {
    display: flex;
    align-items: center;
    gap: 0.25rem;
    font-size: 0.875rem;
    cursor: pointer;
  }

  .no-params {
    color: var(--text-muted);
    font-size: 0.875rem;
    font-style: italic;
    margin-bottom: 1rem;
  }

  .add-btn {
    width: 100%;
    padding: 0.75rem;
    background: var(--primary);
    border: none;
    border-radius: 6px;
    color: white;
    font-weight: 500;
    cursor: pointer;
    transition: opacity 0.15s;
  }

  .add-btn:hover {
    opacity: 0.9;
  }
</style>
