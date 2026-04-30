<script>
  let {
    open = $bindable(),
    title = '',
    closeOnBackdrop = true,
    closeOnEscape = true,
    size = 'md',
    onClose,
    onOpened,
    onClosed,
    children
  } = $props();

  let previouslyFocused = null;
  let modalElement = $state(null);
  let wasOpen = $state(false);

  $effect(() => {
    if (open && !wasOpen) {
      previouslyFocused = document.activeElement;
      onOpened?.();
      modalElement?.focus();
    } else if (!open && wasOpen) {
      onClosed?.();
      previouslyFocused?.focus();
    }

    wasOpen = open;
  });

  $effect(() => {
    return () => {
      previouslyFocused?.focus();
    };
  });

  function close() {
    open = false;
    onClose?.();
    previouslyFocused?.focus();
  }

  function handleBackdropClick(event) {
    if (closeOnBackdrop && event.target === event.currentTarget) {
      close();
    }
  }

  function handleBackdropKeydown(event) {
    if (closeOnBackdrop && event.key === 'Enter') {
      close();
    }
  }

  function handleKeydown(event) {
    if (closeOnEscape && event.key === 'Escape') {
      close();
    }
  }
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div 
    class="modal-backdrop" 
    onclick={handleBackdropClick}
    onkeydown={handleBackdropKeydown}
    role="presentation"
  >
    <div
      class="modal {size}"
      role="dialog"
      aria-modal="true"
      aria-labelledby={title ? 'modal-title' : undefined}
      tabindex="-1"
      bind:this={modalElement}
    >
      {#if title}
        <div class="modal-header">
          <h2 id="modal-title" class="modal-title">{title}</h2>
          <button class="close-button" onclick={close} aria-label="Close" type="button">
            ×
          </button>
        </div>
      {:else}
        <button class="close-button-only" onclick={close} aria-label="Close" type="button">
          ×
        </button>
      {/if}
      <div class="modal-body">
        {@render children?.()}
      </div>
    </div>
  </div>
{/if}

<style>
  .modal-backdrop {
    position: fixed;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    padding: 1rem;
  }
  .modal {
    background: var(--card-bg);
    border: 1px solid var(--border-color);
    border-radius: 8px;
    box-shadow: 0 10px 25px rgba(0, 0, 0, 0.5);
    max-height: 90vh;
    overflow: auto;
    outline: none;
    position: relative;
  }
  .modal.sm { width: 100%; max-width: 400px; }
  .modal.md { width: 100%; max-width: 600px; }
  .modal.lg { width: 100%; max-width: 800px; }
  .modal.xl { width: 100%; max-width: 1200px; }

  .modal-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 1.5rem;
    border-bottom: 1px solid var(--border-color);
  }
  .modal-title {
    font-size: 1.25rem;
    font-weight: 600;
    color: var(--text-primary);
    margin: 0;
  }
  .close-button,
  .close-button-only {
    background: none;
    border: none;
    font-size: 2rem;
    line-height: 1;
    color: var(--text-muted);
    cursor: pointer;
    padding: 0;
    width: 2rem;
    height: 2rem;
    display: flex;
    align-items: center;
    justify-content: center;
    border-radius: 4px;
  }
  .close-button:hover,
  .close-button-only:hover {
    color: var(--text-primary);
    background: var(--hover-bg);
  }
  .close-button-only {
    position: absolute;
    top: 1rem;
    right: 1rem;
    z-index: 1;
  }
  .modal-body {
    padding: 1.5rem;
  }
</style>
