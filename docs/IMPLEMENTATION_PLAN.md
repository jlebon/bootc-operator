# Bootc Operator — Implementation Plan

This document defines the step-by-step implementation milestones for the bootc operator. Each milestone builds on the previous one and includes validation criteria to confirm correctness before proceeding.

For architectural details (CRD schemas, state machines, reconciliation loops, daemon logic, bootc integration), see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Milestone 1: Bootstrap & CRDs

### Steps

- [ ] Initialize the project with Kubebuilder using domain `bootc.dev`.
- [ ] Scaffold the BootcNodePool API (group `bootc.dev`, version `v1alpha1`, kind `BootcNodePool`). Mark it cluster-scoped.
- [ ] Scaffold the BootcNode API (group `bootc.dev`, version `v1alpha1`, kind `BootcNode`). Mark it cluster-scoped.
- [ ] Define the BootcNodePool spec and status types as specified in ARCHITECTURE.md. Add kubebuilder markers for validation, defaults, enums, and print columns.
- [ ] Define the BootcNode spec and status types as specified in ARCHITECTURE.md. Add kubebuilder markers for validation, enums, and print columns.
- [ ] Define shared types: `RebootPolicy` enum, `ImagePullSecretReference`, `RolloutConfig`, `DisruptionConfig`, `HealthCheckConfig`, `BootEntryStatus`.
- [ ] Run `make manifests generate` to produce CRD YAML and DeepCopy methods.
- [ ] Set up the two-binary layout: `cmd/operator/main.go` (controller-runtime manager) and `cmd/daemon/main.go` (standalone poll-based agent).
- [ ] Create the Dockerfile with a multi-stage build that produces both binaries.

### Validation

- [ ] Build the operator and daemon container images.
- [ ] Deploy the operator and daemon to a cluster. Verify the pods start and report healthy (readiness/liveness probes pass).
- [ ] Apply the generated CRDs to a cluster. Verify both `bootcnodes.bootc.dev` and `bootcnodepools.bootc.dev` appear in `kubectl get crds`.
- [ ] Create a valid BootcNodePool resource with all required fields. Verify it is accepted.
- [ ] Create a BootcNodePool missing the required `image` field. Verify the API server rejects it.
- [ ] Inspect a created BootcNodePool and confirm default values are applied: `maxUnavailable=1`, `rebootPolicy=Auto`, `healthCheck.timeout=5m`.
- [ ] Attempt to set an invalid enum value (e.g. `rebootPolicy: Invalid`). Verify rejection.
- [ ] Run `kubectl get bootcnodepools` and confirm the print columns (Image, Phase, Ready, Target, Age) render correctly.

---

## Milestone 2: Daemon Foundation

### Steps

- [ ] Implement the bootc client library (`pkg/bootc/`):
   - `IsBootcHost()`: Detect whether the host is a bootc system by attempting to run `bootc status --json` via nsenter.
   - `Status()`: Execute `bootc status --json --format-version=1` via nsenter, parse the result into a Go struct.
   - `ToBootcNodeStatus()`: Convert the parsed bootc status into a `BootcNodeStatus` for the CRD.
- [ ] Implement the daemon Kubernetes client (`internal/daemon/kubeclient.go`): a thin wrapper around client-go providing `GetNode()`, `GetBootcNode()`, `CreateBootcNode()`, and `UpdateBootcNodeStatus()`. No controller-runtime dependency.
- [ ] Implement the daemon main loop (`internal/daemon/daemon.go`):
   - On startup, call `IsBootcHost()`. If false, log a message and block until shutdown.
   - If bootc host, look up the Kubernetes Node object (for ownerReference) and ensure a BootcNode CRD exists (create if missing, with initial status from `bootc status`).
   - Enter a poll loop: on each tick, GET the BootcNode, run `bootc status --json`, and update the status subresource if anything changed.
- [ ] Wire up `cmd/daemon/main.go`: parse flags (`--node-name`, `--poll-interval`, `--kubeconfig`), build the Kubernetes config, create the daemon, and call `Run()`.
- [ ] Create the DaemonSet manifest (`config/daemon/`): service account, RBAC (get/create/update BootcNodes, get Nodes), and DaemonSet spec with `hostPID: true`, `privileged: true`, and NODE_NAME from the downward API.
- [ ] Write unit tests for the daemon using a mock bootc client and mock Kubernetes client.

