# Plan: Notification Preference Backend API

**Status:** Design phase

## Problem

Users cannot control which notification channels (in-app, Slack, email) deliver which notification types. The `NotificationPreference` type exists in `packages/notifications/src/types.ts` but has no backing database table or API endpoints.

## Solution

Add a `notification_preferences` database table and tRPC endpoints for managing per-user, per-organization notification preferences. Support both user-level defaults and project-level overrides.

## Current State

### What exists

- `NotificationPreference` interface in `packages/notifications/src/types.ts`:
  ```typescript
  interface NotificationPreference {
    id: string;
    userId: string;
    organizationId: string;
    channel: NotificationChannel;       // "in_app" | "slack" | "email"
    notificationType: NotificationType | null;  // null = default for all types
    enabled: boolean;
  }
  ```
  **Note:** This type is missing `projectId`. It must be updated to include `projectId?: string` to match the database schema (see implementation checklist).
- `NOTIFICATION_CHANNELS` constant: `{ IN_APP: "in_app", SLACK: "slack", EMAIL: "email" }`
- `notifications` table exists (stores delivered notifications)
- `notificationsRouter` in `lambdas/api/src/routers/notifications.ts` handles list/read/markAsRead but has no preference endpoints

### What does not exist

- No `notification_preferences` table in the database schema
- No tRPC endpoints for preference CRUD
- No project-level preference concept

## Key Design Decisions

### Channel activation is an intersection, not a precedence chain

A channel is enabled for delivery **only when both gates are open**: the project has the channel enabled, AND the user has the channel enabled. If either says no, the channel is off — regardless of the other's setting.

This means `resolveNotificationPreference` makes two lookups and ANDs them:
1. **Project gate** — does this project allow this channel? Stored as a project-level row (`userId` null; applies to all members of the project).
2. **User gate** — does this user want this channel? Stored as a user-level row (`userId` set, `projectId` null for org-wide default or set for a per-project user override).

**Why intersection, not precedence?** Project-level flags represent what the project admin has enabled for that context (e.g., "Slack notifications are on for this project"). User flags represent personal opt-in ("I want Slack notifications"). Neither should silently override the other — a project enabling Slack doesn't force a user who opted out to receive it, and a user enabling Slack doesn't cause delivery to a project where Slack has been disabled.

**Schema implication:** `userId` must be nullable to support project-level rows (no user). The `NULLS NOT DISTINCT` unique index handles null userId correctly.

**Why not extend the existing notifications table?** Preferences are per-user configuration, not per-notification records. Separate concerns.

### notificationType null means "default for all types"

A row with `notificationType: null` is the catch-all default for a channel. A row with a specific `notificationType` overrides the default for that type. This matches the existing `NotificationPreference` interface where `notificationType` is already nullable.

### Channel resolution: intersection of project gate and user gate

"Should notification type X be delivered on channel Y for user U in project P?" requires two independent lookups that must both return enabled:

