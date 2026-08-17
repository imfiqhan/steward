# Changelog

Steward is `0.x`: the API can change between releases, and this file is where
those changes are written down. Read the **Upgrading** notes before moving a
running panel to a new version.

## v0.1.0

The first tagged release. Everything before it was reachable only as a
pseudo-version, so there is nothing to upgrade *from* — the notes below describe
what a panel gets, not what changed under it.

### The panel

- Resources from a struct: grid, form, detail, and a JSON API over the same
  definitions, with filters, quick search, sorting, batch actions, CSV export,
  soft deletes, and per-row policies.
- Authentication with optional TOTP two-factor and recovery codes, roles,
  permissions, per-route rules, and an operation log.
- A dashboard and custom pages built from `Row`, `Col`, `Card`, `Metric`,
  `Chart`, `Table`, `Text` — the same vocabulary a resource page uses.
- A command palette, a column picker, filter panels above the rows or in a
  drawer, and dark mode.
- Storage on named disks, local or object, public or signed.
- Settings that change without a deploy, a cache seam, a search seam
  (Meilisearch in `contrib/meilistore`), a mailer seam, and a scheduler for
  recurring work.

### Notifications

- `Notify`, `NotifyUsers`, `NotifyRole` write a row addressed to a panel
  account; a bell in the header reads it, and `/auth/notifications` holds the
  history the bell cannot.
- `PruneNotifications` trims read ones. **Nothing calls it for you.**

### Exports

- Past 10,000 matching rows a CSV export becomes a background job: the request
  answers at once, and the account is notified with a link when the file is
  built. `BackgroundExportRows` moves the threshold; a negative value always
  streams.
- Whole-table exports page by primary key rather than by `OFFSET`. On a table of
  102,253 articles that is 3 seconds instead of 74 for the same 22 MB file.
  Such an export is therefore **ordered by key, not by the grid's sort** — a
  single-page export keeps the visible order.
- Exports are written to `ExportDisk`, or to `DefaultDisk` when that is empty.
  Set it explicitly when the default disk's directory is served by something
  other than the panel: the file holds whatever rows its owner could read.
- `PruneExports` removes finished jobs and their files. Also not called for you.

### Upgrading

- **A panel with no `Prefix` now serves from the root.** `Prefix` was `/admin`
  by default; it is now empty, and an empty prefix means `/`. A panel that
  relied on the default moves wholesale — bookmarks, reverse-proxy rules and
  anything that pointed at `/admin` included. Keep the old paths by saying so:

  ```go
  steward.Config{Prefix: "/admin"}
  ```

  Build links with `c.URL("posts")` rather than writing the prefix out, and this
  stops being something to remember.

- **`Between(...).Datetime()` is gone.** A date range carries times now, which
  is what the pair was for:

  ```go
  f.DateRange("PublishedAt", "Published").Datetime()
  ```

- **Two migrations run on first boot** (or on the next `migrate up` if
  `DisableAutoMigrate` is set): `0005_create_admin_notifications` and
  `0006_create_admin_exports`.

- **Saving a row no longer leaves a banner.** The toast carries the message; a
  flash as well showed the same sentence twice.

### Known gaps

- Deleting a row does not delete files uploaded for it, and an upload abandoned
  before the form is saved stays on disk. Nothing prunes either yet.
- Fields cannot react to other fields in the browser: `Show` is evaluated per
  request, on the server. There are no tabbed or stepped forms.
- Password login is not rate limited. The two-factor challenge and the token
  endpoint are.
- Tested on WebKit and Chromium; Firefox has not been run.
