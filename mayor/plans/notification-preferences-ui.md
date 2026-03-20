# Plan: Notification Preferences UI

**Status:** Design phase

## Problem

Users cannot configure which notification channels deliver which notification types. The backend preferences API (see `notification-preferences-backend.md`) provides tRPC endpoints for per-user, per-project preference CRUD, but there is no UI for users to view or manage these settings.

## Solution

Add a "Notifications" settings page to the organization settings area of the dashboard. The page lets users configure per-channel delivery preferences (in-app, Slack, email), with optional per-project overrides. It consumes the `preferences.list`, `preferences.upsert`, and `preferences.delete` tRPC endpoints defined in the backend plan.

## Where It Lives

### Route

`/settings/notifications` — nested under the existing organization settings layout.

The dashboard already has a settings section at `/settings` with pages for general org settings, members, billing, and integrations. Notifications is a new sibling page.

### Navigation

Add a "Notifications" item to the settings sidebar nav in `apps/dashboard/src/features/settings/settings-layout.tsx`, positioned after "Integrations" (since notification channels depend on integrations like Slack being connected).

Icon: `Bell` from lucide-react (consistent with the notification bell in the top nav).

## Page Structure

The page has two sections: **Default Preferences** (org-wide user defaults) and **Project Overrides** (per-project customization).

### Section 1: Default Preferences

A matrix of notification types × channels, rendered as a grid of toggles.

```
                        In-App    Slack     Email
─────────────────────────────────────────────────
All notifications        [on]     [on]     [off]    ← catch-all default
─────────────────────────────────────────────────
Alert triggered          [on]     [on]     [off]
Alert resolved           [on]     [on]     [off]
Deployment completed     [on]     [off]    [off]
Monitor status change    [on]     [on]     [off]
Member joined            [on]     [off]    [off]
...
```

- **Rows**: One row per `NotificationType` value, plus a "All notifications" catch-all row at the top.
- **Columns**: One column per `NotificationChannel` (`in_app`, `slack`, `email`).
- **Cells**: Toggle switch. On = enabled, off = disabled.
- **Catch-all row**: Maps to `notificationType: null` in the backend. When toggled, it sets the default for all types on that channel. Type-specific rows override the catch-all. See "Catch-all toggle behavior" below for cascade rules.
- **Inheritance indicator**: If a type-specific row has no explicit preference (inheriting from the catch-all), the toggle shows the catch-all's state with a dimmed/inherited visual treatment. Clicking it creates an explicit override.

### Section 2: Project Overrides

A collapsible section below defaults. Initially collapsed with a summary line: "No project-specific overrides" or "2 projects with custom preferences".

**Populating the summary count**: The `preferences.list` endpoint only returns preferences for a single `projectId` at a time — it cannot provide the count of projects with overrides. To populate the summary line, the UI calls a separate `preferences.listProjectsWithOverrides` endpoint (see API Contract below) that returns the list of project IDs (and names) that have at least one override row. This is a lightweight query (SELECT DISTINCT project_id) and is fetched once when the section renders.

**Expanding** shows a project selector dropdown. Selecting a project reveals the same matrix as Section 1, but scoped to that project. Values that match the user-level default show as inherited (dimmed toggle). Toggling creates a project-level override row in the backend.

**Removing an override**: Each project-level override row has a "Reset to default" action (trash icon or link) that calls `preferences.delete` to remove the project-level row, reverting to the user default.

### Channel availability

Channels that aren't available are disabled with an explanation:

| Channel | Disabled when | Disabled message |
|---------|---------------|------------------|
| Slack | No Slack workspace connected to the org | "Connect Slack in Integrations to enable" (links to `/settings/integrations`) |
| Email | Never (always available via Clerk email) | — |
| In-app | Never (always available) | — |

Slack workspace connection status is already queryable — the alert card plan and delivery pipeline plan both reference `findSlackWorkspaceByOrganization`.

## API Contract

The UI consumes four tRPC endpoints from the backend preferences API:

### Reading preferences

