<script>
  import { createEventDispatcher } from 'svelte';
  import Modal from './Modal.svelte';
  import LoadingButton from './LoadingButton.svelte';

  export let open = false;
  export let title = 'Confirm Action';
  export let message = '';
  export let confirmLabel = 'Confirm';
  export let cancelLabel = 'Cancel';
  export let variant = 'default'; // default | danger
  export let loading = false;

  const dispatch = createEventDispatcher();

  function handleConfirm() {
    dispatch('confirm');
  }

  function handleCancel() {
    open = false;
    dispatch('cancel');
  }

  function handleClose() {
    open = false;
    dispatch('close');
  }
</script>

<Modal
  bind:open
  {title}
  size="sm"
  closeOnBackdrop={!loading}
  closeOnEscape={!loading}
  on:close={handleClose}
>
  <div class="confirm-dialog">
    <p class="message">{message}</p>
    <div class="actions">
      <LoadingButton
        variant="secondary"
        disabled={loading}
        on:click={handleCancel}
      >
        {cancelLabel}
      </LoadingButton>
      <LoadingButton
        variant={variant === 'danger' ? 'danger' : 'primary'}
        {loading}
        on:click={handleConfirm}
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
