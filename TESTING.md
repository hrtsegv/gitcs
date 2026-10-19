# Testing gitcs

Run the portable regression suite and static checks with the Go version from
`go.mod`:

```sh
go test -race -coverprofile=coverage.out ./...
go vet ./...
go tool cover -func=coverage.out
```

GitHub Actions runs the suite on Linux, macOS, and Windows. Tests requiring
native Git skip when it is unavailable; missing-Git behavior is tested separately.
Unix terminal and permission tests run on Linux/macOS. The terminal tests check
both normal exit and interruption, including terminal attribute restoration.

## Main failure scenarios

| Area | Regression coverage |
| --- | --- |
| Arguments and identity | Invalid dates, reversed ranges, future end dates, invalid authors/scopes/widths, missing paths/Git/config, local/global/conditional Git configuration, explicit author overrides |
| Discovery | Empty/missing/unreadable roots, unreadable subtrees, nested repositories, all dependency exclusions, explicit excluded roots, symlink roots/parents/loops/aliases |
| History and counts | Inclusive dates, author versus committer timestamps, DST/timezones, future and nonmonotonic dates, local branches versus HEAD, merge ancestry, shared commits and clones |
| Storage failures | Shallow history, linked worktrees, initialized submodules, corrupt/missing objects and refs, broken worktree metadata, partial results and complete failure |
| Report output | Empty results, exact counts, narrow/wide calendars, large counts, month/year boundaries, color-free output |
| Explorer state | Incremental loading, stale results, refresh/recovery, repository removal, search/filter counts, cancelled-search reading position, navigation and viewport bounds |
| Patches | Root/empty/merge/binary/mode/deletion changes, first-parent comparisons, shallow/missing history, clone fallback, missing Git, helper/replace isolation, output/error bounds, retries and caching |
| Cancellation and terminal content | Blocked Git config reads, active patch subprocesses, scan cancellation, quit/Ctrl+C, tiny/resized terminals, Unicode, ANSI/control injection, `NO_COLOR` |

## Read-only checks on real repositories

The opt-in integration test compares native Git's hashes and author timestamps
with the report, printable CLI, and explorer. It checks local/HEAD scopes, UTC,
Beirut and Los Angeles dates, a single day, an empty historical range, and one or
multiple active authors. It compares daily totals and individual commit hashes,
so matching overall totals cannot hide a disagreement between dates.

```sh
GITCS_TEST_REAL_ROOT=/home/knbr13/dev go test -run '^TestRealDataAgainstNativeGit$' -v -count=1 ./...
```

The real-data test skips unless the variable is set. It reads local objects only
and has an eight-minute deadline. Keep repository refs stable during the audit.
The normal fixture suite uses the race detector; the real-data matrix can run
without it to keep repeated full-history scans reasonably fast.

On 2026-10-19, the real-data audit processed 69 repositories. Native Git, the
report, CLI, and explorer agreed on 1,077 unique commits for the default range;
the active-author cases agreed on 447 and 618 commits. These are snapshot values
and increase as new commits are created.

The terminal audit also exercised calendar/list/detail navigation, all-date and
repository filters, live/accepted/cancelled searches, warnings/help, refresh,
patch paging/panning/cache/reload/back, missing-Git errors, and normal/interrupted
exit on real data. Real root, merge, and empty commits were opened in the viewer.
The same core failure paths have portable fixtures in the normal test suite.

## Release build and archive

Use a separate output directory to exercise the release build and ZIP packaging:

```sh
make all OUTPUT_DIR=/tmp/gitcs-release-check
unzip -t /tmp/gitcs-release-check/gitcs_binaries.zip
```

The archive contains eight binaries: Linux and Windows amd64/arm64/386, and
macOS amd64/arm64. This checks building and packaging; publishing a GitHub
release is a separate workflow requiring a release tag and name.
