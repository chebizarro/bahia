# Bahia Web Component Reference

Reusable Svelte components live in `web/src/lib/components/`. This page describes their source contracts; route-specific components are documented by their source and tests.

Form primitives, dialogs and feedback components are `runes={false}` components with exported props and default slots; the rest use Svelte 5 `$props()` and snippets. Every component takes callback props (`onclick`, `onChange`, `onClose`, …); none dispatches custom Svelte events.

## Form primitives

| Component | Current props |
| --- | --- |
| `Input` | `id`, `name`, `type`, `value`, `placeholder`, `min`, `max`, `step`, `ariaLabel`, `disabled`, `required`, `error`, `oninput`, `onchange`, `onblur`, `onkeydown` |
| `Select` | `id`, `name`, `value`, `options`, `disabled`, `required`, `error`, `placeholder`, `onchange`, `onblur` |
| `Textarea` | `id`, `name`, `value`, `placeholder`, `disabled`, `required`, `error`, `rows`, `oninput`, `onchange`, `onblur` |
| `Checkbox` | `id`, `name`, `checked`, `disabled`, `label`, `onchange`; default slot when `label` is empty |
| `FormField` | `label`, `id`, `error`, `hint`, `required`; default slot |
| `LoadingButton` | `type`, `variant`, `loading`, `disabled`, `fullWidth`, `title`, `onclick`; default slot |

Labels and hints/errors belong in `FormField`; `Input`, `Select`, and `Textarea` render only native controls.

```svelte
<FormField label="Service name" id="service-name" required error={errors.name}>
  <Input
    id="service-name"
    name="name"
    bind:value={serviceName}
    required
    onblur={validateName}
  />
</FormField>

<LoadingButton loading={saving} onclick={save}>Save</LoadingButton>
```

Use callback props such as `onclick` and `onchange`; these components do not dispatch custom `click` or `change` events.

## Feedback and empty/error states

### Toast and ToastContainer

`Toast` accepts `id`, `type`, `title`, `message`, and `onClose`. Duration is managed by the store, not the component. Use the store exported through `$lib/components/toast.js`. `ToastContainer` takes no props and belongs once in the root layout.

### ErrorBoundary and ErrorState

`ErrorBoundary` is an imperative wrapper. Props: `fallbackTitle`, `fallbackMessage`, `resetLabel`, `onReset`, and `onError`. A parent can bind it and call exported `showError(error, info)`. It does not automatically catch arbitrary descendant exceptions.

`ErrorState` accepts `title`, `message`, `details`, `resetLabel`, `showIcon`, and `onReset`, plus a default slot.

### EmptyState

`EmptyState` is a Svelte 5 component with `title`, `message`, `icon` or `iconComponent`, `showIcon`, `actionLabel` plus `onAction`, and optional `action`/`children` snippets.

```svelte
<EmptyState
  title="No souls"
  message="Create the first soul."
  actionLabel="Create soul"
  onAction={() => goto('/souls/new')}
/>
```

## Dialogs

### Modal

Props:

- bindable `open`;
- `title`, `titleIcon`;
- `size`: `sm`, `md`, `lg`, or `xl`;
- `closeOnBackdrop`, `closeOnEscape`;
- `onClose`, `onOpened`, `onClosed`.

Content is one default slot; there is no footer slot. The component focuses the dialog on open, restores previous focus on close, handles Escape/backdrop according to props, and declares dialog ARIA roles.

```svelte
<Modal bind:open={showDialog} title="Create service" onClose={reset}>
  <form on:submit|preventDefault={save}>
    <!-- fields and buttons -->
  </form>
</Modal>
```

### ConfirmDialog

Props are `open`, `title`, `titleIcon`, `message`, `confirmLabel`, `cancelLabel`, `variant`, `loading`, `onConfirm`, `onCancel`, and `onClose`, plus a default slot. When the confirmation submits a signed intent, pass `intentDomain` (and `intentOrgId` or `intentRecord`) so the confirm button is gated by intent readiness like an `IntentGate`.

```svelte
<ConfirmDialog
  bind:open={confirmDelete}
  title="Delete service"
  message="This cannot be undone."
  confirmLabel="Delete"
  variant="danger"
  onConfirm={removeService}
/>
```

## Layout and data

### Card

`Card` has `title`, `titleIcon`, `value`, `subtitle`, `status`, and a `children` snippet. It does not implement named header/footer slots or a padding prop.

### Table

`Table` accepts:

- `columns` with `key`, `label`, and optional `text`, `icon`, or `render`;
- `data`;
- `onRowClick`;
- `rowClickable`, defaulting from `onRowClick`.

