# Bahia Web Component Library

This guide documents the reusable Svelte components available in the Bahia web app.

**Location**: `web/src/lib/components/`

## Form Components

### Input

**Purpose**: Text input field with label, validation, and error display.

**Props**:
- `label` (string): Field label text
- `id` (string): HTML `id` attribute
- `type` (string, default `'text'`): Input type (`text`, `email`, `password`, `number`, etc.)
- `value` (string): Bound input value
- `placeholder` (string, optional): Placeholder text
- `required` (boolean, default `false`): Adds required indicator
- `disabled` (boolean, default `false`): Disables input
- `error` (string, optional): Error message to display below input
- `autocomplete` (string, optional): HTML autocomplete attribute

**Events**:
- `input`: Fires on value change (use `bind:value` for two-way binding)

**Example**:
```svelte
<Input
  label="Service Name"
  id="service-name"
  bind:value={serviceName}
  required
  error={errors.name}
  placeholder="e.g., web-api"
/>
```

**Accessibility**:
- Associates label with input via `for`/`id`
- Displays error with `aria-invalid` and `aria-describedby`
- Required fields marked with `*`

---

### Select

**Purpose**: Dropdown select field with label and validation.

**Props**:
- `label` (string): Field label text
- `id` (string): HTML `id` attribute
- `value` (string): Bound selected value
- `options` (array): Options list `[{ value, label }, ...]`
- `required` (boolean, default `false`): Adds required indicator
- `disabled` (boolean, default `false`): Disables select
- `error` (string, optional): Error message

**Events**:
- `change`: Fires on selection change (use `bind:value`)

**Example**:
```svelte
<Select
  label="Runtime Type"
  id="runtime-type"
  bind:value={runtimeType}
  options={[
    { value: 'docker', label: 'Docker' },
    { value: 'wasm', label: 'WebAssembly' }
  ]}
  required
/>
```

---

### Textarea

**Purpose**: Multi-line text input with label and validation.

**Props**:
- `label` (string): Field label text
- `id` (string): HTML `id` attribute
- `value` (string): Bound textarea value
- `placeholder` (string, optional): Placeholder text
- `rows` (number, default `4`): Number of visible rows
- `required` (boolean, default `false`): Adds required indicator
- `disabled` (boolean, default `false`): Disables textarea
- `error` (string, optional): Error message

**Events**:
- `input`: Fires on value change (use `bind:value`)

**Example**:
```svelte
<Textarea
  label="Runtime Config (JSON)"
  id="runtime-config"
  bind:value={configJson}
  rows={8}
  placeholder='{  "timeout": 300 }'
/>
```

---

### Checkbox

**Purpose**: Checkbox input with label.

**Props**:
- `label` (string): Checkbox label text
- `id` (string): HTML `id` attribute
- `checked` (boolean): Bound checked state
- `disabled` (boolean, default `false`): Disables checkbox

**Events**:
- `change`: Fires on check/uncheck (use `bind:checked`)

**Example**:
```svelte
<Checkbox
  label="Protected Environment"
  id="protected"
  bind:checked={isProtected}
/>
```

---

### FormField

**Purpose**: Generic form field wrapper with label and error display.

**Props**:
- `label` (string): Field label text
- `for` (string): Associates label with input `id`
- `required` (boolean, default `false`): Shows `*` indicator
- `error` (string, optional): Error message

**Slots**:
- `default`: Input element content

**Example**:
```svelte
<FormField label="Custom Field" for="custom" required error={errors.custom}>
  <input id="custom" type="text" bind:value={customValue} />
</FormField>
```

---

### LoadingButton

**Purpose**: Button with loading spinner and disabled state during async operations.

**Props**:
- `loading` (boolean): Shows spinner, disables button
- `disabled` (boolean, default `false`): Disables button
- `type` (string, default `'button'`): Button type (`button`, `submit`)
- `variant` (string, default `'primary'`): Style variant (`primary`, `secondary`, `danger`)

**Slots**:
- `default`: Button text/content

**Events**:
- `click`: Fires on button click (if not loading/disabled)

**Example**:
```svelte
<script>
  let saving = false;
  
  async function save() {
    saving = true;
    try {
      await api.createService(data);
    } finally {
      saving = false;
    }
  }
</script>

<LoadingButton loading={saving} on:click={save} variant="primary">
  Save Service
</LoadingButton>
```

---

## Feedback Components

### Toast

**Purpose**: Temporary notification message (success, error, info, warning).

**Props**:
- `type` (string): Message type (`success`, `error`, `info`, `warning`)
- `message` (string): Notification text
- `duration` (number, default `3000`): Auto-dismiss duration in ms (0 = no auto-dismiss)

**Events**:
- `dismiss`: Fires when toast is dismissed