### Validation

- [ ] Deploy the daemon to a non-bootc node. Confirm the logs show "Host is not a bootc system, staying idle" and no BootcNode is created.
- [ ] Deploy the daemon to a bootc node. Confirm a BootcNode is created with the correct name (matching the Node), ownerReference pointing to the Node, and `status.booted` populated from `bootc status`.
- [ ] On the bootc node, manually stage an image using `bootc switch --download-only`. Wait for the poll interval and confirm the BootcNode status updates to reflect the staged image.
- [ ] Verify the daemon pod does not crash-loop and produces no API errors in its logs during steady-state operation.

---

## Milestone 3: Controller Foundation

### Steps

- [ ] Implement the Pool Reconciler (`internal/controller/bootcnodepool_controller.go`):
   - Set up watches on BootcNodePool (direct), Node (via map function), and BootcNode (via ownerReference).
   - Add Node watch predicates to filter out high-frequency noise: only trigger on label changes, Ready condition changes, and `spec.unschedulable` changes.
   - Implement the Node-to-pool mapping function: enqueue pools whose nodeSelector matches the node's labels, plus the pool owning the node's BootcNode (to handle label removal).
- [ ] Implement the pool membership sync (reconciliation step 2 from ARCHITECTURE.md):
   - List Nodes matching the pool's `nodeSelector`.
   - List BootcNodes owned by this pool.
   - For new matches: create a BootcNode with ownerReference to the pool and label `bootc.dev/pool=<name>`.
   - For nodes no longer matching: delete the BootcNode.
   - For conflicts (node matched by multiple pools): set Degraded condition with reason NodeConflict on all conflicting pools. Do not create a BootcNode for contested nodes.
- [ ] Implement pool status aggregation (reconciliation step 4): compute `targetNodes` count and set the pool phase to Idle.
- [ ] Wire up the reconciler in `cmd/operator/main.go`: register it with the controller-runtime manager.
- [ ] Add RBAC markers for all required permissions.
- [ ] Write unit tests using envtest: pool creation triggers BootcNode creation, label changes trigger add/remove, conflict detection works.

### Validation

- [ ] Create a BootcNodePool with a nodeSelector. Confirm BootcNodes are created for every matching Node, each with the correct ownerReference and `bootc.dev/pool` label.
- [ ] Add the matching label to a previously unlabeled Node. Confirm a BootcNode is created within seconds.
- [ ] Remove the matching label from a Node. Confirm the BootcNode is deleted.
- [ ] Create a second pool with an overlapping nodeSelector. Confirm both pools enter phase Degraded with condition reason NodeConflict.
- [ ] Verify the controller does not produce excessive reconcile loops in its logs.

---

## Milestone 4: Image Resolution

### Steps

- [ ] Implement the digest resolver (`internal/controller/digest.go`):
   - Use `github.com/google/go-containerregistry` to query container registries.
   - `ResolveDigest(ctx, imageRef, pullSecret) (string, error)`: if the reference is already a digest, return it as-is. If it is a tag, query the registry and return the resolved digest.
   - Support authentication via `kubernetes.io/dockerconfigjson` secrets.
- [ ] Add pull secret reading to the reconciler: when the pool specifies `imagePullSecret`, read the Secret from the operator namespace and pass it to the resolver.
- [ ] Integrate resolution into the reconciliation loop (step 1 from ARCHITECTURE.md):
   - On each reconcile, call `ResolveDigest` with the pool's `spec.image`.
   - Store the result in `status.resolvedDigest`.
   - If the image is a tag, set `RequeueAfter` to re-resolve periodically (e.g. 5 minutes).
- [ ] Propagate the resolved digest to BootcNodes: set `spec.desiredImage` on all owned BootcNodes to the resolved digest.
- [ ] Handle resolution failures: set pool phase to Degraded with a clear error message in the condition. Retry with backoff.
- [ ] Write unit tests for the resolver using a mock registry.

### Validation

