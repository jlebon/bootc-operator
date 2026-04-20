# Kubernetes Operator Design Review Skill

This skill performs a comprehensive design review of Kubernetes operators against official API conventions.

## Overview

This skill provides a systematic checklist for reviewing Kubernetes operators against the official [API Conventions](https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md). Use it to ensure operators follow Kubernetes best practices for API design, controller implementation, and operational patterns.

### What This Skill Reviews

| Category | Focus Areas | Key Outputs |
|----------|-------------|-------------|
| **API Structure** | TypeMeta, ObjectMeta, spec/status separation | CRD compliance |
| **Field Design** | Naming, types, enums, references | Field-level issues |
| **Validation** | Defaults, required fields, immutability | Schema problems |
| **Status & Conditions** | Condition format, observability | Status reporting |
| **Ownership** | OwnerReferences, finalizers, lifecycle | Resource cleanup |
| **Controller Logic** | Reconciliation, watches, idempotency | Implementation bugs |
| **Performance** | API efficiency, watch optimization | Scale issues |
| **Security** | RBAC, least privilege, secrets | Security gaps |

### Review Scope

1. **API Design (CRDs)**: All Custom Resource Definitions
2. **Controller Implementation**: Reconciliation logic and patterns
3. **Architecture Patterns**: Kubernetes-native design principles

### Review Outcomes

- **✅ PASS**: Compliant with Kubernetes conventions
- **⚠️ WARNING**: Improvement opportunity, not breaking
- **❌ FAIL**: Non-compliant, requires fix for correctness/compatibility

---

## Instructions for AI Agent

Review the operator design and implementation against Kubernetes API conventions from https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md

### How to Use This Skill

**Step 1**: Identify the operator components to review:
- Locate all CRD definitions (YAML files or Go types with `+kubebuilder` markers)
- Locate controller/reconciler implementations
- Review ARCHITECTURE.md or design documentation if available

**Step 2**: For each CRD, apply the "CRD-Specific Review Template"

**Step 3**: For each controller, apply the "Controller Review Template"

**Step 4**: Apply all general checklist items (sections 1-10)

**Step 5**: Generate the review report using the output format below

### Review Process

For each review area below, check the implementation against the criteria and report:
- ✅ PASS: Compliant with convention
- ⚠️ WARNING: Minor deviation or improvement opportunity  
- ❌ FAIL: Non-compliant, requires fix

---

## Review Checklist

### 1. API Resource Structure

#### 1.1 TypeMeta and ObjectMeta
- [ ] All CRDs have `apiVersion` and `kind` fields
- [ ] All CRDs have proper `metadata` section
- [ ] Metadata includes `name`, `namespace` (if namespaced), `labels`, `annotations`
- [ ] Uses `ownerReferences` for garbage collection (BootcNode → BootcNodePool)

#### 1.2 Spec and Status Separation
- [ ] Clear separation between `spec` (desired state) and `status` (observed state)
- [ ] Spec contains only user-provided configuration
- [ ] Status contains only system-observed state
- [ ] Status fields are never inputs to the system (read-only from user perspective)
- [ ] No computed/derived fields in spec
- [ ] Controllers write status, users write spec (distinct authorization scopes)

#### 1.3 Resource Naming
- [ ] Kind names are CamelCase and singular (e.g., `Database`, `Backup`, `Certificate`)
- [ ] Resource names are lowercase and plural (e.g., `databases`, `backups`, `certificates`)
- [ ] Group name follows domain format (e.g., `example.com`, `apps.mycompany.io`)
- [ ] API version follows Kubernetes conventions (`v1alpha1`, `v1beta1`, `v1`)
- [ ] Short names defined for common CLI usage (optional)

### 2. Field Naming and Types

#### 2.1 Field Naming Conventions
- [ ] Field names use camelCase (not snake_case or kebab-case)
- [ ] Boolean fields use positive names (e.g., `paused` not `notPaused`)
- [ ] Time fields use suffix `Time` or include time units (e.g., `timeoutSeconds`)
- [ ] Quantity fields include units in name (e.g., `maxUnavailable`, `timeoutSeconds`)
- [ ] Reference fields use suffix `Ref` (e.g., `pullSecretRef`)

#### 2.2 Primitive Types
- [ ] Use `int32` or `int64` for integers (not `int`)
- [ ] Use `string` for text
- [ ] Use `metav1.Time` for timestamps (not `string`)
- [ ] Use `resource.Quantity` for storage/memory quantities
- [ ] Use `intstr.IntOrString` for fields accepting both int and percentage

