# Bootc Operator - Design Plan

## Resources Consulted

- `Bootc Operator.md` -- design doc with problem statement and proposal
- `MCO-2121.xml` -- Jira ticket with discussion comments
- **bootc codebase** -- CLI commands, `bootc status --json` schema
  (`org.containers.bootc/v1` BootcHost), soft reboot, download-only,
  rollback, overlays, sysexts
- **MCO codebase** (`openshift/machine-config-operator`) -- on-cluster
  layering architecture, MCD DaemonSet pattern, node annotation
  coordination, MachineConfigNode CRD, drain logic, Node Disruption
  Policies
- **kured codebase** (`kubereboot/kured`) -- DaemonSet reboot
  coordination, distributed locking via annotations, drain integration,
  sentinel file detection
- **trusted-execution-clusters/operator** (GitHub) -- hybrid Rust+Go
  operator pattern (Go for CRD types, Rust for runtime)
- **openshift/api** repo -- API type conventions, `ImageDigestFormat`
  validation, `OperatorSpec`/`OperatorStatus` patterns, condition
  patterns, enum conventions, discriminated unions
- https://github.com/openshift/enhancements/blob/master/CONVENTIONS.md
  -- cluster conventions, operator patterns, resource requests,
  tolerations, priority classes, upgrade/reconfiguration requirements
- https://github.com/openshift/enhancements/blob/master/dev-guide/api-conventions.md
  -- CRD API design conventions: no booleans, no pointers for optional
  CRD fields, godoc style, discriminated unions, defaulting for config
  vs workload APIs, validation markers + godoc documentation requirements
- https://kubernetes.io/docs/concepts/extend-kubernetes/operator/ --
  K8s operator pattern documentation, recommended frameworks
- https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
  -- upstream K8s API conventions: level-triggered reconciliation,
  declarative spec/status, naming, etc.
- **kubebuilder codebase** (`kubernetes-sigs/kubebuilder`) -- project
  scaffolding, controller patterns, CRD markers, testing with envtest,
  Makefile workflow, multi-binary considerations, webhook patterns

## Overview

A Kubernetes operator that surfaces bootc features to manage bootc-based nodes.
It orchestrates image rollouts across clusters with soft reboot, auto rollback,
and pre-staging. It is not a build system -- users build images with their
existing CI/CD pipelines.

**Language:** Go with clean, importable packages (enables MCO to import logic
during transition period; enables eventual MCO replacement as a separate
operator).

**API Group:** `bootc.dev`

**Target:** Vanilla K8s first. OpenShift compatibility via MCO delegation later.

**Framework:** kubebuilder v4 (controller-runtime v0.23, controller-gen v0.20).

## Architecture

```
               ┌─────────────────────────────────┐
               │  bootc-operator                  │
               │  (Deployment, 1 replica)         │
               │                                  │
               │  Reconcilers:                    │
               │  - BootcNodePool controller      │
               │  - BootcNode controller          │
               │  - Image digest resolver         │
               │  - Node drain manager            │
               └───────────────┬─────────────────┘
                               │
               Reads/writes CRDs + node objects
                               │
         ┌─────────────────────┼─────────────────────┐
         │                     │                     │
  ┌──────┴─────────┐   ┌──────┴─────────┐   ┌──────┴─────────┐
  │ BootcNodePool  │   │ BootcNode      │   │ Node           │
  │ CRD            │   │ CRD (per node) │   │ (cordon/       │
  │ (user-facing)  │   │ (operator-     │   │  drain/        │
  │                │   │  managed)      │   │  uncordon)     │
  └────────────────┘   └───────┬────────┘   └────────────────┘
                               │
               Daemon polls BootcNode CRD
                               │
  ┌────────────────────────────┼────────────────────────────┐
  │                            │                            │
  │  ┌──────────────┐   ┌──────────────┐   ┌──────────────┐│
  │  │ bootc-daemon │   │ bootc-daemon │   │ bootc-daemon ││
  │  │ (node-1)     │   │ (node-2)     │   │ (node-3)     ││
  │  │              │   │              │   │              ││
  │  │ Polls ~30s,  │   │ Polls ~30s,  │   │ Polls ~30s,  ││
  │  │ runs bootc,  │   │ runs bootc,  │   │ runs bootc,  ││
  │  │ reports      │   │ reports      │   │ reports      ││
  │  │ status       │   │ status       │   │ status       ││
  │  └──────────────┘   └──────────────┘   └──────────────┘│
  │  (DaemonSet — no API watches, periodic GET only)       │
  └────────────────────────────────────────────────────────┘
```

### bootc-operator (Deployment)

Single-replica controller-runtime Manager with leader election. Uses the
standard kubebuilder reconciler pattern. Must be able to resume a rollout
from any intermediate state (nodes may be cordoned/drained).

Responsibilities:
- **Deploy and manage the daemon DaemonSet** (MCO pattern): operator creates
  the DaemonSet with the correct daemon image, RBAC, and privileged security
  context. On operator upgrade, the daemon DaemonSet is updated too. The daemon
  image reference comes from an env var (`DAEMON_IMAGE`) on the operator
  Deployment, set during installation. The DaemonSet is an owned resource of
  the operator (or of a singleton config resource) so it is cleaned up on
  uninstall. The DaemonSet runs on all nodes by default; non-bootc nodes
  can be excluded by adding the label `node.bootc.dev/skip` (the DaemonSet
  uses a `nodeAffinity` with `DoesNotExist` on this label). On nodes
  without bootc, the daemon detects this and stays idle (no BootcNode is
  created).
- Watch `BootcNodePool` CRDs for desired state
- Watch `BootcNode` CRDs (created by the daemon) to discover bootc-capable nodes
- Watch `Node` objects for label changes and join/leave events
- Resolve image tags to digests (periodic re-resolution)
- Claim BootcNodes for pools (set spec + `bootc.dev/pool` label on matching
  BootcNodes); release them when they no longer match (clear spec + label)
- Orchestrate rolling updates (maxUnavailable, batch ordering)
- Handle node drain/cordon/uncordon via `k8s.io/kubectl/pkg/drain`
- Monitor rollout health, trigger rollback on failure
- Emit Kubernetes Events on BootcNodePool/BootcNode for lifecycle transitions
- Use finalizers on BootcNodePool for cleanup (uncordon drained nodes on deletion)

### bootc-daemon (DaemonSet)

Lightweight daemon. Does NOT use controller-runtime Manager -- uses plain
`client-go` with periodic GET calls (no informers, no watches). This is a
separate binary from the operator.

Responsibilities:
- On startup, check if the host is a bootc system (`bootc status`). If not,
  stay idle.
- **Create its own BootcNode CRD** if it doesn't exist (name = node name,
  ownerReference → the Node object). This happens regardless of pool
  assignment -- every bootc-capable node gets a BootcNode.
- Periodically GET its own `BootcNode` CRD (~30s default, faster when in
  active phase like Staged/Rebooting)
- Report bootc status back by updating `BootcNode` status (booted/staged/
  rollback images, phase)
- When `spec.desiredImage` is set (by a pool): execute bootc commands on
  the host (`bootc switch`, `bootc upgrade`, etc.)
- When `spec.desiredImage` is empty (no pool): just report status, do nothing
- Verify staging is still valid before reporting Staged (handle the case where
  a `--download-only` staged image was garbage collected due to unexpected
  reboot)
- Execute reboots when instructed (via `systemctl reboot` or soft reboot)

