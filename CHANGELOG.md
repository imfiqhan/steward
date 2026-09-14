# Changelog

Steward is `0.x`: the API can change between releases, and this file is where
those changes are written down. Read the **Upgrading** notes before moving a
running panel to a new version.

## Unreleased

### Removed

- **`Admin` and `Context.Admin` are gone.** They were kept as an alias and a
  duplicate field through v0.3.0 so a codebase could move at its own pace; the
  names are `Panel` and `Context.Panel` now, and nothing answers to the old
  ones.

  This is a break. `0.x` is where it belongs — before anything is promised —
  and keeping a deprecated pair forever costs more than a rename does once.

  **What to do:** replace `steward.Admin` with `steward.Panel` and `c.Admin`
  with `c.Panel`. They were the same type and the same pointer, so nothing but
  the spelling changes. `gopls rename` does it across a workspace in one pass.

## v0.3.0

### Added

- **The sidebar collapses to a rail rather than disappearing.** The header's
  toggle used to slide it off screen entirely, so a panel with the sidebar shut
  had no navigation at all. It now keeps a narrow column of the entries' icons,
  and the page moves over to meet it. Below `48rem` it is still an overlay that
  slides away — a rail on a phone spends a tenth of the screen on icons.

    The rail is a working menu: its entries are clickable and focusable, and
  hovering or tabbing to one shows its label beside it. The component library
  marks a closed sidebar `inert` and `aria-hidden`, which is right for a nav
  that has left the screen and would make a rail a picture of a menu — neither
  the pointer, the keyboard nor a screen reader could reach it — so both are
  lifted while the rail is what is on screen.

    An entry with no icon falls back to the first letter of its title. The rail's
  width is `--sidebar-rail-width`, a theme token like any other.

- **The entry you are on is told apart from the one you are hovering.** Both
  were given the same background, so the only thing between "selected" and
  "under the pointer" was a font weight. The current entry is a shade past that
  colour now — mixed from it, so it stays a step beyond hover whichever way a
  theme's palette runs, darker on a light sidebar and lighter on a dark one.

- **`Config.BrandIcon`** is the panel's own mark: a Lucide name shown beside the
  brand, and the only thing standing for the panel once the rail hides its name.
  Unset, the brand's first letter is used. `Verify` reports a name that does not
  resolve, since a blank mark is the whole of what a rail shows.

### Changed

- **`steward.Admin` is now `steward.Panel`.** The type is one panel inside an
  application, and `steward.App` — the struct every `main.go` hands to
  `steward.CLI` — already had the better claim to the name "app". "Panel" is
  what this project's own prose has called the object all along.

  **Nothing breaks.** `Admin` remains as an alias, so code on either name
  compiles and the two interoperate. `Context.Panel` is the field to reach for;
  `Context.Admin` is still there and still the same pointer, since a struct
  field cannot be aliased the way a type can. Both are deprecated, not removed.

  Unchanged on purpose: `AdminUser` is an administrator, not a panel; the
  `admin_` table prefix is data and is already configurable with
  `Config.TablePrefix`; and the mount path was never `/admin` to begin with —
  `Config.Prefix` defaults to the root and is yours to set.

  The `contrib/*` modules stay on `Admin`. They require the framework at
  `v0.1.0`, which does not have `Panel`; they move once they are bumped to a
  version that does.

### Fixed

- **The `no_ui` build compiles again.** It had not for several releases: the
  tagged stub renderer was missing methods the rest of the package reaches for,
  and a page's view model happened to live in the file the tag excludes.
  Nothing caught it, because a build tag is a separate compilation and no gate
  built it.

  A headless panel now serves the JSON API, refuses HTML with `503` naming the
  tag, serves `Config.AssetsFS` if one is set, and leaves `Verify` useful —
  icon names pass rather than every one being reported missing, since there is
  no sprite to check against. `make noui` builds, vets and tests it, and CI
  runs that on every push.

### Upgrading

