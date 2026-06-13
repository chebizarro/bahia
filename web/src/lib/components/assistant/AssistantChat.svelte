<script>
  import { assistantConnection, assistantUi } from '$lib/stores/assistant.svelte.js';
  import AssistantBubble from './AssistantBubble.svelte';
  import AssistantPanel from './AssistantPanel.svelte';

  let { routeContext = null, defaultSelectedRefs = [] } = $props();
</script>

<AssistantPanel {routeContext} {defaultSelectedRefs} />
<AssistantBubble
  panelOpen={assistantUi.panelOpen}
  hasUnread={assistantUi.hasUnread}
  status={assistantConnection.status}
/>
