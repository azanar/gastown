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

### Single table with optional projectId (not a separate project preferences table)

The existing `NotificationPreference` type has `userId` + `organizationId`. Add an optional `projectId` column. When `projectId` is null, the row represents a user-level default for the organization. When `projectId` is set, it's a project-level override.

**Why single table?** The preference resolution logic is: project-level override > user-level default > system default. A single table with a nullable `projectId` keeps the query simple — one SELECT with ORDER BY projectId NULLS LAST, LIMIT 1.

**Why not extend the existing notifications table?** Preferences are per-user configuration, not per-notification records. Separate concerns.

### notificationType null means "default for all types"

A row with `notificationType: null` is the catch-all default for a channel. A row with a specific `notificationType` overrides the default for that type. This matches the existing `NotificationPreference` interface where `notificationType` is already nullable.

### Precedence rules

Resolution order for "should notification type X be delivered on channel Y for user U in project P?":

1. Project-level preference for this specific type + channel + user + project → use it
2. Project-level default (null type) for this channel + user + project → use it
3. User-level preference for this specific type + channel + user + org → use it
4. User-level default (null type) for this channel + user + org → use it
5. System default: in-app = enabled, Slack = enabled if workspace connected, email = disabled

This can be expressed as a single query:

```sql
SELECT enabled FROM notification_preferences
WHERE user_id = $userId
  AND organization_id = $orgId
  AND channel = $channel
  AND (notification_type = $type OR notification_type IS NULL)
  AND (project_id = $projectId OR project_id IS NULL)
ORDER BY
  project_id NULLS LAST,           -- project-specific before user-default
  notification_type NULLS LAST     -- type-specific before catch-all
LIMIT 1;
```

If no row found, fall back to system defaults.

## Database Schema

### New table: notification_preferences

```typescript
export const notificationPreferences = pgTable(
  "notification_preferences",
  {
    id: uuid("id").primaryKey(),
    userId: text("user_id").notNull(),
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

Returns the effective preference for a specific channel + type + optional project, walking the precedence chain. Useful for the delivery pipeline and for UI to show "effective" state.

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

## Open Questions

- Should there be organization-level defaults (admin sets defaults for all users)? Defer — start with per-user preferences only.
- Should deleting a project cascade-delete its notification preferences? Yes, using FK `onDelete: "cascade"`.
- ~~Do we need a bulk upsert endpoint for the settings UI to save all preferences at once?~~ **Resolved: No.** The UI plan (`notification-preferences-ui.md`) specifies auto-save with immediate per-toggle upsert — each toggle calls `preferences.upsert` individually on click. A bulk endpoint is unnecessary: the matrix is at most ~30 cells (10 notification types × 3 channels), users change one preference at a time, and bulk upsert optimizes for a use case that doesn't exist in the UI. No `preferences.bulkUpsert` endpoint will be implemented.
