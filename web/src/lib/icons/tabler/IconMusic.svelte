<svelte:options runes={false} />
<script>
export let size = 24;
export let strokeWidth = 2;
export let className = '';
export let ariaHidden = 'true';
</script>
<svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width={strokeWidth} stroke-linecap="round" stroke-linejoin="round" class={className} aria-hidden={ariaHidden}>
  <path d="M3 17a3 3 0 1 0 6 0a3 3 0 0 0 -6 0" />
  <path d="M13 17a3 3 0 1 0 6 0a3 3 0 0 0 -6 0" />
  <path d="M9 17v-13h10v13" />
  <path d="M9 8h10" />
</svg>