#### 2.3 Enumerations
- [ ] Enum-like fields use `string` type with clear valid values documented
- [ ] Enum values are CamelCase constants
- [ ] Validation rules specify allowed enum values

### 3. Defaulting and Validation

#### 3.1 Defaulting
- [ ] Default values are clearly documented
- [ ] Defaults are set via OpenAPI schema or admission webhook
- [ ] Defaulting is idempotent (doesn't change on repeated applies)
- [ ] Defaults only set previously unset fields
- [ ] No defaults that depend on other fields' runtime values

#### 3.2 Validation
- [ ] Required fields are marked as required in schema
- [ ] Optional fields are marked with `+optional`
- [ ] Field value ranges/constraints are enforced via validation
- [ ] Cross-field validation is documented (e.g., mutual exclusivity)
- [ ] Immutable fields are protected (use CEL or webhook validation)

### 4. Status Conventions

#### 4.1 Conditions
- [ ] Status includes `conditions` array of type `[]metav1.Condition`
- [ ] Each condition has: `type`, `status`, `reason`, `message`, `lastTransitionTime`, `observedGeneration`
- [ ] Condition types are CamelCase (e.g., `UpToDate`, `Degraded`)
- [ ] Condition reasons are CamelCase (e.g., `AllUpdated`, `RolloutInProgress`)
- [ ] Conditions represent observations, not state machine transitions
- [ ] Positive polarity conditions used (e.g., `Ready` not `NotReady`)
- [ ] Long-running conditions (Ready, Available) come first in conditions array
- [ ] Each condition has clear, documented meanings for True/False/Unknown

#### 4.2 Observed State
- [ ] Status fields reflect actual observed system state
- [ ] Status includes `observedGeneration` to track spec-status sync
- [ ] Status never used as input to determine desired state
- [ ] Aggregate rollup data (counts, summaries) in parent resources

#### 4.3 Status Subresource
- [ ] CRD uses status subresource (separate `/status` endpoint)
- [ ] Spec and status have separate RBAC permissions
- [ ] Status updates don't modify spec
- [ ] Updates to status don't increment resourceVersion unnecessarily

### 5. References and Dependencies

#### 5.1 Object References
- [ ] References use consistent structure: `name` and optionally `namespace`
- [ ] Reference fields named with `Ref` suffix
- [ ] Namespaced resources prefer same-namespace references
- [ ] Cross-namespace references are explicit and justified
- [ ] References validated (target exists) before use

#### 5.2 Owner References
- [ ] Dependent resources use `ownerReferences` for cascade deletion
- [ ] Single controller per resource (one `controller: true` owner)
- [ ] Owner references don't cross namespace boundaries
- [ ] Finalizers used when cleanup order matters

### 6. Behavioral Conventions

#### 6.1 Idempotency
- [ ] Same spec applied multiple times produces same result
- [ ] Reconciliation is idempotent
- [ ] No action taken if desired state == current state
- [ ] Status updates don't trigger unnecessary reconciliation

#### 6.2 Level-Based Logic
- [ ] Controller logic based on observed state, not events
- [ ] No assumptions about event ordering or delivery
- [ ] Handles missed events gracefully
- [ ] Full reconciliation on every reconcile loop

#### 6.3 Optimistic Concurrency
- [ ] Uses `resourceVersion` for conflict detection
- [ ] Handles update conflicts by refetching and retrying
- [ ] No distributed locks or leases for per-resource updates

### 7. Advanced Patterns

#### 7.1 Finalizers
- [ ] Finalizers added before creating dependent resources
- [ ] Finalizer names use domain prefix (e.g., `example.com/cleanup`)
- [ ] Cleanup logic runs before removing finalizer
- [ ] Finalizer removal is idempotent
- [ ] Handles finalizer removal failures gracefully

#### 7.2 Immutability
- [ ] Immutable fields clearly documented
- [ ] Immutability enforced via validation (CEL or webhook)
- [ ] Changes to immutable fields rejected with clear error
- [ ] Consider using immutable create-only resources when appropriate

#### 7.3 Declarative vs Imperative
- [ ] API is declarative (spec describes desired state)
- [ ] No imperative actions in spec (e.g., no `action: restart`)
- [ ] Actions triggered by state changes, not commands
- [ ] State transitions handled by controllers, not encoded in spec

### 8. Scale and Performance

#### 8.1 Watch Efficiency
- [ ] Controllers watch only necessary resources
- [ ] Field selectors used to filter watches when possible
- [ ] Watch predicates filter irrelevant events
- [ ] No polling loops (use watches and informers)

#### 8.2 API Load
- [ ] Minimize per-reconcile API calls
- [ ] Use cached informers, not direct API queries
- [ ] Batch updates when possible
- [ ] Rate limit reconciliation for high-churn resources

#### 8.3 Status Updates
- [ ] Status updated only on actual state changes
- [ ] No periodic heartbeat status updates
- [ ] Aggregate status minimizes per-node API writes

### 9. Security and RBAC

#### 9.1 Least Privilege
- [ ] ServiceAccounts have minimal required permissions
- [ ] Separate RBAC for spec and status subresources
- [ ] No cluster-admin or overly broad permissions
- [ ] Secrets accessed only when necessary

#### 9.2 Multi-Tenancy
- [ ] Namespace isolation respected
- [ ] No cluster-scoped resources unless necessary
- [ ] Cross-namespace access explicitly validated
- [ ] Resource quotas and limits compatible

### 10. Documentation and Observability

#### 10.1 API Documentation
- [ ] All fields have clear descriptions in schema
- [ ] Examples provided for common use cases
- [ ] Default values documented
- [ ] Valid value ranges/enums documented
- [ ] OpenAPI schema complete and accurate

#### 10.2 Observability
- [ ] Conditions provide clear status visibility
- [ ] Error messages actionable and specific
- [ ] Events emitted for significant state changes
- [ ] Metrics exposed for operator health
- [ ] Logs at appropriate levels (not debug in production)

---

## CRD-Specific Review Template

For each Custom Resource Definition in the operator, review:

### CRD: [Resource Name]

**1. Spec Fields Review**:
   - [ ] Field types appropriate (string, int32/int64, boolean, etc.)
   - [ ] Nested object structures clear and well-documented
   - [ ] Enum fields use string type with validation
   - [ ] Selectors use metav1.LabelSelector or metav1.FieldSelector
   - [ ] Quantity fields use resource.Quantity or include units in name
   - [ ] Duration fields use int with time unit suffix (e.g., timeoutSeconds)
   - [ ] References use consistent naming (e.g., `*Ref` suffix)
   - [ ] All required fields marked as required
   - [ ] All optional fields marked with +optional

**2. Status Fields Review**:
   - [ ] Conditions array present and properly typed ([]metav1.Condition)
   - [ ] observedGeneration field present
   - [ ] Counter/aggregate fields use appropriate int types
   - [ ] Timestamps use metav1.Time type
   - [ ] Status reflects actual observed state only
   - [ ] No user-writable fields in status
   - [ ] Hash/checksum fields for change detection clearly named

**3. Validation Rules**:
   - [ ] Required fields validated
   - [ ] Value ranges enforced (min/max)
   - [ ] Format validation for structured strings (URLs, digests, etc.)
   - [ ] Cross-field validation documented or implemented
   - [ ] Immutable fields protected with CEL or webhook
   - [ ] Enum values explicitly listed

**4. Ownership and Lifecycle**:
   - [ ] OwnerReferences set for dependent resources
   - [ ] Single controller owner designated (controller: true)
   - [ ] Finalizers use domain-prefixed names
   - [ ] Cascade deletion behavior correct
   - [ ] Finalizer cleanup logic idempotent

---

## Controller Review Template

For each controller in the operator, review:

### Controller: [Controller Name]

**1. Reconciliation Logic**:
   - [ ] Implements full reconcile on every call (level-based, not edge-based)
   - [ ] No assumptions about event ordering or delivery
   - [ ] Handles missed events gracefully (reads current state)
   - [ ] Idempotent - same input produces same output
   - [ ] No-op when desired state == current state

**2. Watch Configuration**:
   - [ ] Watches only necessary resources
   - [ ] Correct watch-to-reconcile key mapping
   - [ ] Predicates filter irrelevant events (e.g., status-only updates)
   - [ ] Field selectors used when appropriate
   - [ ] Handles watch errors and reconnection

**3. State Management**:
   - [ ] State derived from spec+status, not stored separately
   - [ ] State transitions are clear and documented
   - [ ] No race conditions between state transitions
   - [ ] Error/degraded states handled gracefully
   - [ ] Terminal states (success/failure) reached appropriately

**4. API Efficiency**:
   - [ ] Uses cached informers, not direct API queries in hot path
   - [ ] Minimizes API calls per reconcile
   - [ ] Status updates only on actual changes
   - [ ] No periodic heartbeat updates
   - [ ] Batch operations when possible
   - [ ] RequeueAfter for polling instead of tight loops

**5. Error Handling**:
   - [ ] Transient errors return error to requeue
   - [ ] Permanent errors set Degraded/Failed condition
   - [ ] Error messages actionable and specific
   - [ ] Retry logic has backoff
   - [ ] Errors don't block other resources

**6. Concurrency**:
   - [ ] Uses resourceVersion for optimistic concurrency
   - [ ] Handles update conflicts gracefully (refetch and retry)
   - [ ] No distributed locks for single-resource operations
   - [ ] Thread-safe access to shared state
   - [ ] MaxConcurrentReconciles set appropriately

---

## Output Format

For each review section, provide a structured report:

```markdown
### [Section Name] - [CRD/Controller Name]

**Status**: ✅ PASS | ⚠️ WARNING | ❌ FAIL

**Findings**:
- ✅ Field naming follows camelCase convention
- ⚠️ Missing observedGeneration in status
- ❌ Status fields used as input to reconciliation logic

**Required Actions** (if FAIL):
1. Add `observedGeneration` field to status subresource
2. Remove controller logic that reads status.processedCount as input
3. Move processedCount calculation to be derived from spec

**Recommendations** (if WARNING):
1. Consider adding short names for CLI convenience
2. Add validation webhook for cross-field constraints
```

**Example Output**:

```markdown
### API Resource Structure - Database CRD

**Status**: ⚠️ WARNING

**Findings**:
- ✅ TypeMeta and ObjectMeta properly defined
- ✅ Clear spec/status separation
- ⚠️ Missing observedGeneration field in status
- ✅ Owner references set correctly

**Required Actions**: None (warnings only)

**Recommendations**:
1. Add observedGeneration to track spec-status synchronization
2. Document spec/status separation in API comments
```

---

## Final Summary Template

Provide an executive summary in this format:

```markdown
# Kubernetes Operator Design Review - [Operator Name]

**Review Date**: [Date]
**Operator Version**: [Version]
**Reviewer**: [AI Agent/Human]

## Summary Statistics
- **Total Review Items**: [count]
- **✅ PASS**: [count] ([percentage]%)
- **⚠️ WARNING**: [count] ([percentage]%)
- **❌ FAIL**: [count] ([percentage]%)

## Critical Issues (FAIL)
1. [Issue 1] - [CRD/Controller affected]
2. [Issue 2] - [CRD/Controller affected]
...

## Important Improvements (WARNING)
1. [Improvement 1]
2. [Improvement 2]
...

## Overall Assessment
[Overall compliance level: Excellent | Good | Needs Improvement | Non-Compliant]

[Brief narrative assessment]

## Next Steps
1. [Prioritized action item]
2. [Prioritized action item]
...
```

---

## Quick Start Example

To use this skill for an operator review:

1. **Locate the operator code**:
   ```bash
   # Find CRD definitions
   find . -name "*_types.go" -o -name "*.crd.yaml"
   
   # Find controllers
   find . -path "*/controllers/*" -name "*.go"
   ```

2. **Read the architecture documentation**:
   - Check for ARCHITECTURE.md, DESIGN.md, or README.md
   - Review CRD API documentation

3. **Apply this skill**:
   - Review each CRD using the CRD template
   - Review each controller using the Controller template
   - Check all general conventions (sections 1-10)
   - Generate the final summary report

4. **Prioritize findings**:
   - ❌ FAIL items are API compatibility or correctness issues
   - ⚠️ WARNING items are improvement opportunities
   - ✅ PASS items confirm good practices

---

## References

- **Kubernetes API Conventions**: https://github.com/kubernetes/community/blob/main/contributors/devel/sig-architecture/api-conventions.md
- **Operator Best Practices**: https://sdk.operatorframework.io/docs/best-practices/
- **CRD Validation**: https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/
- **Controller Runtime**: https://github.com/kubernetes-sigs/controller-runtime
- **Kubebuilder Book**: https://book.kubebuilder.io/