The daemon pod runs with `hostPID: true` and `privileged: true`. All
bootc and systemctl commands are executed via `nsenter -t 1 -m --`
(enter PID 1's mount namespace), so bootc sees the host filesystem
(ostree repo, container storage, boot loader config). This is the
same pattern kured uses for reboots and the MCO MCD uses for bootc.
For authenticated image pulls, the daemon writes the pull secret to
`/run/ostree/auth.json` on the host (via nsenter) before running bootc.

RBAC grants `get`/`create`/`update` on all BootcNode resources (needs
`create` to bootstrap its own BootcNode). The daemon enforces
self-scoping in code -- it only reads/writes the BootcNode matching
its own node name. Daemon RBAC is manually defined (not generated from
kubebuilder markers, since markers only apply to the operator binary).

## CRDs

### BootcNodePool (cluster-scoped, user-facing)

The primary CRD. "This pool of nodes should run this bootc image."

Go type definitions follow OpenShift API conventions (see
`openshift/enhancements/dev-guide/api-conventions.md`):
- No pointers for optional fields in CRD-based APIs (unless needed to
  distinguish zero from unset)
- Godoc starts with lowercase field name (matching JSON key)
- Typed string aliases for enums with PascalCase constants
- `+required` fields: no `omitempty`; `+optional` fields: yes `omitempty`
- Document what happens when optional fields are omitted
- Validation described in both godoc and markers
- Use `omitzero` for struct fields (Go 1.24+)

```go
// BootcNodePool defines a pool of nodes that should run a specific bootc
// image. The operator resolves the image reference, stages it on matching
// nodes, and orchestrates rolling reboots to apply it. Analogous to a
// MachineConfigPool in the MCO.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Image",type=string,JSONPath=".spec.image"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.readyNodes"
// +kubebuilder:printcolumn:name="Staged",type=string,JSONPath=".status.stagedNodes",priority=1
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=".status.targetNodes"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type BootcNodePool struct {
    metav1.TypeMeta `json:",inline"`

    // metadata is the standard object's metadata.
    // +optional
    metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

    // spec defines the desired bootc image and rollout parameters.
    // +required
    Spec BootcNodePoolSpec `json:"spec"`

    // status reflects the observed state of the rollout.
    // +optional
    Status BootcNodePoolStatus `json:"status,omitempty,omitzero"`
}

// BootcNodePoolSpec defines the desired state for a bootc image rollout.
type BootcNodePoolSpec struct {
    // image is the container image reference to deploy to the targeted nodes.
    // This may be a tag (e.g. "quay.io/example/my-image:latest") or a digest
    // (e.g. "quay.io/example/my-image@sha256:abc123..."). When a tag is
    // specified, the operator periodically resolves it to a digest; a new
    // digest triggers a rollout. When a digest is specified, the operator
    // deploys exactly that image.
    // The MCO defines `ImageDigestFormat` (digest-only, with CEL regex
    // validation for `@sha256:<64hex>`) and `ImageTagFormat` (tag-only).
    // Since we accept both tags and digests, we use a plain string with
    // MinLength for MVP. A dedicated `ImageReference` type with CEL
    // validation for either format can be added later.
    // +kubebuilder:validation:MinLength=1
    // +required
    Image string `json:"image"`

    // imagePullSecret references a Secret in the operator's namespace that
    // contains credentials for pulling the image from a protected registry.
    // The Secret must be of type kubernetes.io/dockerconfigjson.
    //
    // The operator uses this secret for tag-to-digest resolution via
    // go-containerregistry. The daemon writes the secret to
    // /run/ostree/auth.json on the host before running bootc commands.
    // This is the highest-priority auth path that bootc checks (above
    // /etc/ostree/auth.json and /usr/lib/ostree/auth.json), and it is
    // ephemeral (cleared on reboot), so it does not persistently mutate
    // the host.
    //
    // When omitted, no additional credentials are provided. The node's
    // existing auth configuration (e.g. /etc/ostree/auth.json) is used
    // as-is. This is sufficient when the image is public or the node
    // already has credentials configured.
    // +optional
    ImagePullSecret ImagePullSecretReference `json:"imagePullSecret,omitempty,omitzero"`

    // nodeSelector selects the nodes that this image should be deployed to.
    // Only nodes matching all of the selector's requirements are targeted.
    // When omitted, no nodes are targeted.
    // +optional
    NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

    // rollout configures how the image is rolled out to the targeted nodes.
    // When omitted, default rollout parameters are used.
    //
    // These fields are inline rather than in a separate CRD. This follows
    // K8s precedent (Deployment.spec.strategy, DaemonSet.spec.updateStrategy,
    // MachineConfigPool.spec.maxUnavailable are all inline). A shared
    // rollout policy CRD could be added later via a `rolloutPolicyRef`
    // field if reuse across BootcNodePool instances is needed.
    // +optional
    Rollout RolloutConfig `json:"rollout,omitempty,omitzero"`

    // disruption configures the disruption policy for node reboots during
    // rollout. When omitted, the operator chooses the least disruptive
    // reboot method available.
    // +optional
    Disruption DisruptionConfig `json:"disruption,omitempty,omitzero"`

    // healthCheck configures how the operator verifies node health after
    // a reboot. When omitted, defaults are used.
    // +optional
    HealthCheck HealthCheckConfig `json:"healthCheck,omitempty,omitzero"`
}

// RolloutConfig configures how a bootc image rollout proceeds.
type RolloutConfig struct {
    // maxUnavailable is the maximum number of nodes that may be unavailable
    // (cordoned/rebooting) simultaneously during the reboot phase of a
    // rollout. Must be at least 1. When omitted, defaults to 1.
    // +kubebuilder:default=1
    // +kubebuilder:validation:Minimum=1
    // +optional
    MaxUnavailable int32 `json:"maxUnavailable,omitempty"`
}

// RebootPolicy defines how the operator handles reboots during rollout.
// +kubebuilder:validation:Enum=Auto;Full;Never
type RebootPolicy string

const (
    // RebootPolicyAuto uses soft reboot when possible (kernel and initramfs
    // unchanged), falling back to a full reboot otherwise. This minimizes
    // disruption.
    RebootPolicyAuto RebootPolicy = "Auto"

    // RebootPolicyFull always performs a full reboot, even when a soft
    // reboot would be possible.
    RebootPolicyFull RebootPolicy = "Full"

    // RebootPolicyNever stages the image but never reboots. The
    // administrator is responsible for triggering reboots externally.
    RebootPolicyNever RebootPolicy = "Never"
)

// DisruptionConfig configures the disruption policy for node reboots.
type DisruptionConfig struct {
    // rebootPolicy determines how the operator reboots nodes after staging
    // an image. Must be one of "Auto", "Full", or "Never".
    // When omitted, the operator defaults to "Auto", which uses soft reboot
    // when possible and falls back to a full reboot otherwise.
    // +kubebuilder:default=Auto
    // +optional
    RebootPolicy RebootPolicy `json:"rebootPolicy,omitempty"`
}

// ImagePullSecretReference references a Secret by name in the operator's
// namespace. Following OpenShift API conventions, we use a specific
// reference type rather than the generic corev1.LocalObjectReference.
type ImagePullSecretReference struct {
    // name is the metadata.name of the referenced Secret. The Secret must
    // be of type kubernetes.io/dockerconfigjson and must exist in the
    // operator's namespace.
    // +kubebuilder:validation:MinLength=1
    // +required
    Name string `json:"name"`
}

// HealthCheckConfig configures post-reboot health checking.
type HealthCheckConfig struct {
    // timeout is how long the operator waits for a node to become Ready
    // after a reboot before considering the update failed and triggering
    // a rollback. Must be a valid duration string (e.g. "5m", "10m").
    // When omitted, defaults to "5m".
    // +kubebuilder:default="5m"
    // +optional
    Timeout metav1.Duration `json:"timeout,omitempty"`
}

// BootcNodePoolPhase describes the overall phase of a node pool.
// The steady-state update cycle is: Ready → Staging → Rolling → Ready.
// +kubebuilder:validation:Enum=Idle;Staging;Rolling;Ready;Degraded
type BootcNodePoolPhase string

const (
    // BootcNodePoolPhaseIdle indicates the pool has not yet been
    // reconciled or has no target nodes.
    BootcNodePoolPhaseIdle BootcNodePoolPhase = "Idle"

    // BootcNodePoolPhaseStaging indicates nodes are downloading and
    // staging the desired image (pre-reboot phase).
    BootcNodePoolPhaseStaging BootcNodePoolPhase = "Staging"

    // BootcNodePoolPhaseRolling indicates the operator is performing
    // rolling reboots to apply the staged image.
    BootcNodePoolPhaseRolling BootcNodePoolPhase = "Rolling"

    // BootcNodePoolPhaseReady indicates all target nodes are running
    // the desired image and are healthy.
    BootcNodePoolPhaseReady BootcNodePoolPhase = "Ready"

    // BootcNodePoolPhaseDegraded indicates one or more nodes failed to
    // update and the rollout has been paused.
    BootcNodePoolPhaseDegraded BootcNodePoolPhase = "Degraded"
)

// BootcNodePoolStatus reflects the observed state of a bootc node pool.
type BootcNodePoolStatus struct {
    // observedGeneration is the .metadata.generation that the controller
    // last processed. Used to detect stale status (if observedGeneration
    // < metadata.generation, the controller hasn't reconciled the latest
    // spec change yet).
    // +optional
    ObservedGeneration int64 `json:"observedGeneration,omitempty"`

    // phase is the overall phase of the pool. Provides an at-a-glance
    // summary of the pool's state. Derived from the conditions and node
    // counters. Must be one of "Idle", "Staging", "Rolling", "Ready",
    // or "Degraded". The steady-state update cycle is:
    // Ready → Staging → Rolling → Ready.
    // +optional
    Phase BootcNodePoolPhase `json:"phase,omitempty"`

    // resolvedDigest is the digest that the operator resolved from the
    // image reference in the spec. When the spec image is already a digest,
    // this matches it directly. When it is a tag, this is the digest the
    // tag currently points to. Empty when resolution has not yet occurred.
    // +optional
    ResolvedDigest string `json:"resolvedDigest,omitempty"`

    // targetNodes is the number of nodes matching the nodeSelector.
    // +optional
    TargetNodes int32 `json:"targetNodes,omitempty"`

    // stagedNodes is the number of nodes that have staged the desired image
    // but have not yet rebooted into it.
    // +optional
    StagedNodes int32 `json:"stagedNodes,omitempty"`

    // readyNodes is the number of nodes that are running the desired image
    // and are in a Ready state.
    // +optional
    ReadyNodes int32 `json:"readyNodes,omitempty"`

    // updatingNodes is the number of nodes that are currently being
    // drained, rebooted, or verified.
    // +optional
    UpdatingNodes int32 `json:"updatingNodes,omitempty"`

    // conditions represent the latest observations of the BootcNodePool's
    // state. Known .status.conditions.type values are: "Available",
    // "Progressing", and "Degraded".
    //
    // The Progressing condition's message includes progress detail,
    // e.g. "5 of 10 nodes staged, 3 of 10 rebooted".
    // +listType=map
    // +listMapKey=type
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}
```

Example CR:
```yaml
apiVersion: bootc.dev/v1alpha1
kind: BootcNodePool
metadata:
  name: worker-nodes
spec:
  image: quay.io/example/my-bootc-image:latest
  nodeSelector:
    matchLabels:
      node-role.kubernetes.io/worker: ""
  rollout:
    maxUnavailable: 1
  disruption:
    rebootPolicy: Auto
  healthCheck:
    timeout: 5m
```

Notes:
- If `image` is a tag, the operator resolves it to a digest via
  `go-containerregistry`. The resolved digest drives rollouts.
- Registry credentials for digest resolution and node-level image pulls
  come from the optional `imagePullSecret` field. When specified, the
  operator reads the Secret for tag resolution and ensures the daemon
  DaemonSet mounts it. The daemon writes the credentials to
  `/run/ostree/auth.json` on the host before running bootc commands.
  This is bootc's highest-priority auth path and is ephemeral (cleared
  on reboot). When omitted, the node's existing auth config is used.