```typescript
// Fetch all preferences for current user + org
const { data } = trpc.notifications.preferences.list.useQuery({
  projectId: selectedProjectId,  // optional — include project overrides
});

// Response shape:
{
  preferences: Array<{
    id: string;
    channel: "in_app" | "slack" | "email";
    notificationType: NotificationType | null;  // null = catch-all
    projectId: string | null;  // null = user-level default, string = project override
    enabled: boolean;
  }>;
  defaults: {
    in_app: boolean;   // system default: true
    slack: boolean;    // system default: true if workspace connected
    email: boolean;    // system default: false
  };
}
```

**Backend plan cross-reference**: The `preferences.list` response shape in `notification-preferences-backend.md` currently omits `projectId` from the response array. The UI depends on `projectId` to distinguish user-level rows (`projectId: null`) from project-level override rows (`projectId: string`). The backend plan must be updated to include `projectId: string | null` in the response shape.

### Writing preferences

```typescript
// Toggle a preference
trpc.notifications.preferences.upsert.useMutation({
  onSuccess: () => utils.notifications.preferences.list.invalidate(),
});

// Input:
{
  channel: "in_app" | "slack" | "email",
  notificationType: NotificationType | null,  // null = catch-all
  projectId?: string,                         // omit for user-level default
  enabled: boolean,
}
```

### Deleting overrides

```typescript
// Remove a project-level override (revert to user default)
trpc.notifications.preferences.delete.useMutation({
  onSuccess: () => utils.notifications.preferences.list.invalidate(),
});

// Input:
{ preferenceId: string }
```

### Listing projects with overrides

```typescript
// Fetch project IDs that have at least one override for this user
const { data } = trpc.notifications.preferences.listProjectsWithOverrides.useQuery();

// Response shape:
{
  projects: Array<{
    projectId: string;
    projectName: string;
  }>;
}
```

This powers the Section 2 summary line ("N projects with custom preferences") and the project selector dropdown. The backend implements this as `SELECT DISTINCT project_id FROM notification_preferences WHERE user_id = $userId AND organization_id = $orgId AND project_id IS NOT NULL`, joined to `projects` for the name.

**Backend plan cross-reference**: This endpoint does not exist in `notification-preferences-backend.md` yet. It must be added there as `preferences.listProjectsWithOverrides`.

### Effective state resolution

The UI computes effective state client-side using the same precedence rules as the backend:

1. Project-level preference for specific type + channel → use it
2. Project-level catch-all for channel → use it
3. User-level preference for specific type + channel → use it
4. User-level catch-all for channel → use it
5. System default (`defaults` from response)

This avoids calling `preferences.resolve` per cell — the full preference list is small enough to resolve client-side.

## Key Design Decisions

### Optimistic updates with auto-save (not a form with a Save button)

Each toggle calls `preferences.upsert` immediately on click. The toggle flips optimistically and reverts on error. No "Save changes" button, no unsaved-changes warning.

**Why auto-save?** Each toggle is an independent preference row in the database. There's no concept of "submit all preferences at once" — each row is independent. A Save button would either batch individual upserts (artificial batching) or require a new `bulkUpsert` endpoint (unnecessary complexity). Auto-save also matches the pattern users expect from notification settings (Gmail, Slack, GitHub all use immediate toggles).

**Why not a bulk upsert?** The backend plan's open questions mention this possibility. It's not needed: the matrix is at most ~30 cells (10 notification types × 3 channels), and users change one preference at a time. A bulk endpoint optimizes for a use case that doesn't exist in the UI.

### Catch-all toggle behavior when type-specific overrides exist

When the user toggles the catch-all row (e.g., "All notifications" → Slack OFF), and type-specific overrides already exist for that channel, the behavior is:

**The catch-all toggle does NOT cascade-delete type-specific overrides.** It only upserts the catch-all row (`notificationType: null`). Type-specific overrides remain and continue to take precedence over the catch-all per the resolution rules.

**Why no cascade?** Cascade-deleting would silently destroy user configuration. If a user has explicitly enabled Slack for "Alert triggered" and then turns the catch-all OFF, they likely want most notifications off but alerts still on. Deleting the override would be surprising.