**Nothing has to change to build.** `Admin` is an alias of `Panel` and
`Context.Admin` still holds the same pointer as `Context.Panel`, so a codebase
on the old names compiles untouched — verified against an application with 90
uses of them.

Three things to know:

- **`staticcheck` will now flag the old names** as `SA1019`. That is the one
  visible cost of the deprecation, and it is why the rename is worth doing at
  your own pace rather than not at all.
- **A collapsed sidebar is now a rail of icons** rather than nothing. If a menu
  entry has no `Icon`, it falls back to the first letter of its title —
  legible, but a glyph says more. Set `Config.BrandIcon` for the panel's own
  mark; without one the brand's first letter is used.
- **`no_ui` builds compile again.** If you worked around that by not using the
  tag, the workaround is no longer needed.

The `contrib/*` modules require the framework at a minimum, not a maximum, so
requiring `v0.3.0` alongside them is all it takes.

## v0.2.0

### Added

- **`Form.Tags`** — a list of free values in one text column. Type a value,
  press Enter or comma, and it becomes a chip; Backspace on an empty input takes
  the last one back. The column holds a JSON array, the same shape `Files` uses
  for its paths, and `Column.Tags()` and `DetailField.Tags()` draw the stored
  array as chips rather than as raw JSON.

  This is not `MultiSelect`: there is no list to pick from, no pivot table, and
  nothing virtual — the value goes into the record's own column. Whatever posts
  the field, the stored value is normalised: trimmed, whitespace collapsed,
  blanks and repeats dropped, capped at 200 values.

