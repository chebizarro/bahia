<script>
  import PersonalityBuilder from '$lib/components/PersonalityBuilder.svelte';

  let {
    value = $bindable(),
    showAdvanced = false,
    disabled = false,
    syncStore = true
  } = $props();
</script>

<PersonalityBuilder bind:value {showAdvanced} {disabled} {syncStore} />