**Visual feedback after toggling catch-all:**
- Type-specific rows that have explicit overrides continue showing as "explicit" (solid toggle) — they are unaffected.
- Type-specific rows with no override now inherit the new catch-all value. Their toggles update to show the new inherited state (dimmed).
- A transient info banner appears: "Updated default for {channel}. {N} notification types have custom overrides that are unchanged." The banner auto-dismisses after 5 seconds.

**Toggling catch-all ON when overrides exist that are OFF:**
Same principle — no cascade. The type-specific OFF overrides remain. The banner says "{N} notification types remain disabled with custom overrides."

**"Reset all to default" action:**
The catch-all row has a secondary action (visible on hover or via "..." menu): "Reset all {channel} preferences." This deletes ALL type-specific overrides for that channel + scope (user-level or project-level), leaving only the catch-all. A confirmation dialog appears: "This will remove {N} custom overrides for {channel} notifications. All types will inherit the default setting." This is the only destructive cascade action, and it requires explicit confirmation.

### Client-side preference resolution (not per-cell API calls)

The `preferences.list` endpoint returns all preferences for the user + org in a single query. The UI resolves effective state per cell using a `resolvePreference(preferences, defaults, channel, type, projectId)` helper. This avoids N API calls for N cells and keeps the UI snappy.

The resolution helper mirrors the backend's SQL precedence logic but operates on the in-memory preference array. Since the preference set is small (max ~90 rows for a user with overrides on 3 projects × 10 types × 3 channels), this is trivially fast.

### Notification types are derived from the NotificationType enum

The matrix rows come from the `NotificationType` values exported from `packages/notifications/src/types.ts`. If a new notification type is added to the enum, it automatically appears in the settings UI with inherited defaults — no UI code change needed.

Display names and descriptions for each type live in a `NOTIFICATION_TYPE_META` map in the UI component, mapping enum values to human-readable labels and one-line descriptions.

### Severity threshold is not a first-class UI concept

The bead description mentions "per-severity threshold." Rather than a separate severity slider, this maps naturally to the type-specific toggles: alert types (`alert_triggered`, `alert_resolved`) already carry severity in their notification metadata. If per-severity filtering is needed later (e.g., "only notify me for critical alerts"), it would be a filter within the `alert_triggered` row — implemented as a dropdown or threshold selector replacing the simple toggle for that specific row. Defer this to a follow-up; the toggle matrix covers the common case.

## State Management

### Query layer

```typescript
// In a custom hook: useNotificationPreferences(projectId?)
const preferencesQuery = trpc.notifications.preferences.list.useQuery(
  { projectId },
  { staleTime: 30_000 },  // preferences change infrequently
);

const upsertMutation = trpc.notifications.preferences.upsert.useMutation({
  onMutate: async (newPref) => {
    // Optimistic update: modify cached preference list
    await utils.notifications.preferences.list.cancel();
    const prev = utils.notifications.preferences.list.getData({ projectId });
    utils.notifications.preferences.list.setData({ projectId }, (old) => ({
      ...old,
      preferences: applyOptimisticUpsert(old.preferences, newPref),
    }));
    return { prev };
  },
  onError: (_err, _vars, ctx) => {
    // Rollback on error
    utils.notifications.preferences.list.setData({ projectId }, ctx.prev);
  },
  onSettled: () => {
    utils.notifications.preferences.list.invalidate();
  },
});
```

### Preference resolution helper

```typescript
function resolvePreference(
  preferences: Preference[],
  defaults: { in_app: boolean; slack: boolean; email: boolean },
  channel: NotificationChannel,
  notificationType: NotificationType | null,
  projectId?: string,
): { enabled: boolean; source: "explicit" | "inherited" | "system-default" } {
  // 1. Project-level specific type
  // 2. Project-level catch-all
  // 3. User-level specific type
  // 4. User-level catch-all
  // 5. System default
}
```

The `source` field drives visual treatment: `"explicit"` renders a solid toggle, `"inherited"` renders a dimmed toggle with a tooltip showing where the value comes from, `"system-default"` renders a dimmed toggle with "(default)" label.

## UX Details

### Empty state

When a user first visits the page, no preference rows exist in the database. All toggles show system defaults (in-app on, Slack on if connected, email off) with inherited/default visual treatment. The first toggle click creates a preference row.

