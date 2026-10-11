# Local Restic snapshot recovery

`vigira-restic` is a local operator command for inspecting and recovering file snapshots from the agent's configured local Restic profiles. It uses the same private agent config as `vigira-agent` (`--config PATH`, `VIGIRA_AGENT_CONFIG`, or the normal per-user config file). A profile must be enabled and configured locally. The server cannot supply a repository, source path, credential, executable, or restore destination to this command.

Build it with `go build -o vigira-restic ./apps/agent/cmd/vigira-restic`. Select a profile explicitly for every operation:

```text
vigira-restic snapshots --profile home
vigira-restic browse --profile home --snapshot <64-character-snapshot-id>
vigira-restic check --profile home
vigira-restic restore --profile home --snapshot <64-character-snapshot-id> --target /mnt/recovery/new-empty-directory
vigira-restic retention-plan --profile home --keep-last 30
```

`snapshots` returns the snapshot ID, timestamp, host, paths, tags, and Restic version. `browse` lists the selected snapshot's filesystem entries. Both identify snapshots by their complete lowercase 64-character ID; short IDs are rejected. `check` runs Restic's repository integrity check for repository structure and indexes. The profile credential is passed through the child process environment and is not printed. Restic output is bounded, and process errors are replaced by a generic message so paths and backend details are not copied into logs.

Restore requires an existing, empty destination directory. The command rejects a target that is a symlink, has a symlink in its path, is nested in or contains the repository or any configured backup source, or resolves through a source symlink. It passes the destination and complete snapshot ID as separate arguments and does not enable overwrite. Check the destination and available disk space before starting; the restored file tree is placed under that directory. Restic's normal repository lock remains in effect, and the tool never unlocks a repository automatically.

`retention-plan` runs `forget --dry-run --json --keep-last N` and returns Restic's bounded JSON plan. It does not forget snapshots or prune repository data. Destructive retention apply and prune remain outside this operator command until a separate maintenance authority and review flow is designed. No routine backup automatically changes retention or performs maintenance.

The tool supports local and network-drive repositories from the profile. It is pinned to Restic `0.19.1`, as is the agent runner. The JSON command behavior follows [Restic's scripting documentation](https://restic.readthedocs.io/en/stable/075_scripting.html). This is file-level recovery; it does not provide bare-metal recovery, remote web restore, or arbitrary server-directed restore paths.

## Run the disposable recovery drill

The unit tests use a fake process; this opt-in drill exercises the managed runner with real Restic `0.19.1`. From the repository root, build the pinned test image and run the one focused test:

```powershell
$repo = (Resolve-Path '.').Path
docker build -f internal/restic/testdata/Dockerfile.integration -t vigira-restic-drill .
if ($LASTEXITCODE -ne 0) { throw 'Could not build the Restic drill image' }
docker run --rm --mount "type=bind,source=$repo,target=/src,readonly" --workdir /src -e VIGIRA_RESTIC_DRILL=1 vigira-restic-drill go test ./internal/restic -run '^TestResticRecoveryDrill$' -count=1 -v
if ($LASTEXITCODE -ne 0) { throw 'Restic recovery drill failed' }
```

The drill initializes a new encrypted repository, creates two snapshots from a disposable fixture, lists and browses the first snapshot, checks the repository, restores that explicit snapshot to a new empty directory, and compares the restored file contents. It then requests a keep-last-one retention plan and confirms that both snapshots remain. The repository, restore, and fixture are created under the test's temporary directory and removed when it exits. The test never invokes retention apply, prune, or any operation against an operator profile. Docker and access to the pinned image are required; to run it outside Docker, install Restic `0.19.1` and set `VIGIRA_RESTIC_DRILL=1` when running the same focused Go test.
