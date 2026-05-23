<script>
  import { statusTone, titleize } from '$lib/backup/model.js';
  let { value = '', label = '' } = $props();
  const text = $derived(label || titleize(value || 'unknown'));
  const tone = $derived(statusTone(value || label));
</script>

<span class={`pill ${tone}`}>{text}</span>

<style>
  .pill { display: inline-flex; align-items: center; border: 1px solid var(--border-color); border-radius: 999px; padding: 0.18rem 0.55rem; font-size: 0.78rem; font-weight: 650; white-space: nowrap; }
  .success { border-color: rgba(16, 185, 129, 0.5); color: #86efac; background: rgba(16, 185, 129, 0.08); }
  .pending { border-color: rgba(59, 130, 246, 0.5); color: #93c5fd; background: rgba(59, 130, 246, 0.08); }
  .warning { border-color: rgba(245, 158, 11, 0.55); color: #fcd34d; background: rgba(245, 158, 11, 0.08); }
  .danger { border-color: rgba(239, 68, 68, 0.5); color: #fca5a5; background: rgba(239, 68, 68, 0.08); }
  .neutral { color: var(--text-muted); background: var(--card-bg); }
</style>
