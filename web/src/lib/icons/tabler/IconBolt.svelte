<script>
let { size = 24, stroke = 2, class: className = '', ...rest } = $props();
</script>
<svg xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width={stroke} stroke-linecap="round" stroke-linejoin="round" class={className} {...rest}>
  <path d="M13 3l0 7l6 0l-8 11l0 -7l-6 0l8 -11" />
</svg>
