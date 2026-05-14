<script>
  import Modal from './Modal.svelte';
  import LoadingButton from './LoadingButton.svelte';

  let {
    open = $bindable(),
    title = 'Confirm Action',
    titleIcon = null,
    message = '',
    confirmLabel = 'Confirm',
    cancelLabel = 'Cancel',
    variant = 'default',
    loading = false,
    children,
    onConfirm,
    onCancel,
    onClose
  } = $props();

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
    {@render children?.()}
    <div class="actions">
      <LoadingButton
        variant="secondary"
        disabled={loading}
        onclick={handleCancel}
      >
        {cancelLabel}
      </LoadingButton>
      <LoadingButton
        variant={variant === 'danger' ? 'danger' : 'primary'}
        {loading}
        onclick={handleConfirm}
      >
        {confirmLabel}
      </LoadingButton>
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
