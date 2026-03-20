# Plan: Notification Delivery Pipeline

**Status:** Design phase

## Problem

The `notification-new` Inngest handler currently only stores notifications in the database (in-app). It does not deliver notifications to external channels (Slack, email). Users have no way to receive real-time notifications outside the Sazabi dashboard.

## Solution

Extend `notification-new` to fan out delivery events after storing the notification. Add two new Inngest functions — `notification-deliver-slack` and `notification-deliver-email` — that handle channel-specific delivery. The fan-out respects user notification preferences (mute check) and sends separate events per channel.

## Architecture Overview

```
notification.new event
  └─ notification-new handler
       ├─ store-notification (existing — writes to DB)
       ├─ check-preferences (new — query user prefs for this notification type)
       └─ fan-out delivery events:
            ├─ notification.deliver.slack → notification-deliver-slack handler
            └─ notification.deliver.email → notification-deliver-email handler
```

Event names are already defined in `packages/inngest/src/events/notifications.ts`:
- `NOTIFICATION_EVENTS.DELIVER_SLACK` = `"notification.deliver.slack"`
- `NOTIFICATION_EVENTS.DELIVER_EMAIL` = `"notification.deliver.email"`

## Key Design Decisions

### Mute check in the fan-out step, not in each delivery handler

The `notification-new` handler checks user preferences once and only emits delivery events for enabled channels. This avoids redundant preference lookups in each delivery handler and makes it easy to add new channels later — just add another conditional send.

### Event payload carries everything needed for delivery

Each delivery event includes the full notification content (title, message, type, metadata) plus the notificationId. Delivery handlers are self-contained — they do not need to re-query the notifications table. This matches the existing `NotificationDeliveryPayload` type in `packages/notifications/src/types.ts`.

### Slack delivery reuses the alert Slack patterns, not the alert blocks

The notification Slack message is simpler than the alert card. It uses a basic block layout: header (notification title), section (message body), and context (notification type + timestamp). It does not reuse `buildAlertBlocks` from `packages/tools/src/alerts/lib/slack-delivery.ts` because alerts have severity, status, and structured detail sections that don't apply to general notifications.

However, the Slack client pattern is the same: `findSlackWorkspaceByOrganization` + `createSlackClient` from `packages/tools/src/slack/lib/client.ts`.

## Changes to notification-new.ts

Current flow:
1. Validate event payload
2. `store-notification` step — insert into DB, return `{ notificationId, ... }`

New flow:
1. Validate event payload (unchanged)
2. `store-notification` step (unchanged)
3. `check-preferences` step — query notification preferences for this user + org + notification type. Returns `{ slackEnabled: boolean, emailEnabled: boolean }`
4. `fan-out-delivery` step — conditionally send `notification.deliver.slack` and/or `notification.deliver.email` events

### Preference check logic

The preference check uses `resolveNotificationPreference` from **`packages/notifications/src/resolve.ts`** (shared package). This function is shared between the API's `preferences.resolve` tRPC endpoint and the Inngest `notification-new` handler so both use identical resolution logic. See the preferences backend plan for the function signature and intersection model.

The `check-preferences` step passes `projectId` from the event payload (if present). Channel activation requires **both gates open**: the project must have the channel enabled, AND the user must have the channel enabled. `resolveNotificationPreference` handles both lookups and returns the ANDed result.

Until the notification preferences table exists (see companion bead for preferences backend), default behavior:
- Slack: enabled if the organization has a connected Slack workspace with an alerts channel configured
- Email: disabled by default (no user email preferences table yet)

Once the preferences table exists, `resolveNotificationPreference` applies the intersection model. If no row is found for a gate, that gate defaults to enabled (project) or channel-specific system defaults (user).

**Follow-on (hq-ehuwj, blocked on prefs backend hq-p43dn):** Once the preferences table ships, remove the hardcoded defaults from `notification-new` and let `resolveNotificationPreference` drive all decisions unconditionally.

### Delivery event payload schema

```typescript
interface NotificationDeliveryEventData {
  notificationId: string;
  userId: string;
  organizationId: string;
  projectId?: string;            // required for project-level preference resolution
  type: NotificationType;
  title: string;
  message: string;
  metadata?: Record<string, unknown>;
}
```