- [ ] Create a pool with a digest reference. Confirm `status.resolvedDigest` matches the spec exactly and no registry query is made.
- [ ] Create a pool with a tag reference to a public image. Confirm `status.resolvedDigest` contains a valid digest and all owned BootcNodes have `spec.desiredImage` set to that digest.
- [ ] Create a pool referencing a private image with a pull secret. Confirm resolution succeeds without authentication errors.
- [ ] Create a pool with a nonexistent image reference. Confirm the pool enters phase Degraded with a meaningful error message.
- [ ] (If possible) Push a new image to an existing tag. Confirm the controller detects the change within the requeue interval and updates `status.resolvedDigest` and all BootcNode specs.

---

## Milestone 5: Daemon Staging

### Steps

- [ ] Extend the bootc client library (`pkg/bootc/client.go`):
   - `Stage(ctx, imageRef) error`: run `bootc switch --download-only <image>` via nsenter. Return the error (including stderr) on failure.
- [ ] Implement the daemon state machine for staging (`internal/daemon/daemon.go`):
   - On each poll, compare `spec.desiredImage` against `status.booted.imageDigest`.
   - If they match: set phase to Ready (no-op).
   - If they differ and no staging is in progress: set phase to Staging, call `Stage()`.
   - On successful stage: re-read `bootc status`, populate `status.staged`, set phase to Staged.
   - On failure: set phase to Error with the error message.
- [ ] Implement re-staging: if the daemon is in phase Staged but `status.staged.imageDigest` does not match `spec.desiredImage` (because the pool updated the image), re-enter Staging.
- [ ] Adjust the poll interval: use a fast interval (5s) during Staging, and a slow interval (30s) during Ready or Staged.
- [ ] Implement pull secret propagation: when the BootcNode spec includes a pull secret reference, the daemon reads the Secret via the Kubernetes API and writes it to `/run/ostree/auth.json` on the host before staging.
- [ ] Write unit tests for the staging state machine with a mock bootc client.

### Validation

- [ ] Set `spec.desiredImage` on a BootcNode to a different image than the currently booted one. Confirm the daemon progresses through Ready → Staging → Staged, and `status.staged.imageDigest` matches the desired image.
- [ ] Set `spec.desiredImage` to a nonexistent digest. Confirm the daemon sets phase to Error with a meaningful message and does not crash-loop.
- [ ] While a node is in phase Staged, change `spec.desiredImage` to a third image. Confirm the daemon re-enters Staging and stages the new image.
- [ ] Set `spec.desiredImage` to the currently booted image. Confirm the daemon stays in phase Ready and performs no staging.

---

## Milestone 6: Rollout Orchestration

### Steps

- [ ] Implement the drain manager (`pkg/drain/drain.go`):
   - Wrap `k8s.io/kubectl/pkg/drain` to provide `Drain(ctx, nodeName, timeout) error` and `Uncordon(ctx, nodeName) error`.
   - Before cordoning, check if the node is already unschedulable. If so, record this in a `bootc.dev/was-cordoned` annotation so uncordon restores the prior state.
   - Drain with a bounded per-attempt timeout (~90s). Return an error if the drain is incomplete (e.g. PDB blocks eviction) so the reconciler can requeue and retry.
- [ ] Implement the rollout state machine (`internal/controller/rollout.go`):
   - Track reboot slots per pool: `maxUnavailable` from the pool spec, occupied by nodes with `desiredPhase=Rebooting` or nodes that rebooted but are not yet Ready.
   - Select candidates: nodes in phase Staged, ordered oldest-first.
   - Assign a slot: cordon the node, drain it. If drain succeeds, set `spec.desiredPhase=Rebooting`. If drain fails, requeue.
   - Release a slot: when a node's booted image matches desiredImage and the Node is Ready, uncordon it (respecting the was-cordoned annotation) and clear desiredPhase.
- [ ] Implement the daemon reboot logic (`internal/daemon/daemon.go`):
   - When `spec.desiredPhase=Rebooting`: verify `status.staged.imageDigest == spec.desiredImage`. If they match, run `bootc switch --from-downloaded --apply` (with `--soft-reboot=auto` for Auto policy, without for Full, skip for Never). Set phase to Rebooting.
   - After reboot: the daemon pod restarts, reads `bootc status`, and sets phase to Ready if booted matches desired.
