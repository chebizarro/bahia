<script>
  import { toasts, removeToast } from './toast.js';
  import Toast from './Toast.svelte';

  function handleClose(event) {
    removeToast(event.detail.id);
  }
</script>

<div class="toast-container">
  {#each $toasts as toast (toast.id)}
    <Toast
      id={toast.id}
      type={toast.type}
      title={toast.title}
      message={toast.message}
      on:close={handleClose}
    />
  {/each}
</div>

<style>
  .toast-container {
    position: fixed;
    top: 1rem;
    right: 1rem;
    z-index: 9999;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    pointer-events: none;
  }
  .toast-container :global(.toast) {
    pointer-events: all;
  }
</style>
