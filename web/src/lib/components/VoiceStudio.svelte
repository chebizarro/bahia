<script>
  import VoiceStudio from './souls/VoiceStudio.svelte';

  let {
    value = $bindable(),
    assetRef = $bindable(''),
    showAdvanced = false,
    disabled = false,
    soul = null,
    draft = null,
    onPlay = null
  } = $props();
</script>

<VoiceStudio bind:value bind:assetRef {showAdvanced} {disabled} {soul} {draft} {onPlay} />
