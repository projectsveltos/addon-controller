# Reconciliation Storm Fix — addon-controller

## Problem Statement

Two related production issues were observed when managing multiple child clusters
with 10+ services each via ClusterSummary Helm features:

### Problem 1: Resource Version Conflict Storm

When deploying services incrementally (updating ClusterDeployment spec one service
at a time), the addon-controller and an external controller (k0rdent Controller
Manager / KCM) race to update the same ClusterSummary resource. This produces
hundreds of `"the object has been modified; please apply your changes to the latest
version and try again"` errors per minute, with conflict counts exceeding 50-100
per 30-second window. Services eventually reconcile but take 3-5x longer than
expected.

### Problem 2: CPU-Thrashing Stall (No Conflicts, Just Stuck)

On a bare-metal cluster (3 child clusters, 11-13 services each), the
addon-controller consumed ~3 CPU cores continuously, reconciling ClusterSummaries
in a tight loop (Profile -> ClusterSummary cycling every ~100ms) without producing
conflict errors. The `observedGeneration` on the ClusterDeployment stopped advancing
for 30+ minutes. A manual restart resolved it immediately.

**Evidence observed:**
- Rapid-fire "Reconciling ClusterSummary" -> "success" -> "Reconciling" cycles at ~100ms
- `kubectl top pod` showing addon-controller at 2977m CPU (nearly 3 cores)
- No CPU limits set on the addon-controller deployment
- "empty selector matches no cluster" log line appearing frequently

---

## Root Cause Analysis

### Architecture Background

The reconciliation flow is unidirectional:

```
Profile/ClusterProfile --> creates/updates --> ClusterSummary --> deploys to --> Target Cluster
```

The ClusterSummary controller uses a deferred `scope.Close()` pattern (via the
cluster-api `patch.Helper`) that patches both spec/metadata and status after each
reconciliation. The `ClusterSummaryPredicate` watches for FeatureSummary hash
changes to trigger re-reconciliation.

### Root Cause 1: Watch Events Bypass RequeueAfter Delays

controller-runtime's `RequeueAfter` adds items to the workqueue with a delay. However,
watch events from `scope.Close()` status patches call `queue.Add()` — which is **not**
rate-limited. The watch event immediately re-enqueues the item, and the workqueue
processes whichever enqueue fires first (usually the immediate watch event).

The existing `NextReconcileTime` field in ClusterSummary status was designed as an
application-level guard in `skipReconciliation()`, but it was only set in **one**
code path: `HandOverError` during undeploy (`clustersummary_deployer.go:438`). It was
never set during normal deploy flow, making the guard effectively dead code for the
tight-loop scenario.

**The feedback loop:**

```
Reconcile() --> deploy --> sets hash in status --> scope.Close() patches
                                                        |
                                                   Watch event fires
                                                        |
                                              ClusterSummaryPredicate: hash changed!
                                                        |
                                              IMMEDIATE re-enqueue (ignores RequeueAfter)
                                                        |
                                                   Reconcile() again
```

### Root Cause 2: Conflict Errors Create Cascading Retries

When both addon-controller and KCM update the same ClusterSummary:

1. addon-controller reads ClusterSummary (resourceVersion: N)
2. KCM updates ClusterSummary spec (resourceVersion: N+1)
3. addon-controller's `scope.Close()` tries to patch status with resourceVersion N -> **conflict**
4. addon-controller returns `Result{RequeueAfter: ConflictRetryTime}` (60s)
5. But KCM's successful update fires a watch event -> ClusterSummaryPredicate sees spec changed -> **immediately re-enqueues**, bypassing the 60s delay

The cluster-api `patch.Helper.Patch()` issues separate patches for spec/metadata and
status (using the status subresource), but both carry `resourceVersion` in the
`MergeFrom` patch. A spec update from KCM invalidates the addon-controller's status
patch too.

### Root Cause 3: shouldReconcile Always Returns True for Continuous Mode

`shouldReconcile()` unconditionally returns `true` for `SyncModeContinuous` and
`SyncModeContinuousWithDriftDetection`, running the full reconciliation flow even
when nothing has changed. Combined with the watch-event bypass, this enables a
tight loop where every reconciliation triggers another via status patch watch events.

### Root Cause 4: Redundant Status Patches Generate Watch Events

