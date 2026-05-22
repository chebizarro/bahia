<script>
let { size = 24, strokeWidth = 2, class: className = "" } = $props();
</script>
<svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width={strokeWidth} stroke-linecap="round" stroke-linejoin="round" class={className} aria-hidden="true">
  <path d="M3 17a3 3 0 1 0 6 0a3 3 0 0 0 -6 0" />
  <path d="M13 17a3 3 0 1 0 6 0a3 3 0 0 0 -6 0" />
  <path d="M9 17v-13h10v13" />
  <path d="M9 8h10" />
</svg>