- If `image` is updated while a rollout is in progress, the current rollout is
  cancelled and a new one starts with the updated image (same behavior as
  Deployment updates).

### BootcNode (cluster-scoped, daemon-created)

One per bootc-capable node in the cluster. Created by the daemon on
startup, with ownerReference to the Node object (GC'd when Node is
deleted). Exists regardless of pool assignment -- unassigned BootcNodes
have empty spec and the daemon just reports status.

**Level-based design note:** The `desiredPhase` field may look
edge-triggered (operator mutates it from `Staged` → `Rebooting`), but
it is level-compatible: if the daemon or operator restarts at any point,
they re-read current state and converge. The daemon doesn't need event
history -- it checks `desiredPhase` vs `status.phase` and acts
accordingly. This is standard for multi-component coordination where a
purely declarative spec (just `desiredImage`) is insufficient to express
sequenced operations (stage → drain → reboot).

```go
// BootcNode tracks the bootc state of a single cluster node. Created by
// the daemon when it detects the node is a bootc host. The daemon reports
// status; the operator sets the spec when a BootcNodePool claims the node.
// When no pool claims the node, the spec is empty and the daemon just
// reports the current bootc state.
//
// The name MUST match the Kubernetes Node name. The ownerReference
// points to the Node object (not the pool), so the BootcNode is GC'd
// when the Node is deleted.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Pool",type=string,JSONPath=".metadata.labels.bootc\\.dev/pool"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Booted",type=string,JSONPath=".status.booted.image",priority=1
// +kubebuilder:printcolumn:name="Staged",type=string,JSONPath=".status.staged.image",priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type BootcNode struct {
    metav1.TypeMeta `json:",inline"`

    // metadata is the standard object's metadata.
    // The name must match the Kubernetes Node name.
    // The label bootc.dev/pool indicates which BootcNodePool (if any)
    // has claimed this node. Empty or absent when unassigned.
    // +optional
    metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

    // spec defines the desired bootc state for this node. Set by the
    // operator when a BootcNodePool claims this node. Empty when no
    // pool has claimed the node (daemon just reports status).
    // +optional
    Spec BootcNodeSpec `json:"spec,omitempty,omitzero"`

    // status reflects the observed bootc state, reported by the daemon.
    // +optional
    Status BootcNodeStatus `json:"status,omitempty,omitzero"`
}

// BootcNodeDesiredPhase describes the phase that the daemon should work towards.
// +kubebuilder:validation:Enum=Staged;Rebooting;RollingBack
type BootcNodeDesiredPhase string

const (
    // BootcNodeDesiredPhaseStaged instructs the daemon to download and stage
    // the desired image without rebooting.
    BootcNodeDesiredPhaseStaged BootcNodeDesiredPhase = "Staged"

    // BootcNodeDesiredPhaseRebooting instructs the daemon to apply the staged
    // image and reboot (soft reboot if possible, full otherwise).
    BootcNodeDesiredPhaseRebooting BootcNodeDesiredPhase = "Rebooting"

    // BootcNodeDesiredPhaseRollingBack instructs the daemon to rollback to
    // the previous image and reboot.
    BootcNodeDesiredPhaseRollingBack BootcNodeDesiredPhase = "RollingBack"
)

// BootcNodeSpec defines the desired bootc state for a single node.
// All fields are optional -- when empty, the daemon just reports status
// without taking any action.
type BootcNodeSpec struct {
    // desiredImage is the container image digest that this node should be
    // running. Always a fully qualified digest reference
    // (e.g. "quay.io/example/my-image@sha256:abc123..."). Set by the
    // operator when a pool claims this node. Empty when no pool has
    // claimed the node.
    // +optional
    DesiredImage string `json:"desiredImage,omitempty"`

    // desiredPhase is the phase that the daemon should work towards.
    // Must be one of "Staged", "Rebooting", or "RollingBack".
    // Set by the operator to drive the daemon through the rollout
    // lifecycle. Empty when no pool has claimed the node.
    // +optional
    DesiredPhase BootcNodeDesiredPhase `json:"desiredPhase,omitempty"`

    // rebootPolicy determines how the daemon reboots this node.
    // Propagated from the owning BootcNodePool's disruption config.
    // Must be one of "Auto", "Full", or "Never". When empty, defaults
    // to "Auto".
    // +optional
    RebootPolicy RebootPolicy `json:"rebootPolicy,omitempty"`
}

// BootcNodePhase describes the current phase of the daemon on a node.
// +kubebuilder:validation:Enum=Ready;Staging;Staged;Rebooting;RollingBack;Error
type BootcNodePhase string

const (
    // BootcNodePhaseReady indicates the node is running the desired image.
    BootcNodePhaseReady BootcNodePhase = "Ready"

    // BootcNodePhaseStaging indicates the daemon is downloading/staging
    // the desired image.
    BootcNodePhaseStaging BootcNodePhase = "Staging"

    // BootcNodePhaseStaged indicates the desired image is staged and
    // ready to be applied on reboot.
    BootcNodePhaseStaged BootcNodePhase = "Staged"

    // BootcNodePhaseRebooting indicates the daemon is applying the staged
    // image and rebooting.
    BootcNodePhaseRebooting BootcNodePhase = "Rebooting"

    // BootcNodePhaseRollingBack indicates the daemon is rolling back to
    // the previous image.
    BootcNodePhaseRollingBack BootcNodePhase = "RollingBack"

    // BootcNodePhaseError indicates an error occurred during the update.
    BootcNodePhaseError BootcNodePhase = "Error"
)

// BootcNodeStatus reflects the observed bootc state on a node, as
// reported by the daemon from `bootc status --json`.
type BootcNodeStatus struct {
    // booted is the bootc deployment that the node is currently running.
    // +optional
    Booted BootEntryStatus `json:"booted,omitempty,omitzero"`

    // staged is the bootc deployment that is staged for the next boot,
    // if any.
    // +optional
    Staged BootEntryStatus `json:"staged,omitempty,omitzero"`

    // rollback is the bootc deployment that the node can roll back to,
    // if any.
    // +optional
    Rollback BootEntryStatus `json:"rollback,omitempty,omitzero"`

    // phase is the daemon's current phase on this node.
    // +optional
    Phase BootcNodePhase `json:"phase,omitempty"`

    // message is a human-readable description of the current state or
    // error. Empty when no additional detail is available.
    // +optional
    Message string `json:"message,omitempty"`

    // conditions represent the latest observations of this node's bootc
    // state. Known .status.conditions.type values are: "ImageStaged"
    // and "Healthy".
    // +listType=map
    // +listMapKey=type
    // +optional
    Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// BootEntryStatus describes a single bootc deployment slot (booted,
// staged, or rollback), as reported by `bootc status --json`.
type BootEntryStatus struct {
    // image is the fully qualified container image reference for this
    // deployment (e.g. "quay.io/example/my-image@sha256:abc123...").
    // +optional
    Image string `json:"image,omitempty"`

    // version is the image version label, if available.
    // +optional
    Version string `json:"version,omitempty"`

    // timestamp is when this deployment's image was built.
    // +optional
    Timestamp metav1.Time `json:"timestamp,omitempty"`

    // softRebootCapable indicates whether this deployment can be reached
    // via a soft reboot (userspace-only restart) rather than a full
    // hardware reboot. This is true when the kernel and initramfs are
    // unchanged relative to the currently booted deployment.
    // +optional
    SoftRebootCapable bool `json:"softRebootCapable,omitempty"`
}
```