When a feature is already `Provisioned` with an unchanged hash, `proceedDeployingFeature`
still calls `updateFeatureStatus`, which writes the same status values (including a
new `LastAppliedTime`). This causes `scope.Close()` to detect a diff and issue a
status patch, generating a watch event even though nothing meaningful changed.

---

## Fix Description

Three files changed, 85 insertions, 5 deletions.

### Fix 1: Activate the NextReconcileTime Guard (Primary Fix)

**Files:** `controllers/clustersummary_controller.go`

Added `r.setNextReconcileTime()` calls before every `return reconcile.Result{RequeueAfter: X}`
across 10 call sites in `reconcileNormal()` and `proceedDeployingClusterSummary()`.

This activates the existing `skipReconciliation()` guard. When a watch event
re-enqueues the item before the intended delay, `skipReconciliation()` finds
`NextReconcileTime` is still in the future and skips the reconciliation.

**Call sites instrumented:**

| Method | Error Condition | Backoff Duration |
|--------|----------------|-----------------|
| `reconcileNormal` | Watcher start failure | `deleteRequeueAfter` (10s) |
| `reconcileNormal` | Dependency check error | `normalRequeueAfter` (10s) |
| `reconcileNormal` | Dependencies not deployed | `normalRequeueAfter` (10s) |
| `reconcileNormal` | Chart map update error | `normalRequeueAfter` (10s) |
| `reconcileNormal` | ResourceSummary removal error | `normalRequeueAfter` (10s) |
| `proceedDeployingClusterSummary` | ConflictError | `ConflictRetryTime` (60s) |
| `proceedDeployingClusterSummary` | HealthCheckError | `HealthErrorRetryTime` (60s) |
| `proceedDeployingClusterSummary` | General deploy error | Exponential backoff (20-60s) |
| `proceedDeployingClusterSummary` | DryRun success | `dryRunRequeueAfter` (60s) |

### Fix 2: In-Memory Cooldown Map (Conflict Resilience)

**Files:** `controllers/clustersummary_controller.go`

Added `NextReconcileTimes map[types.NamespacedName]time.Time` to the reconciler struct.
The `setNextReconcileTime` method writes to both the status field AND the in-memory map.
The `skipReconciliation` method checks both sources.

**Why this is needed:** When `scope.Close()` encounters a conflict error (Fix 3
swallows these), the `NextReconcileTime` status field is never persisted to etcd.
The next reconciliation fetches a fresh object with no `NextReconcileTime`, which
would bypass the guard entirely. The in-memory map survives status-patch conflicts,
ensuring the backoff is enforced regardless.

This follows the same pattern as the existing `DeletedInstances` map used for
deletion throttling.

### Fix 3: Precise Remaining-Time Requeue

**Files:** `controllers/clustersummary_controller.go`

When `skipReconciliation()` triggers, the requeue delay now uses the actual remaining
time from `NextReconcileTime` (or the in-memory cooldown) instead of the hardcoded
`normalRequeueAfter` (10s). This prevents unnecessary wake-ups when the original
backoff was longer (e.g., 60s for ConflictRetryTime).

**Before:** If ConflictRetryTime is 60s, the controller would wake up every 10s just
to call `skipReconciliation()` and skip — 5 wasted cycles.

**After:** The controller wakes up once, right when the cooldown expires.

### Fix 4: Swallow Conflict Errors in scope.Close()

**Files:** `controllers/clustersummary_controller.go`

Conflict errors from `scope.Close()` are now logged at debug level and swallowed
instead of being propagated to controller-runtime. The watch event from whatever
caused the conflict will re-enqueue the resource, and the next reconciliation will
recompute status.

Propagating the conflict error would cause controller-runtime to immediately requeue
(its default error-handling behavior), bypassing the intended `NextReconcileTime`
backoff. The in-memory cooldown map (Fix 2) ensures the guard still works.

### Fix 5: Custom Rate Limiter

**Files:** `controllers/clustersummary_controller.go`

Configured the ClusterSummary controller workqueue with an exponential failure rate
limiter starting at 1 second (vs the default 5ms). This provides infrastructure-level
protection — even if application-level guards are bypassed, the workqueue ensures
exponential delay between processing the same failed item.

```go
RateLimiter: workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
    1*time.Second,  // base delay (default is 5ms)
    5*time.Minute,  // max delay
),
```

### Fix 6: Skip Redundant Status Patches

**Files:** `controllers/clustersummary_deployer.go`