- **`Config.ThemeCSS`** — a panel's own colours and corner rounding, as custom
  properties layered over the bundle's. All 44 tokens the panel reads are named
  in [Customization](https://steward.fiqhan.dev/docs/customization/), including
  the login page, which now reads them too.

- **A warning for a class the stylesheet has no rule for.** The bundle is
  compiled from the class names the framework's own templates use, so a Tailwind
  utility an override reaches for is in it only by coincidence — and writing one
  that is not fails silently. With `Dev: true` the panel reads the overrides at
  boot and names what it finds. A class your own `ThemeCSS` defines counts as
  known.

- **`migrate down -force`.** A rollback whose plan is every applied migration is
  refused without it, since that is the command that empties the database.
  `-yes` is accepted as the same thing.

- **`Admin.Close`** stops a panel's background work — the queued-export worker
  — and waits for an export already in flight to finish. It leaves `Config.DB`
  alone, so close the panel first and the database after. A long-lived process
  rarely needs it; **a test does**, since without it every panel a suite builds
  goes on polling a database the test has closed.

- **`Config.DisableQueryProbe`** turns off the `Verify` probe described below.

- **`Context.NestedIDs`** maps a HasMany relation's rows from the key the form
  gave them — `new_3_a1b2` for a row the browser added, its own id for one that
  was already there — to the primary key they were saved as. `Create` filled
  that id and the row went out of scope with it; it is kept now, because it is
  what a record referring to a sibling row has to resolve against. Read it from
  a `Saved` hook; the rows do not exist when `Saving` runs.

### Changed

- **Errors name the nearest valid value, the set to choose from, and the line
  the mistake is on.** Every message that reports a name a reader chose — a
  field path, a badge colour, a disk, a validation rule, an icon, a filter
  layout, a relation, a CLI command, a flag, a field type — now reads:

  ```
  posts: grid column: unknown field "Titel" (admin/posts.go:24)
    did you mean: Title
    available: AuthorID, Body, Cover, CreatedAt, ID, Keywords, Status, Title
  ```

  Suggestions are offered within two edits and ignore case. Candidate lists are
  sorted and capped at fifteen with a count of the rest — an unknown icon used
  to answer with all 1,637 names on one line. The same mistake now produces the
  same message every run, which it did not when the list came out of a map.

  Declaration sites come from the stack at the moment a builder runs, so
  nothing had to be threaded to get them. `QuickSearch`, `Command`,
  `DefaultSort` and `Tree` take paths as varargs and have no per-path object to
  hang a site on; their messages name the model instead.

- **`Verify` runs the statements a panel's declarations build** — reading the
  table by column name, each quick and command search, each filter, each
  sortable column — bounded with `LIMIT 0`. The database parses, analyses and
  plans, which is where these failures are raised, and returns without reading
  a row.

  This reaches what no amount of checking the Go type can: a column the model
  declares and no migration ever added (invisible to `SELECT *` — the field
  just stays at its zero value), and a predicate the dialect refuses for a
  column's type, which is how quick search on a `uuid` column broke every
  search on PostgreSQL in v0.1.0. A table that does not exist yet is a
  migration that has not run and is passed over.

- **The scaffolder prints the paths it wrote, one per line, and nothing else.**
  What to do next moved behind `--verbose`. `--yes` is accepted wherever
  `--force` was.

- **Every CLI failure exits 1**, including a bad flag, which used to leave
  through `flag`'s own exit code and print the failure twice.

- **An unknown command or flag is refused before the panel is built**, so a
  typo is reported as a typo rather than as whatever the database had to say
  about being unreachable. `help` no longer needs a working database.

### Fixed

- **A child field in a repeater row is a field.** `Span`, `ReadOnly`, `Disable`,
  `Symbol`, `Min` and `Max` reached the row's markup as nothing at all, so every
  child took the full twelve columns whatever it declared. A row is the form's
  own twelve-column grid now, and `Fieldset` and `Divider` work inside one.

  The modifiers a row cannot honour — `CreationRules`, `UpdateRules`,
  `OnlyOnCreate`, `OnlyOnUpdate`, `Show`, `SavingValue`, `ValuesFunc` — are
  refused by `Verify` rather than ignored at render time.

- **A repeater row added in the browser is live.** A cloned row carried markup
  the component library had not built and this framework had not bound: a select
  was an inert combobox, a date field had no calendar trigger.

- **`HasMany(...).Label(s)`** names the fieldset, instead of the relation's Go
  field name being the only thing a reader sees.

- **A failed `Saved` hook is reported.** The record was written and the hook's
  error was swallowed, so a panel said "Post saved." while the work the hook was
  there to do had not happened. The save still stands — it is already committed
  — but the toast is a warning carrying the hook's message.

- **The command palette says when a section could not answer.** A resource whose
  query timed out or was refused contributed nothing, which read as "no
  matches" — the same answer a typo gets.

- **`admin:create-user` prepares the tables it writes to.** On a database that
  had not been served yet it failed with the driver's own "no such table:
  admin_users".

- **`admin:create-user` does not block on a prompt nobody can answer.** It asked
  for a password whenever `-password` was absent, including when stdin was a
  pipe, where the prompt either waits forever or takes the next line of a
  script as the password. It now refuses and names the flag.

- **The scaffolder refuses a field type it does not have.** `--fields
  "title:strnig"` fell through to a string column and exited 0, so the spec was
  wrong and the generated model compiled, ran, and was wrong with it.

- **The operation log is written on the request that caused it.** It went out
  from a goroutine of its own, which outlived the request, the panel and the
  process shutting down: one unbounded goroutine per mutating request, and
  writes that could land after the panel had stopped.

### Upgrading

Four things to look at, none of them large:

- **`Verify` now opens the database.** It always ran `Build`, so it always
  needed one; what is new is a query per declaration. Wherever that is not
  wanted — a check run before migrations, a place where a round trip is not
  free — set `Config.DisableQueryProbe`. A table that does not exist yet is
  already passed over.
- **Call `Admin.Close` in tests.** Every panel a suite builds otherwise keeps a
  goroutine polling a database the test has closed.
- **A test asserting on an error message will need rewording.** The text is
  what changed; which errors are reported, and when, is the same.
- **Anything reading the scaffolder's stdout** now gets bare paths rather than
  `created: <path>`. `--verbose` restores the commentary, not the old shape.

The `contrib/*` modules are unchanged and stay at `v0.1.0`. They require the
framework at a minimum, not a maximum, so requiring `v0.2.0` alongside them is
all it takes.

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
