<svelte:options runes={false} />
<script>
  import Modal from './Modal.svelte';
  import LoadingButton from './LoadingButton.svelte';
  import IntentGate from './IntentGate.svelte';

  export let open = false;
  export let title = 'Confirm Action';
  export let titleIcon = null;
  export let message = '';
  export let confirmLabel = 'Confirm';
  export let cancelLabel = 'Cancel';
  export let variant = 'default';
  export let loading = false;
  export let onConfirm = null;
  export let onCancel = null;
  export let onClose = null;
  // When confirming publishes a signed intent, name its domain (and the
  // record's org id, when known) so confirm stays disabled until the intent
  // can be submitted. Cancel is never gated.
  export let intentDomain = '';
  export let intentOrgId = '';

  function handleConfirm() {
    onConfirm?.();
  }

  function handleCancel() {
    open = false;
    onCancel?.();
  }

  function handleClose() {
    open = false;
    onClose?.();
  }
</script>

<Modal
  bind:open
  {title}
  {titleIcon}
  size="sm"
  closeOnBackdrop={!loading}
  closeOnEscape={!loading}
  onClose={handleClose}
>
  <div class="confirm-dialog">
    <p class="message">{message}</p>
    <slot />
    <div class="actions">
      <LoadingButton
        variant="secondary"
        disabled={loading}
        onclick={handleCancel}
      >
        {cancelLabel}
      </LoadingButton>
      {#if intentDomain}
        <IntentGate domain={intentDomain} orgId={intentOrgId}>
          <LoadingButton
            variant={variant === 'danger' ? 'danger' : 'primary'}
            {loading}
            onclick={handleConfirm}
          >
            {confirmLabel}
          </LoadingButton>
        </IntentGate>
      {:else}
        <LoadingButton
          variant={variant === 'danger' ? 'danger' : 'primary'}
          {loading}
          onclick={handleConfirm}
        >
          {confirmLabel}
        </LoadingButton>
      {/if}
    </div>
  </div>
</Modal>

<style>
  .confirm-dialog {
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }
  .message {
    color: var(--text-primary);
    line-height: 1.5;
    margin: 0;
  }
  .actions {
    display: flex;
    gap: 0.75rem;
    justify-content: flex-end;
  }
</style>
