<script>
  import { assistantConnection, assistantUi, toggleAssistantPanel } from '$lib/stores/assistant.svelte.js';
  import AssistantBubble from './AssistantBubble.svelte';
  import AssistantPanel from './AssistantPanel.svelte';

  let { routeContext = null } = $props();
</script>

<AssistantPanel {routeContext} />
<AssistantBubble
  panelOpen={assistantUi.panelOpen}
  hasUnread={assistantUi.hasUnread}
  status={assistantConnection.status}
  onclick={toggleAssistantPanel}
/>