This extends the existing `NotificationDeliveryPayload` from `packages/notifications/src/types.ts` and `NotificationDeliveryEventSchema` in `packages/notifications/src/events.ts`. Both must be updated to include `projectId` — without it, the preference resolution logic cannot consult project-level overrides.

**Required type/schema updates:**
- `packages/notifications/src/types.ts`: add `projectId?: string` to `NotificationDeliveryPayload`
- `packages/notifications/src/events.ts`: add `projectId: z.string().uuid().optional()` to `NotificationDeliveryEventSchema`

## notification-deliver-slack.ts Design

New file: `lambdas/inngest/src/functions/notification-deliver-slack.ts`

### Structure (following existing Inngest function patterns)

```typescript
export const handleNotificationDeliverSlack = inngest.createFunction(
  {
    id: "notification-deliver-slack",
    name: "Deliver Notification to Slack",
    retries: 3,
  },
  { event: NOTIFICATION_EVENTS.DELIVER_SLACK },
  async ({ event, step, logger }) => { ... }
);
```

### Steps

1. **validate** — Parse event data with `NotificationDeliveryEventSchema`
2. **find-workspace** — Call `findSlackWorkspaceByOrganization(organizationId)`. If no workspace or no alerts channel, log and return early (no error — workspace may have been disconnected between fan-out and delivery)
3. **deliver** — Post message to the alerts channel using `createSlackClient(workspace.botAccessToken).chat.postMessage()`

### Slack message format

Simple block layout:
- **Header block**: notification title (plain_text)
- **Section block**: message body (mrkdwn, converted via `convertMarkdownToSlackMrkdwn`)
- **Context block**: notification type label + timestamp

Fallback text: `"{title}: {message}"`

No thread_ts (each notification is a top-level message). No action buttons in v1.

### Return value

```typescript
{ delivered: boolean; channelId?: string; messageTs?: string; reason?: string }
```

## notification-deliver-email.ts Design

New file: `lambdas/inngest/src/functions/notification-deliver-email.ts`

### Email infrastructure

The codebase already uses **Resend** (`packages/emails`) with React Email templates. The email package has existing templates for magic links, org invitations, and waitlist confirmation.

### Structure

```typescript
export const handleNotificationDeliverEmail = inngest.createFunction(
  {
    id: "notification-deliver-email",
    name: "Deliver Notification to Email",
    retries: 3,
  },
  { event: NOTIFICATION_EVENTS.DELIVER_EMAIL },
  async ({ event, step, logger }) => { ... }
);
```

### Steps

1. **validate** — Parse event data with `NotificationDeliveryEventSchema`
2. **resolve-user-email** — Look up the user's email from Clerk (the auth provider). Use the Clerk Backend SDK to fetch user by userId
3. **send-email** — Send via Resend using a new `NotificationEmail` React Email template

### New email template

Add `packages/emails/src/notification-email.tsx` — a simple notification email template:
- Subject: `"[Sazabi] {title}"`
- Body: notification message with Sazabi branding (reuse `brand.tsx` patterns from existing templates)
- Footer: "You received this because you have email notifications enabled for {notification type} events."

Add corresponding `sendNotificationEmail` function to `packages/emails/src/index.ts`.

### Configuration

The Inngest Lambda **does not currently have** the required secrets. They must be provisioned:

**RESEND_API_KEY + RESEND_FROM_EMAIL:**
- SSM path: `/email/resend_credentials` (already exists, stored as JSON: `{ "api_key": "...", "from_email": "..." }`)
- Currently injected only into the API lambda (`lambda_api_secrets` in `terraform/main/ssm-secrets.tf`)
- **Action required:** Add `RESEND_API_KEY` and `RESEND_FROM_EMAIL` to `lambda_inngest_secrets` in `terraform/main/ssm-secrets.tf`, sourcing from `data.aws_ssm_parameter.resend_credentials`
- Add to `lambdas/inngest/src/config.ts` AppConfig: `resend: { apiKey: string; fromEmail: string }` (mirroring the API lambda's config pattern)
- From email for notifications: use the existing `from_email` value from the SSM secret (currently used for transactional emails)

**CLERK_SECRET_KEY:**
- SSM path: `/auth/clerk_secret_key` (already exists in SSM parameter store, defined in `terraform/prerequisites/ssm-secrets.tf`)
- The `data.aws_ssm_parameter.clerk_secret_key` block exists in `terraform/main/ssm-secrets.tf` but is **not injected into any lambda's environment variables**
- **Action required:** Add `CLERK_SECRET_KEY` to `lambda_inngest_secrets` in `terraform/main/ssm-secrets.tf`, sourcing from `data.aws_ssm_parameter.clerk_secret_key`
- Add to `lambdas/inngest/src/config.ts` AppConfig: `clerk: { secretKey: string }`

### Return value

```typescript
{ delivered: boolean; toEmail?: string; reason?: string }
```

## Registration

Add both new handlers to `lambdas/inngest/src/functions/index.ts` exports array.

## Feature Flags

Two flags gate this system:

**`"notification-delivery"`** — gates the entire fan-out. Checked at the start of the `fan-out-delivery` step in `notification-new`. When off, no delivery events are emitted and `notification-new` behaves exactly as today (store only). This is the master switch for the whole delivery pipeline.

**`"notification-delivery-email"`** — gates email specifically, checked within the `fan-out-delivery` step after the delivery flag is confirmed on. When off, only `DELIVER_SLACK` is emitted; email is suppressed entirely. Allows Slack delivery to ship and stabilize before enabling email.

| Component | Flag | Behavior when off |
|-----------|------|-------------------|
| `notification-new` `fan-out-delivery` step | `notification-delivery` | No delivery events emitted; store-only (current behavior) |
| `notification-new` `fan-out-delivery` step | `notification-delivery-email` | `DELIVER_EMAIL` not emitted; Slack unaffected |
| `notification-deliver-slack` | None | Never triggered if master flag is off |
| `notification-deliver-email` | None | Never triggered if master or email flag is off |

- Add `"notification-delivery"` and `"notification-delivery-email"` to `FeatureFlag` union in `apps/dashboard/src/types/feature-flags.ts`
- Create both flags in PostHog

## Implementation Checklist

- [ ] `packages/notifications/src/types.ts` — add `projectId?: string` to `NotificationDeliveryPayload`
- [ ] `packages/notifications/src/events.ts` — add `projectId: z.string().uuid().optional()` to `NotificationDeliveryEventSchema`
- [ ] `lambdas/inngest/src/functions/notification-new.ts` — add `check-preferences` step after `store-notification`, using `resolveNotificationPreference` from `packages/notifications`
- [ ] `lambdas/inngest/src/functions/notification-new.ts` — add `fan-out-delivery` step that conditionally sends `DELIVER_SLACK` and `DELIVER_EMAIL` events (include `projectId` in payload)
- [ ] `lambdas/inngest/src/functions/notification-deliver-slack.ts` — new file, Slack delivery handler
- [ ] `lambdas/inngest/src/functions/notification-deliver-email.ts` — new file, email delivery handler
- [ ] `packages/emails/src/notification-email.tsx` — new React Email template for notifications
- [ ] `packages/emails/src/index.ts` — add `sendNotificationEmail` export
- [ ] `lambdas/inngest/src/functions/index.ts` — register both new handlers
- [ ] `apps/dashboard/src/types/feature-flags.ts` — add `"notification-delivery"` and `"notification-delivery-email"` to `FeatureFlag` union
- [ ] Create `notification-delivery` and `notification-delivery-email` flags in PostHog
- [ ] `terraform/main/ssm-secrets.tf` — add `RESEND_API_KEY`, `RESEND_FROM_EMAIL`, and `CLERK_SECRET_KEY` to `lambda_inngest_secrets`
- [ ] `lambdas/inngest/src/config.ts` — add `resend: { apiKey, fromEmail }` and `clerk: { secretKey }` to AppConfig
- [ ] Default preference logic: Slack enabled when workspace connected, email disabled until preferences table exists

## Decisions

- **Slack channel target:** Alerts channel for simplicity. Dedicated notifications channel deferred.
- **Batching:** Deferred to follow-up (e.g., 5 user_joined events during bulk invite).
- **Email rate limiting:** Resend's built-in limits are sufficient for v1. Per-user throttling deferred.
- **Channel activation model:** Intersection — project gate AND user gate. See preference backend plan.