## Reconciler Patterns

### BootcNodePool Reconciler

Follows the kubebuilder deploy-image reconciler pattern:

```go
func (r *BootcNodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // 1. Fetch the BootcNodePool CR
    // 2. If not found, return (deleted)
    // 3. Initialize status conditions if empty (set to Unknown)
    // 4. Add finalizer if not present
    // 5. If being deleted (DeletionTimestamp set):
    //    - Uncordon any drained nodes
    //    - Remove finalizer
    //    - Return
    // 6. Resolve image tag → digest (if tag)
    // 7. List BootcNodes (daemon-created) for nodes matching nodeSelector
    // 8. Check for overlapping BootcNodePool CRDs (reject via Degraded condition)
    // 9. Claim matching BootcNodes: set bootc.dev/pool label and
    //    spec.desiredImage/desiredPhase on each matching BootcNode.
    //    (BootcNodes are created by the daemon, not the pool.)
    // 10. Release BootcNodes that this pool previously claimed but whose
    //     node no longer matches the nodeSelector: clear spec fields and
    //     remove bootc.dev/pool label. The BootcNode stays (it's bound
    //     to the Node, not the pool).
    // 11. Orchestrate rollout:
    //     - Count nodes by phase (ready, staging, staged, rebooting, error)
    //     - Update BootcNodePool status counters
    //     - Select next batch for drain+reboot (respecting maxUnavailable)
    //     - Cordon, drain, set desiredPhase=Rebooting
    //     - Handle rollback for failed nodes
    // 12. Re-fetch CR before status update (avoid "object modified" errors)
    // 13. Update status with conditions
    // 14. Return with RequeueAfter for periodic re-resolution
}

func (r *BootcNodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.BootcNodePool{}).
        // Watch BootcNodes (daemon-created, not pool-owned) to react to
        // status changes reported by the daemon.
        Watches(&v1alpha1.BootcNode{}, handler.EnqueueRequestsFromMapFunc(
            r.findBootcNodePoolsForBootcNode,
        )).
        Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(
            r.findBootcNodePoolsForNode,
        )).
        Named("bootcnodepool").
        Complete(r)
}
```

RBAC markers:
```go
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodepools/finalizers,verbs=update
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=bootc.dev,resources=bootcnodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;delete
// +kubebuilder:rbac:groups="",resources=pods/eviction,verbs=create
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
```

## Rollout Flow (StageThenReboot)

### bootc command semantics

Two cases depending on whether the image reference changes:

- **Same image ref, new digest** (e.g. tag `:latest` resolved to a new digest):
  - Stage: `bootc upgrade --download-only`
  - Apply: `bootc upgrade --from-downloaded --soft-reboot=auto --apply`
- **Different image ref** (e.g. switching from `imageA` to `imageB`):
  - Stage: `bootc switch <image>` (downloads and stages without rebooting by
    default -- does NOT support `--download-only`)
  - Apply: `bootc upgrade --from-downloaded --soft-reboot=auto --apply`

Note: `--download-only` stages are ephemeral. If the node reboots for any
reason before the staged image is applied, the image is eligible for GC.
The daemon must verify staging is still valid before reporting Staged, and
re-stage if needed.

### Flow