### Loading state

Skeleton grid matching the matrix layout. The channel headers and type labels render immediately; toggle cells show skeleton pulse animations.

### Error handling

- **Upsert failure**: Toast notification ("Failed to update preference. Please try again."), toggle reverts to previous state via optimistic rollback.
- **List query failure**: Error banner above the matrix ("Unable to load notification preferences.") with a retry button.

### Responsive behavior

On narrow viewports (< 768px), the matrix collapses to a list layout: each notification type is a card with three toggle rows (one per channel) stacked vertically.

## Feature Flag

The notification preferences page is gated behind **`"notification-preferences"`**.

**Frontend:** `useFeatureFlag("notification-preferences")` — checked in two places:
1. `settings-layout.tsx` — conditionally renders the "Notifications" nav item
2. `/settings/notifications/page.tsx` — redirects to `/settings` if flag is off

**Backend:** No flag needed. The tRPC endpoints are safe to deploy ungated — they require authentication and return empty results when no preferences exist.

Registration:
- Add `"notification-preferences"` to `FeatureFlag` union in `apps/dashboard/src/types/feature-flags.ts`
- Create flag in PostHog

## Visual Design

### Matrix cell states

| State | Toggle appearance | Tooltip |
|-------|------------------|---------|
| Explicitly enabled | Solid toggle, on | "Enabled (set by you)" |
| Explicitly disabled | Solid toggle, off | "Disabled (set by you)" |
| Inherited enabled | Dimmed toggle, on | "Enabled (inherited from {source})" |
| Inherited disabled | Dimmed toggle, off | "Disabled (inherited from {source})" |
| Channel unavailable | Disabled toggle, off | "Connect Slack in Integrations" |

### Catch-all row styling

The "All notifications" catch-all row has a subtle background highlight (`bg-muted/50`) and a bottom border to visually separate it from the type-specific rows. A help icon next to the label shows a tooltip: "Sets the default for all notification types on this channel. Override individual types below."

## Implementation Checklist

- [ ] `apps/dashboard/src/types/feature-flags.ts` — add `"notification-preferences"` to `FeatureFlag`
- [ ] Create `notification-preferences` flag in PostHog
- [ ] `apps/dashboard/src/features/settings/settings-layout.tsx` — add "Notifications" nav item, gated on feature flag
- [ ] `apps/dashboard/src/app/settings/notifications/page.tsx` — new route, gated on feature flag
- [ ] `apps/dashboard/src/features/notifications/components/notification-preferences-page.tsx` — page component with default preferences and project overrides sections
- [ ] `apps/dashboard/src/features/notifications/components/preferences-matrix.tsx` — reusable matrix grid component (used for both default and project-scoped views)
- [ ] `apps/dashboard/src/features/notifications/components/preference-toggle.tsx` — toggle cell with inherited/explicit/unavailable visual states
- [ ] `apps/dashboard/src/features/notifications/hooks/use-notification-preferences.ts` — custom hook wrapping `preferences.list` query + `upsert`/`delete` mutations with optimistic updates
- [ ] `apps/dashboard/src/features/notifications/lib/resolve-preference.ts` — client-side preference resolution helper
- [ ] `apps/dashboard/src/features/notifications/lib/notification-type-meta.ts` — display names and descriptions for each `NotificationType`

## Open Questions

1. **Should admins be able to set org-wide defaults?** The backend plan defers org-level defaults. If added later, the UI would need an "Organization defaults" tab visible only to admins, with the same matrix layout. The component architecture (reusable `PreferencesMatrix`) supports this without restructuring.

2. **Digest/batching preferences?** The delivery pipeline plan mentions batching as a future improvement. If added, the UI would need a "Delivery frequency" dropdown per channel (Immediate / Hourly digest / Daily digest). Not in scope for v1.

3. **Per-severity alert filtering?** Deferred. The current toggle matrix covers enable/disable per notification type. A severity threshold within `alert_triggered` (e.g., "only critical and above") would replace the simple toggle with a dropdown for that specific row. Worth a follow-up plan if users request it.
