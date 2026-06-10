# Plugin versioning and releases: goclawkit handoff

Status: MOSTLY LANDED. The CONVENTION this doc specifies now lives in `docs/sdk-spec.md`
(semver `version`; bare `v<semver>` release tags; the `@<ref>` install pin; the
`CHANGELOG.md` convention), and the matching host-side notes are in
`goclaw/docs/plugin-updates.md`. The tag-grammar decision (section 3) is SETTLED: one plugin
per repo, so a bare `v<semver>` tag is unambiguous and no per-plugin namespacing is needed.
Remaining: the optional version-bump CI lint (section 5), and cutting the first real tags /
bumping the gmail plugins (section 6, items 5-6) once goclaw-gmail is split into one-plugin
repos. This doc is kept as the rationale record.

The goclaw side (the RFC for HOW the host detects and surfaces updates, never auto-applying)
is `goclaw/docs/plugin-updates.md`. goclaw owns the MECHANISM (provenance, checking, the
operator surface); goclawkit owns the CONVENTION plugin authors follow.

## 0. Why goclawkit has to do anything here

goclaw can check "is there a newer version of this plugin" only as well as authors publish
version signals. Today they publish almost nothing usable:

- `plugin.yml` `version` is documented as **free-form** (`docs/sdk-spec.md`: "the plugin's
  own version, free-form") and in practice is left at `1.0.0`. `goclaw-gmail` shipped a NEW
  `gmail-tools` plugin while the version stayed `1.0.0`; the channel and tool both read
  `1.0.0` regardless of changes. A free-form, un-bumped version is useless as an update
  signal.
- There are no release tags. The only "version" upstream is the moving tip of a branch,
  which is noisy (every README edit looks like an update) and, for a monorepo, mostly
  irrelevant (most commits do not touch a given plugin's subdir).

So the update check's strongest signal (release tags, see the goclaw RFC section 2b) does
not exist until goclawkit establishes the convention and authors adopt it. That is this
work.

## 1. What to deliver (summary)

1. **Versioning discipline:** make `plugin.yml` `version` (and the handshake `Info.Version`)
   a REQUIRED semver string that authors MUST bump on any behavior change. Document it, and
   ideally enforce it.
2. **Release tags as the blessed distribution:** define a semver git-tag convention for
   releasing a plugin, including how a tag names WHICH plugin in a multi-plugin repo (the
   open decision, section 3).
3. **Install-by-tag:** document the `@<tag>` install/update spec the goclaw side will add, so
   authors and operators share one notation.
4. **Optional but recommended:** a `CHANGELOG.md` convention and a CI lint that fails when
   code changed without a `version` bump, to make the discipline enforceable rather than
   aspirational.

## 2. Versioning discipline (semver, required, bumped on change)

Change the manifest contract from "free-form version" to:

- `version` MUST be **semver** (`MAJOR.MINOR.PATCH`, e.g. `1.4.0`). No `v` prefix in
  `plugin.yml` (the tag carries the `v`, section 3); the manifest holds the bare semver.
- `version` MUST agree with the handshake `Info.Version` (already required, keep it).
- Authors MUST bump it on any behavior change:
  - PATCH: a fix with no interface change.
  - MINOR: new capability, backward compatible (e.g. gmail-tools adding a 5th tool).
  - MAJOR: a breaking change to the plugin's tools/inputs/behavior the agent relies on.
- A version that does not move across a real change is a BUG (it makes the manifest-version
  update signal lie). Call this out explicitly in the docs.

Where to document: update `docs/sdk-spec.md`'s `plugin.yml` schema section (the `version`
bullet) and the `Info.Version` field comment (change "free-form" to "semver, bumped on
change"). Both currently say free-form.

## 3. THE decision: how a tag names a plugin in a multi-plugin repo

This is the one genuinely open design question, and it must be settled before tags can be
goclaw's primary update signal. The problem: `goclaw-gmail` is ONE repo shipping TWO plugins
(`cmd/gmail` the channel, `cmd/gmail-tools` the tool). A repo-wide tag `v1.3.0` cannot say
WHICH plugin it releases, and the two plugins version independently (you might fix
gmail-tools without touching the channel). So a flat repo tag is ambiguous.

Three options, with trade-offs. Pick one and make it THE convention (authors and the goclaw
checker must agree on the exact grammar).

### 3a. Path-prefixed tags (RECOMMENDED): `<plugin>/v<semver>`

A tag is `<plugin-name>/v<semver>`, e.g. `gmail/v1.3.0`, `gmail-tools/v2.0.1`. The prefix is
the plugin's `name` (== its `plugin.yml` name == its install subdir leaf, keep these
aligned). One repo can carry independent tag lines per plugin.

- **Pro:** unambiguous, each plugin releases on its own cadence, scales to N plugins per
  repo, and it is a real-world convention (Go monorepos tag submodules as `module/vX.Y.Z`;
  goreleaser-monorepo and others use exactly this).
- **Pro for the checker:** to find "latest gmail release", `git ls-remote --tags <url>` and
  filter to `gmail/v*`, pick the highest semver. Clean and deterministic.
- **Con:** authors must remember the prefix; a bare `v1.3.0` push does not release anything
  in this scheme (which is arguably good: it prevents an accidental repo-wide "release").
- **Single-plugin repos** (the `goclaw-roll` layout, plugin.yml at root) use the SAME grammar
  with the plugin name, e.g. `roll/v1.0.0`, OR we allow a bare `v<semver>` as a shorthand
  that means "the single plugin in this repo." Decide whether to allow the bare shorthand or
  require the prefix uniformly (uniform is simpler for the checker; the shorthand is friendlier
  for the common single-plugin case). Leaning: allow bare `v<semver>` ONLY when the repo has
  one plugin (plugin.yml at root), require the prefix otherwise.

### 3b. One repo, one plugin (sidestep the problem)

Mandate that each plugin lives in its own repo, so a flat `v<semver>` tag is unambiguous.
This is clean for tags but breaks the existing `goclaw-gmail` two-plugins-one-repo layout
the SDK explicitly supports (`docs/sdk-spec.md` documents `#cmd/<name>` subdir installs and
"a repo may host several plugins"). Rejecting the monorepo layout to make tagging easy is the
tail wagging the dog. Not recommended, but noted: it is the simplest if we were willing to
drop monorepos.

### 3c. Tag carries the version, manifest disambiguates

Use a flat `v<semver>` tag and rely on the per-plugin `plugin.yml` `version` for which-plugin
resolution. This conflates the repo's release with each plugin's version and falls apart when
two plugins in the repo are at different versions. Rejected: it cannot express independent
versioning, which is the whole reason the monorepo case is hard.

**Recommendation: 3a (path-prefixed `<plugin>/v<semver>`), with a bare `v<semver>` shorthand
allowed only for single-plugin-at-root repos.** This is the decision to ratify with the
goclaw side before goclaw implements tag-based checks, because goclaw's `plugin check` must
parse exactly this grammar.

## 4. Install-by-tag notation (shared with goclaw)

goclaw will extend the install spec with an optional ref pin (see goclaw RFC section 2b/5).
Authors and operators should use one notation; document it here so it matches:

```
goclaw plugin add <git-url>#<subdir>@<ref>
```

- `<ref>` is a tag (`gmail/v1.3.0`), or a bare `v1.3.0` for a single-plugin repo, or a raw
  commit sha for pinning without a release.
- No `@<ref>` means "default branch HEAD" (today's behavior), which gets only the WEAKER
  update signal (manifest version, section 2a of the goclaw RFC; there is no commit-drift
  fallback), and should be discouraged in docs in favor of a tag.
- `goclaw plugin update <name>` re-installs at the newer tag the check reported, through the
  full sandbox (the update is re-vetted untrusted code, not a fast path).

goclawkit's job is to document this notation in `sdk-spec.md` alongside the existing
`#cmd/<name>` subdir text, and to make the example plugins (roll, irc) shippable as tagged
releases so there is a worked reference.

## 5. Optional: enforce the discipline (so it is not aspirational)

A convention authors are merely asked to follow will rot (the `1.0.0`-forever problem we
already have). Two enforcement aids, in increasing strength:

- **`CHANGELOG.md` convention:** each plugin keeps a `CHANGELOG.md` with a section per
  version. `goclaw plugin check` can then show WHAT changed, not just that something did
  (goclaw RFC section 6). Low effort, high operator value. Document the format (Keep a
  Changelog style is fine).
- **A version-bump CI lint:** a goclawkit-provided check (a script or a small `go test`
  helper) that fails CI when a plugin's tracked source changed between two refs but its
  `plugin.yml` `version` did not. This turns "you should bump version" into "CI is red until
  you do." This is the highest-leverage piece for making the update signal trustworthy. Spec
  it as: given the previous release tag for this plugin and HEAD, if any file under the
  plugin's dir changed and `version` is unchanged, fail with a clear message. (Authors opt in
  by adding it to their repo's CI, like the existing golangci-lint job.)

A lighter SDK-side option: a `plugin.Info` self-check or a `goclawkit` command that prints a
plugin's `{name, version, kind}` so a release script can assert the tag matches the manifest
version (`gmail/v1.3.0` tag must correspond to `version: 1.3.0` in `cmd/gmail/plugin.yml`).
This catches the "tagged v1.3.0 but forgot to bump the manifest" mistake.

## 6. Concrete checklist for the implementing session

1. `docs/sdk-spec.md`: change `version` from "free-form" to "semver, REQUIRED, bumped on
   change", in both the `plugin.yml` schema section and the `Info.Version` field comment. Add
   a "Releasing a plugin" subsection documenting the tag grammar chosen in section 3 and the
   `@<ref>` install notation in section 4.
2. Ratify the section-3 tag grammar WITH the goclaw side (so `goclaw plugin check`'s parser
   and these docs agree). This is a cross-repo decision; do not finalize one without the
   other.
3. Add the `CHANGELOG.md` convention (section 5) to the SDK docs as recommended-not-required.
4. Decide whether to ship the version-bump CI lint (section 5) as a goclawkit-provided helper
   now or defer it; if shipping, add it and wire it into the example plugins' CI as the
   reference.
5. Cut reference tagged releases for the worked example plugins (`roll`, `irc`) using the
   chosen grammar, so there is a real example for operators and a fixture for goclaw's
   `plugin check` to test against.
6. Bump the gmail plugins' versions for real (they are stuck at `1.0.0`) and tag them, as the
   first non-example application of the convention.

## 7. What NOT to do

- Do NOT build any update-CHECKING or update-INSTALLING logic in goclawkit. That is goclaw's
  job (the host owns provenance and the operator surface). goclawkit only defines the
  convention authors follow and the docs/lints that keep them honest.
- Do NOT make goclawkit reach out to networks or registries. There is no plugin registry;
  distribution is "a git repo with tags." Keep it that way (operator clones/installs from a
  URL they chose).
- Do NOT auto-bump versions or auto-tag in the SDK. Releasing is a deliberate author action,
  same principle as goclaw never auto-applying an update.

## 8. The decision this handoff turned on (RESOLVED)

The tag grammar (section 3) was the one thing everything else keyed off. RESOLVED in favor
of **one plugin per repo, bare `v<semver>` tags**, NOT the path-prefixed `<plugin>/v<semver>`
form: the multi-plugin case is avoided by structure (each plugin gets its own repo) rather
than by a namespacing grammar. The example plugins were already split out this way
(`goclaw-roll`, `goclaw-irc`, `goclaw-webhook`); `goclaw-gmail` (two plugins, one repo) is
to be split likewise. With no monorepos, a flat `v<semver>` is unambiguous, and goclaw's
checker needs only the one grammar. Documented in `docs/sdk-spec.md` ("Releasing a plugin").