**Example**:
```svelte
<Toast type="success" message="Service created successfully" duration={3000} />
```

**Usage with Toast Store**:
```javascript
import { toasts } from '$lib/components/toast.js';

// Show toast
toasts.success('Operation succeeded');
toasts.error('Operation failed');
toasts.info('FYI: something happened');
toasts.warning('Be careful!');
```

---

### ToastContainer

**Purpose**: Container that displays active toasts from the global toast store.

**Props**: None

**Placement**: Include once in `+layout.svelte`:
```svelte
<script>
  import ToastContainer from '$lib/components/ToastContainer.svelte';
</script>

<ToastContainer />
<slot />
```

---

### ErrorBoundary

**Purpose**: Catches errors in child components and displays fallback UI.

**Props**:
- None (wraps child content)

**Slots**:
- `default`: Protected component tree
- `error`: Custom error UI (optional, receives `{error}`)

**Example**:
```svelte
<ErrorBoundary>
  <slot name="error" let:error>
    <ErrorState message={error.message} />
  </slot>
  
  <MyComponentThatMightThrow />
</ErrorBoundary>
```

---

### ErrorState

**Purpose**: Displays an error message with optional retry action.

**Props**:
- `title` (string, default `'Something went wrong'`): Error title
- `message` (string): Error description
- `retryable` (boolean, default `false`): Shows retry button
- `retryText` (string, default `'Try Again'`): Retry button label

**Events**:
- `retry`: Fires when retry button is clicked

**Example**:
```svelte
<ErrorState
  title="Failed to load services"
  message={error.message}
  retryable
  on:retry={loadServices}
/>
```

---

### EmptyState

**Purpose**: Displays a message when no data is available, with optional action.

**Props**:
- `title` (string): Empty state title
- `message` (string): Description text
- `actionLabel` (string, optional): Action button text
- `icon` (string, optional): Icon name/emoji

**Events**:
- `action`: Fires when action button is clicked

**Example**:
```svelte
<EmptyState
  title="No services yet"
  message="Create your first service to get started"
  actionLabel="Create Service"
  icon="📦"
  on:action={openCreateModal}
/>
```

---

## Modal & Dialog Components

### Modal

**Purpose**: Full-featured modal dialog with header, body, footer, and backdrop.

**Props**:
- `open` (boolean): Controls modal visibility (use `bind:open`)
- `title` (string): Modal header title
- `size` (string, default `'md'`): Modal width (`sm`, `md`, `lg`, `xl`)
- `closeOnBackdrop` (boolean, default `true`): Click backdrop to close
- `closeOnEscape` (boolean, default `true`): Press ESC to close

**Slots**:
- `default`: Modal body content
- `footer`: Modal footer (buttons, actions)

**Events**:
- `close`: Fires when modal is closed

**Example**:
```svelte
<Modal bind:open={showCreateModal} title="Create Service" size="lg" on:close={resetForm}>
  <form on:submit|preventDefault={handleSubmit}>
    <Input label="Name" bind:value={name} />
    <!-- More fields -->
  </form>
  
  <svelte:fragment slot="footer">
    <button on:click={() => showCreateModal = false}>Cancel</button>
    <LoadingButton loading={saving} on:click={handleSubmit}>Create</LoadingButton>
  </svelte:fragment>
</Modal>
```

**Accessibility**:
- Focus trap: keeps keyboard focus inside modal
- `aria-modal="true"` and `role="dialog"`
- ESC key closes modal
- Restores focus to trigger element on close

---

### ConfirmDialog

**Purpose**: Simple confirmation dialog with confirm/cancel actions.

**Props**:
- `open` (boolean): Controls dialog visibility (use `bind:open`)
- `title` (string): Dialog title
- `message` (string): Confirmation message
- `confirmText` (string, default `'Confirm'`): Confirm button label
- `cancelText` (string, default `'Cancel'`): Cancel button label
- `variant` (string, default `'danger'`): Button variant (`primary`, `danger`)

**Events**:
- `confirm`: Fires when confirmed
- `cancel`: Fires when cancelled

**Example**:
```svelte
<ConfirmDialog
  bind:open={showDeleteConfirm}
  title="Delete Service"
  message="Are you sure you want to delete '{serviceName}'? This cannot be undone."
  confirmText="Delete"
  variant="danger"
  on:confirm={deleteService}
  on:cancel={() => showDeleteConfirm = false}
/>
```

---

## Layout & Data Components

### Card

**Purpose**: Container with border, padding, and optional header/footer.

**Props**:
- `title` (string, optional): Card header title
- `padding` (string, default `'md'`): Padding size (`sm`, `md`, `lg`)

**Slots**:
- `header`: Custom header content (overrides `title` prop)
- `default`: Card body content
- `footer`: Card footer content