```
Phase 1: Staging (parallel across all nodes)
─────────────────────────────────────────────
1. User creates/updates BootcNodePool CR
2. Operator resolves tag → digest (using go-containerregistry)
3. Operator creates BootcNode CRDs for matching nodes
   (also watches Node objects to handle label changes / new nodes)
4. Operator sets desiredImage + desiredPhase=Staged on ALL BootcNode CRDs
5. All agents poll, detect mismatch:
   - If image ref changed: run `bootc switch <image>`
   - If same ref, new digest: run `bootc upgrade --download-only`
6. Each daemon verifies staging via `bootc status --json`, updates
   BootcNode.status.phase = Staged
   (Agent re-verifies on each poll cycle; re-stages if GC'd)

Phase 2: Rolling reboot (batched by maxUnavailable)
────────────────────────────────────────────────────
7. Operator selects batch of Staged nodes (up to maxUnavailable)
8. Operator re-verifies each node is still Staged before proceeding
9. For each node in batch:
   a. Operator cordons the node
   b. Operator drains the node (evicts pods, respects PDBs)
   c. Operator sets BootcNode.spec.desiredPhase = Rebooting
   d. Agent polls (fast-poll in active phase), sees Rebooting, runs:
      `bootc upgrade --from-downloaded --soft-reboot=auto --apply`
   e. Node reboots (soft or full depending on image diff)
   f. Agent pod restarts, runs `bootc status --json`
   g. Agent updates BootcNode.status with new booted image
   h. Agent sets phase = Ready
   i. Operator verifies node is Ready within healthCheck.timeout
   j. Operator uncordons the node
10. Repeat for next batch until all nodes updated

Phase 3: Rollback (on failure)
──────────────────────────────
If a node doesn't become Ready within timeout:
   a. If node is reachable (daemon running): operator sets
      desiredPhase=RollingBack, daemon runs `bootc rollback --apply`
   b. If node is unreachable: bootloader falls back to previous
      deployment automatically (A/B boot). Operator detects node
      came back on old image and marks it as failed.
   c. Operator stops rollout, sets Degraded condition on BootcNodePool

Note: Agent credentials survive rollback because they come from the
pod's service account token (projected by kubelet), not from /etc.
```

## bootc Features Surfaced

### MVP

| Feature | bootc command | How the operator uses it |
|---------|--------------|------------------------|
| Image switch | `bootc switch <image>` | Change the tracked image on a node |
| Pre-staging | `bootc upgrade --download-only` | Stage on all nodes before any reboots |
| Staged apply | `bootc upgrade --from-downloaded --apply` | Apply pre-staged image + reboot |
| Soft reboot | `--soft-reboot=auto` | Minimize disruption when kernel unchanged |
| Rollback | `bootc rollback --apply` | Auto-rollback on health check failure |
| Status | `bootc status --json` | Rich per-node state in BootcNode CRD |
| Update check | `bootc upgrade --check` | Detect when new digest available for a tag |

### Post-MVP

| Feature | Description |
|---------|-------------|
| Sysexts via OCI | Extend hosts with sysexts without rebuilding images |
| Logically bound images | Pre-pull app containers alongside the OS |
| Runtime configuration | Files/units applied at runtime (depends on bootc API maturity) |
| /usr overlays | Managed overlays for debugging/hotfixes |
| Config diff monitoring | Alert on /etc drift (`bootc config-diff`) |
| kured awareness | Respect kured's lock annotation if kured is present |
| NDP refinement | Granular disruption policies (no-reboot, service restart, etc.) |
| Multi-cluster | HyperShift/ACM/Hive integration patterns |
| Observability | Prometheus metrics (rollout progress, durations, error rates) |
| HA operator | 2+ replica support with leader election |

## Go Package Structure

kubebuilder scaffolds a single-binary project. Since we have two binaries
(operator + daemon), we extend the standard layout:

```
bootc-operator/
├── api/                          # Go CRD type definitions (kubebuilder-managed)
│   └── v1alpha1/
│       ├── bootcnodepool_types.go   # BootcNodePool CRD types
│       ├── bootcnode_types.go    # BootcNode CRD types
│       ├── groupversion_info.go  # SchemeBuilder, AddToScheme
│       └── zz_generated.deepcopy.go  # AUTO-GENERATED
├── cmd/
│   ├── operator/                 # bootc-operator binary (replaces cmd/main.go)
│   │   └── main.go              # controller-runtime Manager setup
│   └── daemon/                   # bootc-daemon binary (manual, not kubebuilder)
│       └── main.go              # plain client-go poll loop
├── internal/
│   ├── controller/               # Operator reconcilers (kubebuilder-managed)
│   │   ├── bootcnodepool_controller.go      # BootcNodePool reconciler
│   │   ├── bootcnodepool_controller_test.go # Unit tests (Ginkgo + envtest)
│   │   ├── suite_test.go                 # envtest bootstrap
│   │   └── rollout.go                    # Rollout orchestration logic
│   └── daemon/                   # Daemon logic (manual, not kubebuilder)
│       ├── daemon.go             # Poll loop + state machine
│       ├── bootc.go              # bootc CLI wrapper (host nsenter)
│       └── reboot.go             # Reboot execution
├── pkg/                          # Importable packages (for MCO)
│   ├── bootc/                    # bootc CLI interface
│   │   ├── client.go             # Execute bootc commands
│   │   ├── types.go              # Parsed bootc status types
│   │   └── status.go             # Status parsing
│   └── drain/                    # Drain coordination (wraps kubectl drain)
│       └── drain.go
├── config/                       # Kustomize manifests (kubebuilder-managed)
│   ├── crd/bases/                # AUTO-GENERATED CRD YAMLs
│   ├── manager/manager.yaml      # Operator Deployment
│   ├── daemon/                   # Daemon DaemonSet (manual)
│   │   ├── daemonset.yaml
│   │   ├── service_account.yaml
│   │   └── kustomization.yaml
│   ├── rbac/                     # AUTO-GENERATED operator RBAC + manual daemon RBAC
│   │   ├── role.yaml             # AUTO-GENERATED from +kubebuilder:rbac markers
│   │   ├── daemon_role.yaml      # MANUAL: daemon ClusterRole
│   │   └── daemon_role_binding.yaml
│   ├── default/kustomization.yaml
│   ├── prometheus/               # ServiceMonitor
│   └── samples/                  # Example CRs
├── tests/                        # E2E tests (existing harness, NOT kubebuilder)
│   ├── run.sh                    # Test runner: discovers test-*.sh, runs each in FCOS VM
│   ├── test-smoke.sh             # Existing: verify cluster + reboot
│   ├── test-rollout.sh           # NEW: deploy BootcNodePool, verify rollout
│   ├── k8s/                      # Container image: QEMU + FCOS + kubeadm
│   │   ├── Containerfile
│   │   ├── config.bu             # Butane config for single-node K8s
│   │   └── entrypoint.sh
│   └── results/                  # Test artifacts (gitignored)
├── hack/                         # Dev scripts, boilerplate
├── go.mod
├── go.sum
├── Makefile                      # Extended with daemon targets
├── Dockerfile                    # Multi-stage: builds both binaries
└── PROJECT                       # kubebuilder metadata (DO NOT EDIT)
```

### Makefile extensions (beyond kubebuilder defaults)

```makefile
# Standard kubebuilder targets: manifests, generate, build, test, run, deploy,
# install, uninstall, docker-build, docker-push, fmt, vet, lint

# Additional targets for the daemon:
.PHONY: build-daemon
build-daemon: fmt vet
	go build -o bin/daemon cmd/daemon/main.go

.PHONY: docker-build-daemon
docker-build-daemon:
	$(CONTAINER_TOOL) build -t $(DAEMON_IMG) -f Dockerfile.daemon .
```

### Dockerfile

Multi-target Dockerfile that builds both binaries:
```dockerfile
FROM golang:1.25 AS builder
# ... build both cmd/operator and cmd/daemon

FROM gcr.io/distroless/static:nonroot AS operator
COPY --from=builder /workspace/bin/operator /manager

FROM gcr.io/distroless/static:nonroot AS daemon
COPY --from=builder /workspace/bin/daemon /daemon
COPY --from=builder /usr/bin/nsenter /usr/bin/nsenter
```

Note: Both images use distroless. The daemon only needs `nsenter` in the
container -- all host commands (bootc, systemctl) are executed via
`nsenter -t 1 -m --` which uses the host's binaries. The daemon runs
with elevated privileges (`hostPID: true`, `privileged: true`).

