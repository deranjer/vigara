# Compose implementation and validation checkpoint — 2026-10-25

This checkpoint records implementation and observed validation, not milestone closure.
192.168.1.97 is the authorized test environment. 192.168.1.150 is production
and was used only for a read-only container-name inventory. No production
configuration or application was changed.

## Delivered in this checkpoint

- Initial immutable logical-profile binding to the device already approved for
  its stacks. Exclusive assignments at epoch 1 carry the exact saved profile
  and assignment into preview and deployment. Historical selection cannot
  bypass current ownership requirements; changed manifests stop queued jobs.
- Agent preflight checks required absolute paths, exact filesystem mount
  boundaries, device nodes, and pre-existing external network/volume attributes
  before preview and apply. Requirements are checked in the agent's namespace.
- Explicit central secret reveal/edit against a persisted exact draft or saved
  revision. Responses are uncached and audit persistence is required. The UI
  starts masked, clears on hide/change/navigation, and discards late responses.
- Image digests captured during preview, persisted by the server for 15 minutes,
  and applied without resolving mutable tags again. Deployment results bind
  job ID, commit, complete configuration revision, preview token, image map and
  Compose readiness; mismatches remain uncertain rather than successful.
- Journal reuse checks and exclusive immutable override files prevent blind
  replay and symlink overwrite. Interrupted outcomes require reconciliation.
- Offline metadata-only recovery planning from a local Git checkout, described
  in [Compose recovery planning](compose-recovery.md).

## Observed evidence

- Focused PostgreSQL tests passed with migration 21, profile activation,
  initial-host restrictions, historical bypass refusal, pinned job inputs,
  uncertain-result reconciliation and refusal after external profile changes.
- Linux fixture tests exercised missing mounts, exact mount boundaries,
  symlinks, network/volume mismatches, journal corruption/interruption,
  failed application, digest pinning and repeated job execution.
- A real disposable Compose project on 192.168.1.97 ran the exact previewed
  nginx image digest, passed its nginx health check, and retained the same
  container ID on retry of the recorded job. The project had no published
  host ports or shared storage and was cleaned up after validation.
- Real SOPS 3.9.4/age tests passed for central, target and independent recovery
  recipients, unrelated-key refusal, tampering, rotation, permissions and
  cleanup. Real Linux/PostgreSQL endpoint tests proved target-independent
  reveal/edit, metadata-only auditing, refusal on audit failure and exact
  ciphertext version fencing.
- Frontend typecheck, lint, 55 tests, production build and documentation build
  passed. Existing frontend bundle-size warnings remain.

## Remaining acceptance gates

ENG-177/179/180/181 remain in progress. Initial binding is not ownership
transfer. Replicas, epoch changes, old-host fencing, managed resource creation,
capability/startup-dependency execution, backup/deployment storage coordination
and complete release rollback remain unfinished. A Compose wait success means
services are running or pass configured health checks; it does not prove
application data correctness.

ENG-183 still requires the complete existing-application adoption → UI edit →
encrypted Git save → exact revision deployment drill, including Git outage,
offline target, lost replies and restart. The component drills above do not
substitute for this combined workflow.

ENG-182/184 still require resumable restore orchestration, safe replacement
ownership, representative data verification, second-server profile import and
independent recovery without the original Vigara server/database. The offline
planner always reports NOT_EXECUTABLE and does not restore or deploy anything.

The protected central editor identity and independent recovery-key custody are
operator prerequisites. No private keys are stored in Git or provisioned to
production by this checkpoint.

## TP-Link observations

Both management interfaces returned HTTP 200 from 192.168.1.97. The logged-in
router UI confirmed Archer BE550 v1.0 firmware 1.3.1, EasyMesh enabled and Main
Router mode. The RE653BE v1.0 reported firmware 1.0.8 and a wired connection.
No SNMP control was found in the inspected menus; no stable automatic
authenticated telemetry interface was established. See [TP-Link support](tplink.md)
for bounded reachability checks and the required test-agent setup. These
observations do not claim automated WAN/client/mesh telemetry support.