- [ ] Integrate the rollout state machine into the pool reconciler (step 3 from ARCHITECTURE.md).
- [ ] Implement the health check timeout: if a node does not become Ready within `spec.healthCheck.timeout` after being set to Rebooting, mark the pool Degraded and halt the rollout (no new slots assigned).
- [ ] Update pool status aggregation: compute `updatingNodes`, `readyNodes`, `stagedNodes`. Set pool phase to Staging, Rolling, or Ready as appropriate.
- [ ] Write unit tests for the drain manager and rollout state machine.

### Validation

- [ ] With a pool of 1 node and `maxUnavailable=1`: trigger a rollout. Confirm the node is cordoned, drained, set to Rebooting, reboots, comes back Ready, and is uncordoned. Pool phase transitions through Staging → Rolling → Ready.
- [ ] With a pool of 5 nodes and `maxUnavailable=2`: trigger a rollout. Confirm at most 2 nodes are cordoned/rebooting simultaneously. As each completes, the next begins. All 5 eventually reach Ready.
- [ ] Create a PodDisruptionBudget that blocks drain on a node. Confirm the controller retries drain without setting desiredPhase to Rebooting. After removing the PDB, confirm the rollout proceeds.
- [ ] Simulate a node that does not become Ready after reboot (e.g. block kubelet). Confirm the pool enters phase Degraded after the health check timeout and no additional nodes are assigned reboot slots.
- [ ] Manually cordon a node before a rollout. After the rollout completes, confirm the node remains cordoned (was-cordoned annotation preserved prior state).

---

## Milestone 7: Polish & Testing

### Steps

- [ ] Build an end-to-end test harness (`test/e2e/`):
   - Full rollout lifecycle: pool creation → tag resolution → staging → rolling reboots → all nodes Ready.
   - Tag update detection and re-rollout.
   - Rollback: change pool image back to a previous digest.
   - Staging failure recovery.
   - Health check timeout and Degraded state.
   - PDB blocking and drain retry.
   - Multi-pool with non-overlapping selectors.
   - Pool deletion and cleanup.
- [ ] Add status conditions to both CRDs using `metav1.Condition`:
   - BootcNodePool: Available, Progressing (with progress message), Degraded.
   - BootcNode: ImageStaged, Healthy.
- [ ] Add Kubernetes event recording at key transitions:
   - Pool events: ImageResolved, RolloutStarted, RolloutComplete, UpdateAvailable.
   - Node events: Staging, Staged, Draining, Rebooting, Ready, StagingFailed.
- [ ] Implement error handling across all components:
   - Transient errors (network, API server): retry with exponential backoff.
   - Permanent errors (bad image reference): Degraded with clear message, no retry loop.
   - bootc command failures: Error phase with stderr in the message field.
- [ ] Create example manifests (`config/samples/`): simple pool with a public tag, production pool with digest pinning and pull secret, multi-pool setup.
- [ ] Update README.md with a quickstart guide, deployment instructions, and troubleshooting section.

### Validation

- [ ] Run the full E2E test suite. All scenarios pass.
- [ ] Inspect pool and node conditions during a rollout. Confirm conditions accurately reflect state at each phase transition, including progress messages (e.g. "3 of 5 nodes staged, 1 rebooting").
- [ ] Inspect Kubernetes events during a rollout. Confirm events are logged at each key transition.
- [ ] Trigger each error scenario (invalid image, staging failure, health timeout). Confirm graceful handling: no panics, clear messages, correct phase/condition.
- [ ] Delete a pool. Confirm all owned BootcNodes are garbage-collected, all nodes are uncordoned, and no resources are orphaned.
- [ ] Follow the quickstart in README.md from scratch. Confirm a new user can deploy the operator and complete a rollout without prior knowledge.

---

## Milestone Dependencies

```
M1 (CRDs)
├── M2 (Daemon) — needs BootcNode CRD
└── M3 (Controller) — needs both CRDs
    └── M4 (Resolution) — extends controller
        └── M5 (Staging) — daemon uses resolved digest
            └── M6 (Rollout) — controller + daemon coordination
                └── M7 (Testing) — validates all prior work
```

No milestone should be considered complete until its validation criteria pass.
