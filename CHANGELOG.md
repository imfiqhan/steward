# Changelog

Steward is `0.x`: the API can change between releases, and this file is where
those changes are written down. Read the **Upgrading** notes before moving a
running panel to a new version.

## v0.1.2

### Fixed

- **A chart's legend was cut off at the bottom of its tile.** The Chart
  component appends the legend beside the plot rather than inside it, and the
  plot was taking the tile's whole height, so the legend began where the tile
  ended and the card clipped it — a row of labels with the descenders shaved
  off. The tile's height is now divided between the two: the plot gives up
  28px for one row of labels, 60px where many series wrap onto three.

  A chart tile is still a fixed `clamp(180px, 30vh, 300px)` whatever its span
  and whether or not it has a legend, so a row of tiles stays level. Charts
  without a legend are unchanged.

Nothing to do on upgrade, and nothing to change in a panel's own code: this is
in the stylesheet the module ships.

## v0.1.1

### Fixed

- **Quick search and the command palette on PostgreSQL, for a resource whose
  searchable columns are not all text.** A `uuid` column given to
  `QuickSearch` or `Command` made every search fail, not only a search for a
  uuid: PostgreSQL resolves `ILIKE` for text alone, quick search puts every
  declared column in one `OR`, and the refusal happens during analysis, so the
  whole statement went with it. A grid answered 500 for any term, including one
  that matched nothing; the palette answered 200 with an empty list. The column
  is cast now — `CAST(col AS TEXT) ILIKE ?` — on PostgreSQL only, and only for
  pattern matching. Equality is untouched, so `uuid = uuid` keeps its index.

  Relation paths are covered too: `QuickSearch("Submission.ID")` searches
  through a subquery, and that reads the same predicate.

Nothing to do on upgrade: no configuration, no migration, and the SQL is
unchanged on SQLite, MySQL and SQL Server. See
[Databases](https://steward.fiqhan.dev/docs/databases/) for what a pattern
search costs on a large table.

The `contrib/*` modules are unchanged and stay at `v0.1.0`. They require the
framework at a minimum, not a maximum, so requiring `v0.1.1` alongside them is
all it takes.

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
