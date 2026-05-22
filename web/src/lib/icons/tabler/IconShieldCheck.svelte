<svelte:options runes={false} />
<script>
export let size = 24;
export let strokeWidth = 2;
export let className = '';
export let ariaHidden = 'true';
</script>
<svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width={strokeWidth} stroke-linecap="round" stroke-linejoin="round" class={className} aria-hidden={ariaHidden}>
  <path d="M11.46 20.846a12 12 0 0 1 -7.96 -14.846a12 12 0 0 0 8.5 -3a12 12 0 0 0 8.5 3a12 12 0 0 1 -.09 7.06" />
  <path d="M15 19l2 2l4 -4" />
</svg>