The empty row text is fixed to `No data`. `render(row)` returns trusted HTML and must not receive untrusted content.

```svelte
<Table
  data={services}
  columns={[
    { key: 'name', label: 'Service' },
    { key: 'runtime_type', label: 'Runtime' }
  ]}
  onRowClick={(row) => goto('/services/' + row.id)}
/>
```

### Badge

`Badge` accepts `variant` and `size`; text is the default slot.

```svelte
<Badge variant="success">Running</Badge>
```

## Intent and session gates

### IntentGate

`IntentGate` wraps mutation controls in a native `fieldset` that is disabled until a signed intent for `domain` can be submitted (`$lib/stores/intent-readiness.svelte.js`). Props: `domain`, optional `orgId`, `record` (its org id is resolved the way the stores resolve it) and `orgField`; content is the `children` snippet. Readiness comes from local state only — a disconnected relay never disables a control. While the session opens, the reason ("Connecting…") is exposed as tooltip and to assistive technology; when the session knows no organization for an org-scoped intent the reason is shown beside the control; when the signer lacks NIP-44 the controls are disabled with the blocker.

```svelte
<IntentGate domain="service" record={service}>
  <LoadingButton onclick={save}>Save</LoadingButton>
</IntentGate>
```

### AuthGuard

`AuthGuard` renders `children` only for a signed-in session that holds one of `requiredRoles` (roles come from the decrypted `org-member` records); otherwise it redirects to `/`. `requiresRestCompatibility` additionally requires the daemon to advertise `direct_nostr_http_auth` for pages that use an HTTP route.

### PendingDomainIntents and SensitiveIntentNotice

`PendingDomainIntents` lists the session's pending intents for `domain` (published, awaiting the `30315` status or the canonical record). `SensitiveIntentNotice` does the same for a gift-wrapped sensitive `domain` (`org`, `secret`, `notification`, `relay`), whose pending rows live in `$lib/stores/sensitive-intents.svelte.js` and are initialized once the session is authenticated.

### RouteCanaryOutages and SBOMDetails

`RouteCanaryOutages` renders the `route-canary` records for `serviceId`/`environmentId` (or all, when null), with `open` filtering, `showServiceNames`/`resolveServiceName`, `emptyTitle`/`emptyMessage` and an `onCount` callback. `SBOMDetails` renders an `sbom` reference with its `packages`, `attestation` and `loading` state and downloads the payload through the Blossom blob proxy.

## ConnectionStatus

`ConnectionStatus` exposes subscription health in the app shell.

Props:

- `connection`, defaulting to `controlplaneConnection`;
- `retry`, defaulting to control-plane `manualRetry`.

The expanded panel shows relay count/list, `lastEventAt`, `lastEoseAt`, `lastError` and the last `CLOSED` reason of the protected bootstrap REQ (for example `restricted:` when the signed-in pubkey is not admitted by the sidecar), with manual retry for error/disconnected states. The connection object also carries `resubscribeAttempts`, `lastClosedReason`, and reconnect state.

## SoulFactory components

### ProvisioningProgress

Props are `run` and optional `onComplete`. `run` contains `status`, `step`, `progress`, `message`, and terminal `result`. The component knows all eight stages and never decides completion from EOSE.

```svelte
<ProvisioningProgress
  run={provisioningRuns.get(requestId)}
  onComplete={() => goto('/souls/' + agentId)}
/>
```

### SoulCard

`SoulCard` accepts only `soul`. It renders its own link to `/souls/{soul.agentId}`; it does not emit a custom click event. It displays soul, deployment, runtime-target, and runtime-state badges.

```svelte
<SoulCard {soul} />
```

### TemplateSelector

`TemplateSelector` accepts `selected` and `onSelect`. It loads templates from the SoulFactory store; callers do not pass a `templates` prop. The callback receives the template object or `null` for custom.

```svelte
<TemplateSelector bind:selected onSelect={(template) => selected = template} />
```

Souls routes also use `AvatarStudio`, `PersonalityBuilder`, `VoiceStudio`, and top-level `MemoryConfig`. Consult their `$props()` and tests before reuse.

## Svelte conventions

- For `runes={false}` components, use exported props, default slots, and explicit callback props.
- For runes components, use the `$props()` contract and snippets where defined.
- Prefer callback props over new event-dispatcher APIs.
- Label native controls through `FormField`.
- Update unit tests whenever a public prop contract changes.

## Related documents

- [Web app setup](web-app-setup.md)
- [Web testing](web-testing.md)
- [Web store-first design](architecture/web-store-first.md)