In `proceedDeployingFeature`, when the deployer result is `FeatureStatusProvisioned`
and the feature already has the same hash and status, the `updateFeatureStatus` call
is skipped entirely. This avoids unnecessary status patches that would generate watch
events and re-enqueue the item.

```go
if existingFS := getFeatureSummaryForFeatureID(clusterSummary, f.id); existingFS != nil &&
    existingFS.Status == libsveltosv1beta1.FeatureStatusProvisioned &&
    reflect.DeepEqual(existingFS.Hash, currentHash) {
    // Status is already correct, no update needed
    return nil
}
```

---

## How the Fixes Interact

```
Watch event arrives --> Reconcile() --> skipReconciliation()
                                              |
                                   Check in-memory cooldown map
                                   + Check status.NextReconcileTime
                                              |
                                   Too soon? --> skip, requeue at exact remaining time
                                   Expired?  --> proceed with reconciliation
                                              |
                                   On error: set NextReconcileTime
                                             (both in-memory + status)
                                              |
                                   scope.Close() --> conflict? --> swallow
                                                     (in-memory guard still active)
                                              |
                                   Feature already provisioned with same hash?
                                   --> skip status update (no watch event generated)
```

Defense in depth:
- **Fix 1** (NextReconcileTime) prevents reconciliation at the application level
- **Fix 2** (in-memory map) ensures the guard survives status-patch conflicts
- **Fix 3** (precise requeue) reduces unnecessary wake-ups
- **Fix 4** (swallow conflicts) prevents controller-runtime from bypassing backoff
- **Fix 5** (rate limiter) provides infrastructure-level protection
- **Fix 6** (skip redundant patches) reduces unnecessary watch events at the source

---

## Files Changed

| File | Lines | Description |
|------|-------|-------------|
| `controllers/clustersummary_controller.go` | +79/-5 | Fixes 1-5: NextReconcileTime guard, in-memory cooldown, precise requeue, conflict handling, rate limiter |
| `controllers/clustersummary_deployer.go` | +9 | Fix 6: Skip redundant status patches |
| `controllers/export_test.go` | +2 | Export `setNextReconcileTime` for test access |

---

## Testing

### Unit Tests

All existing unit tests pass with no regressions:
- `lib/clusterops`: 11/11 specs passed
- `pkg/scope`: passed (64.5% coverage)
- `controllers/dependencymanager`: passed

### Build Verification

```
go build ./...   # Clean
go vet ./...     # Clean
```

### Functional Verification

To run FV tests against a Kind cluster:

```bash
# Build arm64 image (for Apple Silicon, no Rosetta needed)
docker build --load --build-arg BUILDOS=linux --build-arg TARGETARCH=arm64 \
  -t projectsveltos/addon-controller:v1.6.0 .

make create-cluster
make fv
```

### Manual Verification

To verify the fix under the exact production conditions:

1. Deploy addon-controller with the fix to a management cluster
2. Create a ClusterProfile targeting a child cluster with 10+ Helm services
3. Incrementally update the ClusterDeployment spec (add one service at a time)
4. Monitor:
   - `kubectl top pod` for addon-controller CPU (should stay < 500m)
   - addon-controller logs for conflict error frequency (should be < 5/minute)
   - `observedGeneration` advancement (should not stall)

---

## Risk Assessment

| Fix | Risk | Rationale |
|-----|------|-----------|
| NextReconcileTime guard | Very low | Uses existing API field and existing guard function |
| In-memory cooldown map | Low | Follows established `DeletedInstances` pattern; map is protected by existing `PolicyMux` |
| Precise remaining-time requeue | Very low | Pure optimization of existing skip path |
| Swallow conflict in scope.Close | Low-medium | In-memory cooldown ensures backoff is enforced even without etcd persistence |
| Custom rate limiter | Low | Only affects retry timing; happy path is unaffected |
| Skip redundant status patches | Low | Pure optimization; `reflect.DeepEqual` on hash is deterministic |

---

## Related Work

- Commit `92f0c47` (merged 2026-03-16): "refactor: use deterministic JSON hashing for
  Unstructured objects" — addresses a related issue where non-deterministic hashing via
  `render.AsCode` could cause hash changes on every reconciliation, contributing to
  the tight loop. Our fix is complementary: even with deterministic hashing, the
  watch-event bypass of RequeueAfter still needed to be addressed.