**Example**:
```svelte
<Card title="Recent Deployments">
  <Table data={deployments} />
  
  <svelte:fragment slot="footer">
    <a href="/deployments">View all →</a>
  </svelte:fragment>
</Card>
```

---

### Table

**Purpose**: Simple data table with headers and rows.

**Props**:
- `data` (array): Array of row objects
- `columns` (array): Column definitions `[{ key, label, format? }, ...]`
- `emptyMessage` (string, default `'No data'`): Message when `data` is empty

**Slots**:
- `cell`: Custom cell renderer (receives `{row, column, value}`)

**Example**:
```svelte
<Table
  data={services}
  columns={[
    { key: 'name', label: 'Service' },
    { key: 'runtime_type', label: 'Runtime' },
    { key: 'created_at', label: 'Created', format: (val) => new Date(val).toLocaleDateString() }
  ]}
/>
```

---

### Badge

**Purpose**: Small colored label/tag for status, type, or metadata.

**Props**:
- `variant` (string): Color variant (`success`, `error`, `warning`, `info`, `default`)
- `text` (string): Badge text

**Example**:
```svelte
<Badge variant="success" text="Running" />
<Badge variant="error" text="Failed" />
<Badge variant="info" text="Pending" />
```

**Usage in Tables**:
```svelte
<Table data={deployments}>
  <svelte:fragment slot="cell" let:row let:column>
    {#if column.key === 'status'}
      <Badge variant={statusVariant(row.status)} text={row.status} />
    {:else}
      {row[column.key]}
    {/if}
  </svelte:fragment>
</Table>
```

---

## Soul Factory Components

### ProvisioningProgress

**Purpose**: Displays real-time provisioning progress for Soul Factory operations.

**Props**:
- `runId` (string): Provisioning run ID
- `events` (array): Array of progress events

**Example**:
```svelte
<ProvisioningProgress runId={$currentRun.id} events={$currentRun.events} />
```

---

### SoulCard

**Purpose**: Card component displaying a Soul's details and status.

**Props**:
- `soul` (object): Soul object with `{ name, status, pubkey, ... }`

**Events**:
- `click`: Fires when card is clicked

**Example**:
```svelte
<SoulCard soul={soulData} on:click={() => goto(`/souls/${soul.id}`)} />
```

---

### TemplateSelector

**Purpose**: Grid selector for Soul Factory templates.

**Props**:
- `templates` (array): Array of template objects
- `selected` (string): Currently selected template ID

**Events**:
- `select`: Fires when a template is selected (detail: `{ templateId }`)

**Example**:
```svelte
<TemplateSelector
  templates={$templates}
  selected={selectedTemplate}
  on:select={(e) => selectedTemplate = e.detail.templateId}
/>
```

---

## Accessibility Guidelines

All components follow these accessibility principles:

1. **Semantic HTML**: Use appropriate elements (`<button>`, `<a>`, `<label>`)
2. **Keyboard Navigation**: All interactive elements are keyboard-accessible
3. **Focus Management**: Modals trap focus, buttons have visible focus states
4. **ARIA Attributes**: Form errors use `aria-invalid`, `aria-describedby`
5. **Color Contrast**: All text meets WCAG 2.1 AA contrast requirements
6. **Screen Reader Support**: Labels, error messages, and state changes are announced

### Testing Accessibility

Use these tools to verify accessibility:

- **axe DevTools**: Browser extension for automated accessibility testing
- **Keyboard Navigation**: Tab through all interactive elements
- **Screen Reader**: Test with VoiceOver (macOS), NVDA (Windows), or JAWS

## Styling Conventions

Components use utility-first CSS with these conventions:

- **Spacing**: `gap-2`, `p-4`, `mb-2` (Tailwind-like)
- **Colors**: `text-red-600`, `bg-blue-50`, `border-gray-300`
- **Typography**: `text-sm`, `font-medium`, `leading-tight`
- **Responsive**: Mobile-first, `md:`, `lg:` breakpoints

**Customization**: Component styles are scoped within `<style>` blocks. Override via CSS custom properties or wrapper classes.

## Adding New Components

When creating new reusable components:

1. **Place in** `web/src/lib/components/`
2. **Export props** with clear names and defaults
3. **Emit events** for user interactions (`on:click`, `on:submit`, etc.)
4. **Add slots** for flexible composition
5. **Document** props, events, slots, and examples (add to this guide)
6. **Test** for accessibility and keyboard navigation

## Next Steps

- **Setup Guide**: See [web-app-setup.md](./web-app-setup.md)
- **API Client Reference**: See [web-api-client.md](./web-api-client.md)
- **Testing Guide**: See [web-testing.md](./web-testing.md)