**Project gate** — is channel Y enabled for project P?
```sql
SELECT enabled FROM notification_preferences
WHERE user_id IS NULL
  AND organization_id = $orgId
  AND project_id = $projectId
  AND channel = $channel
  AND (notification_type = $type OR notification_type IS NULL)
ORDER BY notification_type NULLS LAST  -- type-specific before catch-all
LIMIT 1;
```
If no row found: default = enabled (project hasn't explicitly disabled it).

**User gate** — does user U want channel Y?
```sql
SELECT enabled FROM notification_preferences
WHERE user_id = $userId
  AND organization_id = $orgId
  AND channel = $channel
  AND (notification_type = $type OR notification_type IS NULL)
  AND (project_id = $projectId OR project_id IS NULL)
ORDER BY
  project_id NULLS LAST,           -- per-project user preference before org-wide default
  notification_type NULLS LAST     -- type-specific before catch-all
LIMIT 1;
```
If no row found: system default (in-app = enabled, Slack = enabled if workspace connected, email = disabled).

**Result:** `projectEnabled AND userEnabled`. Both must be true for the channel to fire.

## Database Schema

### New table: notification_preferences

```typescript
export const notificationPreferences = pgTable(
  "notification_preferences",
  {
    id: uuid("id").primaryKey(),
    userId: text("user_id"),  // null for project-level rows (no specific user)
    organizationId: text("organization_id").notNull(),
    projectId: uuid("project_id").references(() => projects.id, { onDelete: "cascade" }),
    channel: notificationChannel("channel").notNull(),
    notificationType: notificationType("notification_type"),  // null = all types
    enabled: boolean("enabled").notNull().default(true),
    createdAt: timestamp("created_at", { withTimezone: true }).defaultNow().notNull(),
    updatedAt: timestamp("updated_at", { withTimezone: true }).defaultNow().notNull(),
  },
  (table) => ({
    // Unique constraint: one preference per user + org + project + channel + type combo
    uniquePreference: uniqueIndex("notification_preferences_unique_idx")
      .on(table.userId, table.organizationId, table.projectId, table.channel, table.notificationType),
    // Query index for preference resolution
    userOrgIdx: index("notification_preferences_user_org_idx")
      .on(table.userId, table.organizationId),
  }),
);
```

### New pgEnum: notification_channel

```typescript
export const notificationChannel = pgEnum(
  "notification_channel",
  ALL_NOTIFICATION_CHANNELS as [string, ...string[]],
);
```

### Migration

Generate with `bun run db:generate` in `packages/database`. The migration creates:
1. `notification_channel` enum type
2. `notification_preferences` table
3. Unique index on (userId, organizationId, projectId, channel, notificationType) — uses `CREATE UNIQUE INDEX CONCURRENTLY` for zero-downtime deployment
4. Query index on (userId, organizationId)

**Null handling in unique index:** PostgreSQL treats NULLs as distinct in unique indexes by default. Use `NULLS NOT DISTINCT` (PG 15+) to ensure only one catch-all row per user + org + channel + project combo.

**PG version confirmed:** Production runs PostgreSQL 17.4 via Supabase (see `packages/database/docker-compose.yml` — `postgres:17.4-alpine3.21` with comment "Matches current production supabase postgres version"). `NULLS NOT DISTINCT` is fully supported (available since PG 15). No fallback needed.

## tRPC Endpoints

Add preference endpoints to `notificationsRouter` in `lambdas/api/src/routers/notifications.ts`.

### preferences.list

```typescript
preferences: {
  list: protectedProcedure
    .input(z.object({
      projectId: z.string().uuid().optional(),
    }).optional())
    .query(...)
}
```

Returns all preferences for the current user + org. If `projectId` provided, includes project-level overrides. Response shape:

```typescript
{
  preferences: Array<{
    id: string;
    channel: NotificationChannel;
    notificationType: NotificationType | null;
    projectId: string | null;
    enabled: boolean;
  }>;
  defaults: {
    // System defaults for reference
    in_app: boolean;
    slack: boolean;
    email: boolean;
  };
}
```

### preferences.upsert

```typescript
preferences: {
  upsert: protectedProcedure
    .input(z.object({
      channel: NotificationChannelSchema,
      notificationType: NotificationTypeSchema.nullable(),
      projectId: z.string().uuid().optional(),
      enabled: z.boolean(),
    }))
    .mutation(...)
}
```

Uses INSERT ... ON CONFLICT UPDATE to upsert a preference row. Scoped to the authenticated user + org.

### preferences.delete

```typescript
preferences: {
  delete: protectedProcedure
    .input(z.object({
      preferenceId: z.string().uuid(),
    }))
    .mutation(...)
}
```

Deletes a specific preference row (returns to system default for that combo). Scoped to authenticated user + org.

### preferences.resolve

```typescript
preferences: {
  resolve: protectedProcedure
    .input(z.object({
      channel: NotificationChannelSchema,
      notificationType: NotificationTypeSchema,
      projectId: z.string().uuid().optional(),
    }))
    .query(...)
}
```

Returns the effective preference for a specific channel + type + optional project, applying the intersection model (project gate AND user gate). Useful for the delivery pipeline and for UI to show "effective" state.

## Preference Resolution Helper

Add a shared function to **`packages/notifications/src/resolve.ts`** (shared package, not the API lambda). This function must live in the shared `packages/notifications` package because it is imported by both the API lambda (for the `preferences.resolve` tRPC endpoint) and the Inngest lambda (for the `notification-new` handler's preference check step). Placing it in `lambdas/api/src/lib/notifications.ts` would make it inaccessible to the Inngest lambda.

```typescript
// packages/notifications/src/resolve.ts
export const resolveNotificationPreference = async (
  db: Database,
  params: {
    userId: string;
    organizationId: string;
    channel: NotificationChannel;
    notificationType: NotificationType;
    projectId?: string;
  }
): Promise<boolean> => { ... }
```

Export from `packages/notifications/src/index.ts` barrel.

This is used by both the tRPC `preferences.resolve` endpoint and by the `notification-new` handler's preference check step.

## Implementation Checklist

- [ ] `packages/notifications/src/types.ts` — add `projectId?: string` to `NotificationPreference` interface
- [ ] `packages/database/src/schema.ts` — add `notificationChannel` pgEnum
- [ ] `packages/database/src/schema.ts` — add `notificationPreferences` table (with `NULLS NOT DISTINCT` on unique index — PG 17.4 supports this)
- [ ] `packages/database` — run `bun run db:generate` to create migration
- [ ] `packages/notifications/src/resolve.ts` — add `resolveNotificationPreference` helper (shared package, not API lambda)
- [ ] `packages/notifications/src/index.ts` — export `resolveNotificationPreference`
- [ ] `lambdas/api/src/routers/notifications.ts` — add `preferences.list` endpoint
- [ ] `lambdas/api/src/routers/notifications.ts` — add `preferences.upsert` endpoint
- [ ] `lambdas/api/src/routers/notifications.ts` — add `preferences.delete` endpoint
- [ ] `lambdas/api/src/routers/notifications.ts` — add `preferences.resolve` endpoint (using shared `resolveNotificationPreference`)

## Decisions

- **Organization-level defaults:** Deferred — start with per-user preferences only.
- **Project cascade delete:** Yes, FK `onDelete: "cascade"` on `projectId`.
- **Bulk upsert endpoint:** Not needed. UI uses auto-save per-toggle (individual upsert per click). See `notification-preferences-ui.md`.
- **Channel activation model:** Intersection — project gate AND user gate must both be enabled. See "Channel resolution" above.