## Edge Cases

- **Overlapping BootcNodePool CRDs**: If two BootcNodePool CRDs match the same node,
  the operator should reject the second one via a status condition on the
  BootcNodePool (not a webhook for MVP simplicity). A node can only be managed by
  one BootcNodePool.
- **BootcNodePool deletion during rollout**: Finalizer runs cleanup: uncordon
  any drained nodes, release all claimed BootcNodes (clear spec + pool label),
  then remove finalizer. BootcNodes are NOT deleted (they're bound to Node
  objects, not pools).
- **BootcNodePool spec update during rollout**: Current rollout is cancelled. All
  nodes are re-staged with the new image. Already-rebooted nodes stay on the
  old image until re-staged and rebooted.
- **Daemon unavailable**: If the daemon pod isn't running on a node (node
  unreachable), the operator skips it and marks it as unavailable.
- **Non-bootc nodes**: The daemon detects bootc is not available and stays
  idle (no BootcNode is created). If a pool's nodeSelector matches a
  non-bootc node, the pool ignores it (no BootcNode exists to claim).
- **Node label changes**: If a node's labels change so it no longer matches a
  pool's nodeSelector, the pool releases the BootcNode (clears spec + removes
  pool label). The BootcNode stays (bound to the Node). If another pool's
  selector now matches, it claims the BootcNode (sets spec + pool label).
- **New bootc node joins**: The daemon creates a BootcNode on startup. If any
  pool's nodeSelector matches, the pool claims it on the next reconcile.
- **Staged image garbage collected**: If a node reboots unexpectedly between
  staging and apply, the `--download-only` staged image may be GC'd. The daemon
  re-verifies and re-stages on each poll cycle.
- **Rollback failure**: If `bootc rollback --apply` fails (no previous
  deployment), the node is marked as Error and the operator stops the rollout.
