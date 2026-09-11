# Releasing

Releases are managed by [release-please](https://github.com/googleapis/release-please). The process is automated — maintainers only need to merge a release PR and confirm a few things beforehand.

## How it works

1. Every merge to `main` that includes a conventional commit prefix (`feat:`, `fix:`, `perf:`, etc.) causes release-please to open or update a release PR titled `release: vX.Y.Z`.
2. The release PR contains an updated `CHANGELOG.md` and a bumped version in `.release-please-manifest.json`.
3. Merging the release PR tags the commit, which triggers CI to verify the tag matches the manifest and to undraft the GitHub Release.

### Version bump rules

| Commit prefix | Bump |
|---|---|
| `feat:` | minor (patch if pre-1.0) |
| `fix:`, `perf:`, `refactor:` | patch |
| `feat!:` or `BREAKING CHANGE:` footer | major |

## Cutting a release

1. Review the open release-please PR — confirm the version and changelog look right.
2. Approve and merge the PR.
3. CI will tag the commit, verify versions match, and publish the GitHub Release automatically.

You can also trigger a release manually via **Actions → release-please → Run workflow**, choosing the bump type or supplying an explicit version.

## Changelog entries

release-please builds the changelog from commit messages since the last tag using the sections defined in `release-please-config.json`:

| Prefix | Section | Visible |
|---|---|---|
| `feat:` | Added | yes |
| `fix:` | Fixed | yes |
| `perf:`, `refactor:` | Changed | yes |
| `revert:` | Removed | yes |
| `docs:` | Documentation | yes |
| `test:`, `ci:`, `chore:` | Miscellaneous | hidden |

### Overriding a changelog entry

When a commit message is too terse or developer-focused, you can override the changelog entry from the PR body. Add this block anywhere in the PR description:

```
BEGIN_COMMIT_OVERRIDE
feat: describe the change in user-facing terms
END_COMMIT_OVERRIDE
```

release-please uses this text instead of the commit message when building the changelog.

### Breaking changes

Add a `BREAKING CHANGE:` footer to the commit body (not the subject line):

```
feat!: remove the Foo option

BREAKING CHANGE: the Foo option has been removed. Use Bar instead.
```

This produces a major version bump and a dedicated breaking changes section in the changelog.