- **kured conflict (MVP)**: Document that kured should be disabled on
  bootc-managed nodes. kured awareness (checking kured's lock annotation)
  deferred to post-MVP.
- **Object modified errors**: Re-fetch CRs before status updates to avoid
  conflict errors (standard controller-runtime pattern).

## MVP Implementation

### 1. Scaffold

Set up the kubebuilder project structure and extend it for the dual-binary
(operator + daemon) layout.

- [x] `kubebuilder init --domain bootc.dev --repo github.com/jlebon/bootc-operator`
- [x] `kubebuilder create api --group "" --version v1alpha1 --kind BootcNodePool`
      (yes to both Resource and Controller)
- [x] `kubebuilder create api --group "" --version v1alpha1 --kind BootcNode`
      (yes to Resource, no to Controller -- the operator watches it but the
      daemon creates it)
- [x] Move `cmd/main.go` → `cmd/operator/main.go`; update `Makefile` to
      build from `cmd/operator/`
- [x] Create `cmd/daemon/main.go` stub (flag parsing, client-go setup,
      placeholder poll loop)
- [x] Add `Makefile` targets: `build-daemon`, `docker-build-daemon`
- [x] Create multi-target `Dockerfile` (operator + daemon stages, both
      distroless; daemon includes nsenter)
- [x] Create `config/daemon/` directory with:
      - `daemonset.yaml` (`hostPID: true`, `privileged: true`,
        `nodeAffinity` with `DoesNotExist` on `node.bootc.dev/skip`,
        nsenter binary included)
      - `service_account.yaml`
      - `kustomization.yaml`
- [x] Create daemon RBAC in `config/rbac/`:
      - `daemon_role.yaml` (ClusterRole: `get`/`create`/`update` on
        `bootcnodes`, `get` on `nodes`)
      - `daemon_role_binding.yaml`
- [x] Wire `config/default/kustomization.yaml` to include daemon resources
- [x] Verify `make manifests generate build` passes

### 2. CRD types

Define all API types with proper kubebuilder markers and OpenShift API
conventions.

- [x] `api/v1alpha1/bootcnodepool_types.go`:
      - `BootcNodePool`, `BootcNodePoolSpec`, `BootcNodePoolStatus`
      - `RolloutConfig`, `DisruptionConfig`, `HealthCheckConfig`
      - `ImagePullSecretReference`
      - Enum types: `RebootPolicy`, `BootcNodePoolPhase`
      - Enum constants with godoc
      - kubebuilder markers: validation, printcolumns, subresource, scope
      - Godoc following OpenShift conventions (lowercase field names,
        document omitted behavior, validation in prose + markers)
- [x] `api/v1alpha1/bootcnode_types.go`:
      - `BootcNode`, `BootcNodeSpec`, `BootcNodeStatus`, `BootEntryStatus`
      - Enum types: `BootcNodeDesiredPhase`, `BootcNodePhase`
      - Spec fields are `+optional` (empty when no pool assigned)
      - ownerReference → Node (documented in godoc, enforced in daemon code)
      - `bootc.dev/pool` label for pool association (documented in godoc)
      - printcolumn for Pool label
- [x] `api/v1alpha1/groupversion_info.go`: verify SchemeBuilder is correct
- [x] Run `make manifests generate` → verify CRD YAMLs in `config/crd/bases/`
- [x] Create `config/samples/bootcnodepool_v1alpha1.yaml` (example CR)

### 3. bootc client library

`pkg/bootc/` -- Go wrapper for the bootc CLI. Used by the daemon to
execute bootc commands on the host via nsenter.

- [x] `pkg/bootc/types.go`: Go types matching the `org.containers.bootc/v1`
      BootcHost JSON schema (booted/staged/rollback deployments, image refs,
      versions, timestamps, softRebootCapable)
- [x] `pkg/bootc/client.go`: `Client` interface + implementation with methods:
      - `NewClient()` constructor (uses nsenterRunner)
      - `NewClientWithRunner(runner)` for testing
      - `Status(ctx) (*Host, error)` -- runs `bootc status --json`, parses
      - `Switch(ctx, image) error` -- runs `bootc switch <image>`
      - `UpgradeDownloadOnly(ctx) error` -- runs `bootc upgrade --download-only`
      - `UpgradeApply(ctx, softReboot) error` -- runs `bootc upgrade
        --from-downloaded [--soft-reboot=auto] --apply`
      - `Rollback(ctx, apply) error` -- runs `bootc rollback [--apply]`
      - `IsBootcHost(ctx) bool` -- returns true if bootc is available
      - `CommandRunner` interface for testability (nsenterRunner default)
- [x] `pkg/bootc/auth.go`: `WriteAuthFile(ctx, runner, data)` -- writes
      dockerconfigjson to `/run/ostree/auth.json` on the host via nsenter;
      `RemoveAuthFile(ctx, runner)` for cleanup
- [x] `pkg/bootc/status.go`: JSON parsing logic, mapping BootcHost fields
      to our `BootEntryStatus` API type. Helper functions:
      `ToBootEntryStatus`, `ToBootcNodeStatus`, `HasStagedImage`,
      `StagedImageRef`, `BootedImageRef`, `IsDownloadOnly`
- [x] Unit tests for JSON parsing (mock `bootc status --json` output),
      client commands, and status mapping (24 tests, 78.8% coverage)

### 4. Daemon

`cmd/daemon/main.go` + `internal/daemon/` -- the per-node agent.

- [x] `internal/daemon/daemon.go`: `Daemon` struct and main loop:
      - On startup: call `bootcClient.IsBootcHost()`. If false, log and
        stay idle (sleep forever).
      - On startup (bootc host): create BootcNode CRD if it doesn't
        exist. Set `metadata.name` = node name, `ownerReferences` →
        Node object. Set initial status from `bootc status --json`.
      - Poll loop: periodically GET own BootcNode CRD.
        - Fast poll (~5s) when in active phase (Staging/Rebooting).
        - Slow poll (~30s) when idle or Ready.
      - On each poll: run `bootc status --json`, update BootcNode status
        (booted/staged/rollback/phase).
      - State machine:
        - `spec.desiredImage` empty → do nothing, just report status
        - `spec.desiredImage` set + `desiredPhase=Staged`:
          - If already staged (verify via status) → set phase=Staged
          - If not staged → determine switch vs upgrade, execute, set
            phase=Staging then Staged
        - `spec.desiredPhase=Rebooting`:
          - Verify image is staged before rebooting
          - Run `bootc upgrade --from-downloaded --apply
            [--soft-reboot=auto]`
          - (node reboots; daemon restarts; next poll reports new status)
        - `spec.desiredPhase=RollingBack`:
          - Run `bootc rollback --apply`
          - (node reboots)
      - `needsSwitch()` helper determines `bootc switch` vs `bootc
        upgrade --download-only` by comparing image repositories
      - `shouldSoftReboot()` helper checks reboot policy + staged
        deployment capability
- [x] `internal/daemon/kubeclient.go`: KubeClient interface implementation
      using client-go REST client for BootcNode operations and typed
      clientset for Node operations. Separate from controller-runtime
      (no informers, no watches).
- [x] `internal/daemon/bootc.go`: NOT NEEDED as separate file. The
      daemon directly uses `pkg/bootc.Client` interface and the
      `pkg/bootc.ToBootcNodeStatus`/`StagedImageRef`/`BootedImageRef`
      helpers. The wiring is inline in `daemon.go`.
- [x] `internal/daemon/reboot.go`: NOT NEEDED as separate file. Reboots
      are handled by `bootc upgrade --from-downloaded --apply` and
      `bootc rollback --apply` which both trigger reboots directly.
      No separate `nsenter systemctl reboot` is needed.
- [x] `cmd/daemon/main.go`: entrypoint. Parse flags (`--node-name` from
      downward API env var, `--poll-interval`, `--kubeconfig`). Create
      client-go rest.Config (in-cluster). Instantiate Daemon, run loop.
- [x] Unit tests: mock bootc client and KubeClient interfaces, test
      state machine transitions (20 tests covering all phases, error
      handling, soft reboot policy, image repo switching, BootcNode
      creation). Coverage: 57.2%.

### 5. BootcNodePool reconciler

`internal/controller/bootcnodepool_controller.go` -- the main reconciler.

- [x] Reconcile function (kubebuilder deploy-image pattern):
      1. Fetch BootcNodePool CR
      2. If not found → return
      3. Initialize status conditions if empty (Available, Progressing,
         Degraded → Unknown)
      4. Add finalizer (`bootc.dev/cleanup`) if not present
      5. Handle deletion (DeletionTimestamp set):
         - Release all claimed BootcNodes (clear spec + pool label)
         - Remove finalizer
         - (Uncordoning drained nodes deferred to rollout orchestration)
      6. Resolve image reference (MVP: pass-through; full digest
         resolution via go-containerregistry deferred)
      7. List BootcNodes; match against pool's nodeSelector by looking up
         each BootcNode's corresponding Node
      8. Overlapping pool detection: if a matching BootcNode is already
         claimed by another pool → set Degraded condition
      9. Claim matching BootcNodes (set spec.desiredImage,
         spec.desiredPhase=Staged, add `bootc.dev/pool` label)
      10. Release non-matching BootcNodes (clear spec + pool label)
      11. Orchestrate rollout (deferred to item 6 -- rollout.go)
      12. Re-fetch CR before status update
      13. Update status (phase, counters, conditions)
      14. Return with RequeueAfter for periodic re-resolution (5m)
- [x] `SetupWithManager`: `For(BootcNodePool)`,
      `Watches(BootcNode, findPoolsForBootcNode)`,
      `Watches(Node, findPoolsForNode)`
- [x] RBAC markers (bootcnodepools, bootcnodes, nodes, events,
      pods, pods/eviction). Drain RBAC added with item 7.
- [x] Integration tests (envtest): 15 tests covering condition init,
      finalizer, deletion cleanup, node claiming/releasing, overlap
      detection, status counters/phases, nodeSelector changes, image
      updates, idempotent reconciliation, observedGeneration. Controller
      coverage: 73.7%.
- [x] Deploy daemon DaemonSet: reconcile the DaemonSet as an owned resource
      (create/update from `DAEMON_IMAGE` env var).
      `internal/controller/daemonset.go`: `DaemonSetReconciler` watches
      the daemon DaemonSet and ensures it matches the desired state.
      Creates/updates DaemonSet, ServiceAccount, ClusterRole, and
      ClusterRoleBinding. Image comes from `DAEMON_IMAGE` env var on
      the operator Deployment. `EnsureDaemonResources()` bootstraps
      resources at startup via a manager Runnable. RBAC markers generate
      permissions for `apps/daemonsets`, `serviceaccounts`,
      `rbac.authorization.k8s.io/clusterroles`, and
      `rbac.authorization.k8s.io/clusterrolebindings`. Operator's
      `config/manager/manager.yaml` updated with `DAEMON_IMAGE` and
      `POD_NAMESPACE` env vars. 10 integration tests covering resource
      creation, idempotency, image updates, spec correctness (node
      affinity, tolerations, hostPID, privileged, NODE_NAME env),
      and ClusterRole drift repair. Controller coverage: 78.1%.
- [ ] Digest resolution: `internal/controller/digest.go` using
      `go-containerregistry` (`remote.Image`, `remote.Head`), with optional
      auth from `imagePullSecret`

### 6. Rollout orchestration

`internal/controller/rollout.go` -- stage-then-reboot strategy.

- [x] `orchestrateRollout(pool, claimed, nodeMap, desiredImage)`:
      - `classifyNodes()` groups nodes by phase (readyAtDesired, staged,
        staging, rebooting, rollingBack, errored, needsStaging)
      - Pool status counters updated from classification in controller's
        existing `updateStatus()` and `computePhase()`
      - `computePhase()` updated: Staged nodes without rebooting →
        Rolling (not Staging); tracks `hasStaged` separately
      - `uncordonReadyNodes()`: uncordons nodes that rebooted into
        desired image, resets desiredPhase to Staged
      - Error nodes → stop rollout (Degraded), don't advance more
      - RollingBack nodes → wait (requeue)
      - `advanceStagedNodes()`: selects next batch of Staged nodes
        (up to maxUnavailable minus currently rebooting), sorts by
        name for deterministic ordering, cordons each node, sets
        desiredPhase=Rebooting
      - Staging re-verification: confirms node is still Staged before
        advancing (guards against races)
      - Active rollout uses 15s requeue interval (vs 5m default)
      - `desiredPhaseForNode()` preserves Rebooting/RollingBack if
        already set by orchestrator (prevents claim sync from resetting)
- [x] Integration tests (envtest): 10 rollout-specific tests covering
      advance to Rebooting, maxUnavailable=1 and =2, uncordon after
      success, no advance during staging, wait for rebooting nodes,
      preserve Rebooting phase, error stops rollout, full multi-batch
      rolling update, pool phase Rolling when staged, image update
      mid-rollout. Controller coverage: 76.8%.
- [x] Health check: implemented in item 9. `checkRebootTimeouts()`
      tracks time via `bootc.dev/rebooting-since` annotation. Nodes
      that exceed `healthCheck.timeout` are set to
      desiredPhase=RollingBack.
- [x] Drain: integrated via `pkg/drain.Drainer` (item 7). Nodes are
      now cordoned AND drained (pods evicted, PDBs respected) before
      `desiredPhase=Rebooting` is set.

### 7. Drain manager

`pkg/drain/` -- wraps `k8s.io/kubectl/pkg/drain`.

- [x] `pkg/drain/drain.go`:
      - `Drainer` interface with `Cordon(ctx, nodeName)`,
        `Drain(ctx, nodeName)`, `Uncordon(ctx, nodeName)`
      - `NewDrainer(clientset, Options)` constructor wrapping
        `k8s.io/kubectl/pkg/drain.Helper`
      - `Options`: configurable `Timeout`, `GracePeriodSeconds`,
        `DeleteEmptyDirData`, `Force`, `Out`/`ErrOut` writers
      - `IgnoreAllDaemonSets: true` by default (bootc-daemon DaemonSet
        must not be evicted)
      - Respects PDBs (eviction-based, not delete)
- [x] Unit tests with fake clientset: 10 tests covering cordon,
      uncordon, drain (empty node, pod eviction, DaemonSet ignored,
      PDB rejection), defaults, custom options. 100% coverage.
- [x] Integrated into rollout orchestration (`internal/controller/rollout.go`):
      `advanceStagedNodes()` now calls `Drain()` after cordon and before
      setting `desiredPhase=Rebooting`. Drainer injected via reconciler field.
- [x] RBAC markers added: `pods` (get/list/delete), `pods/eviction` (create)
- [x] Wired up in `cmd/operator/main.go`: creates kubernetes clientset
      from manager config, instantiates `drain.NewDrainer` with
      `DeleteEmptyDirData: true`, passes to reconciler
- [x] `noopDrainer` fallback for tests that don't configure a Drainer

### 8. Soft reboot

- [x] Daemon: when `desiredPhase=Rebooting`, read `spec.rebootPolicy`:
      `shouldSoftReboot()` in `daemon.go` checks policy and staged
      deployment's `SoftRebootCapable` flag. `Auto`/empty → soft reboot
      if capable; `Full`/`Never` → no soft reboot. `UpgradeApply()`
      appends `--soft-reboot=auto` when enabled.
- [x] BootcNode status: daemon populates `softRebootCapable` from
      `bootc status --json` staged deployment info via
      `pkg/bootc/status.go:ToBootEntryStatus`.
- [x] Pool reconciler: `claimBootcNode()` copies
      `disruption.rebootPolicy` from BootcNodePool to
      `spec.rebootPolicy` on each claimed BootcNode, defaulting to
      `Auto` when empty.
- [x] Unit tests: `TestReconcileRebootingSoftRebootAuto`,
      `TestReconcileRebootingFullPolicy`, `TestShouldSoftReboot` (9
      sub-tests) covering Auto/Full/Never/empty policies.

### 9. Auto rollback

- [x] Operator: when advancing a BootcNode to desiredPhase=Rebooting
      (in `advanceStagedNodes`), sets a `bootc.dev/rebooting-since`
      annotation with the current RFC3339 timestamp.
- [x] On each reconcile: `checkRebootTimeouts()` checks rebooting
      nodes against `healthCheck.timeout` (default 5m). If elapsed
      time exceeds the timeout, sets desiredPhase=RollingBack.
- [x] Daemon: handles RollingBack phase → runs `bootc rollback --apply`
      (already implemented in item 4).
- [x] Operator: `handleCompletedRollbacks()` detects nodes that
      completed rollback (desiredPhase=RollingBack, status.Phase=Ready,
      booted image != desired). Uncordons the node, clears the
      rebooting-since annotation, resets desiredPhase=Staged, and
      sets status.Phase=Error with a descriptive message. This
      triggers existing Degraded logic to stop the rollout.
- [x] `classifyNodes()` extended with `rolledBack` category to
      distinguish completed rollbacks from regular needsStaging nodes.
- [x] `uncordonReadyNodes()` clears the rebooting-since annotation
      when a node successfully reboots into the desired image.
- [x] `BootcNodePoolReconciler.Now` field (func() time.Time) for
      testability, defaults to `time.Now`.
- [x] Integration tests: 7 tests covering annotation setting/clearing,
      timeout trigger with custom and default timeouts, completed
      rollback handling (Error status, uncordon, Degraded pool),
      no-false-positive within timeout, and waiting during active
      rollback. Controller coverage: 78.0%.

### 10. Events

- [x] Set up `record.EventRecorder` in the reconciler. Added `Recorder`
      field to `BootcNodePoolReconciler` with `recordEvent`/`recordEventf`
      nil-safe helpers. Wired up via `mgr.GetEventRecorderFor()` in
      `cmd/operator/main.go`. RBAC marker for events already present.
      Uses the old `record.EventRecorder` API (new `events.EventRecorder`
      migration tracked as TODO).
- [x] Emit events on BootcNodePool:
      - `RolloutStarted` (pool transitions from Idle/Ready to Staging)
      - `StagingComplete` (pool transitions from Staging to Rolling)
      - `RolloutComplete` (all nodes at desired image)
      - `RolloutDegraded` (node failed, rollout paused)
      - `OverlappingPools` (node claimed by another pool)
      - `NodeClaimed` (node joins pool)
- [x] Emit events on BootcNode:
      - `NodeDrained` (node drained before reboot)
      - `RebootInitiated` (desiredPhase set to Rebooting)
      - `UpdateComplete` (node rebooted into desired image)
      - `RollbackTriggered` (health check timeout exceeded)
      - `RollbackComplete` (rollback finished, node on old image)
      - `NodeReleased` (node released from pool)
- [x] Phase transition events use `emitPhaseTransitionEvents()` to avoid
      duplicate emissions: events are only emitted when the pool phase
      changes (previousPhase != newPhase).
- [x] Integration tests: 10 tests using `record.FakeRecorder` covering
      RolloutStarted, RebootInitiated, NodeDrained, UpdateComplete,
      RolloutComplete, RolloutDegraded, RollbackTriggered,
      OverlappingPools, NodeReleased, RollbackComplete, and
      no-duplicate-emission on re-reconcile. Controller coverage: 79.4%.

### 11. Testing

- [x] **Unit tests** (Ginkgo v2 + Gomega):
      - bootc client JSON parsing (24 tests, 78.8% coverage)
      - Daemon state machine transitions (20 tests, 57.2% coverage)
      - Rollout orchestration logic (10 tests)
      - Drain manager (10 tests, 100% coverage)
      - Events (10 tests)
- [x] **Integration tests** (envtest):
      - Create BootcNodePool → verify conditions initialized
      - Simulate daemon-created BootcNodes → verify pool claims them
      - Delete BootcNodePool → verify finalizer cleans up
      - Overlapping pools → verify Degraded condition
      - Update image tag → verify rollout restarts
      - DaemonSet management (10 tests)
      - Auto rollback (7 tests)
      - Controller coverage: 79.4%
- [ ] **E2E tests** (existing `tests/` harness):
      - `tests/test-rollout.sh`: deploy operator + daemon, create
        BootcNodePool targeting the single-node cluster, verify staging,
        verify reboot, verify node is running the new image

## Verification

- **Unit tests**: Ginkgo v2 + Gomega + envtest for reconciler logic. Mock
  bootc CLI for daemon tests. Test rollout strategy, digest resolution, drain.
- **Integration tests**: envtest spins up real API server + etcd (no kubelet).
  Create BootcNodePool CRs, verify BootcNode CRDs are created, verify status
  conditions, verify finalizer cleanup.
- **E2E tests**: Use the existing `tests/` harness (`tests/run.sh`). Each
  test boots a real FCOS VM with a single-node K8s cluster (kubeadm),
  deploys the operator + daemon, and runs test scripts inside the VM.
  Real bootc is available on the host -- no fake binary needed. Supports
  reboots via the autopkgtest protocol (`/tmp/autopkgtest-reboot <mark>`).
  New tests go in `tests/test-<name>.sh`. Example: `test-rollout.sh` would
  create a BootcNodePool, verify staging, reboot, and verify the node is
  running the new image after reboot.
