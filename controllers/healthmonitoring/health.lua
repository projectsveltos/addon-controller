-- Sveltos health monitoring: health of the Deployments, StatefulSets, DaemonSets, Jobs and
-- PersistentVolumeClaims deployed by a profile.
--
-- addon-controller prepends a "config" table to this script:
--   config.ownerKind    ClusterProfile or Profile
--   config.ownerName    name of the profile
--   config.helmReleases set of "<namespace>/<release name>" for the Helm charts of the profile
--
-- The health rules follow the ones of Argo CD (gitops-engine, pkg/health, Apache-2.0).

local function isOwned(resource)
  local annotations = (resource.metadata or {}).annotations or {}

  -- Resources deployed by Sveltos (raw YAML, Kustomize, ...) carry the owner annotations
  if annotations["projectsveltos.io/owner-kind"] == config.ownerKind and
     annotations["projectsveltos.io/owner-name"] == config.ownerName then
    return true
  end

  -- Resources deployed by a Helm chart carry the Helm release annotations
  local release = annotations["meta.helm.sh/release-name"]
  local namespace = annotations["meta.helm.sh/release-namespace"]
  if release ~= nil and namespace ~= nil then
    return config.helmReleases[namespace .. "/" .. release] == true
  end

  return false
end

local function deploymentHealth(resource)
  local metadata = resource.metadata or {}
  local spec = resource.spec or {}
  local status = resource.status or {}

  if spec.paused then
    return "Suspended", "Deployment is paused"
  end

  if (metadata.generation or 0) > (status.observedGeneration or 0) then
    return "Progressing", "Waiting for rollout to finish: observed deployment generation less than desired generation"
  end

  for _, condition in ipairs(status.conditions or {}) do
    if condition.type == "Progressing" and condition.reason == "ProgressDeadlineExceeded" then
      return "Degraded", string.format("Deployment %q exceeded its progress deadline", metadata.name or "")
    end
  end

  local desired = spec.replicas or 1
  local updated = status.updatedReplicas or 0
  local current = status.replicas or 0
  local available = status.availableReplicas or 0

  if updated < desired then
    return "Progressing", string.format(
      "Waiting for rollout to finish: %d out of %d new replicas have been updated", updated, desired)
  end
  if current > updated then
    return "Progressing", string.format(
      "Waiting for rollout to finish: %d old replicas are pending termination", current - updated)
  end
  if available < updated then
    return "Progressing", string.format(
      "Waiting for rollout to finish: %d of %d updated replicas are available", available, updated)
  end

  return "Healthy", ""
end

local function statefulSetHealth(resource)
  local metadata = resource.metadata or {}
  local spec = resource.spec or {}
  local status = resource.status or {}

  local observed = status.observedGeneration or 0
  if observed == 0 or (metadata.generation or 0) > observed then
    return "Progressing", "Waiting for statefulset spec update to be observed"
  end

  local ready = status.readyReplicas or 0
  if spec.replicas ~= nil and ready < spec.replicas then
    return "Progressing", string.format("Waiting for %d pods to be ready", spec.replicas - ready)
  end

  local strategy = spec.updateStrategy or {}
  if strategy.type == "OnDelete" then
    return "Healthy", ""
  end

  if strategy.type == "RollingUpdate" and strategy.rollingUpdate ~= nil then
    local partition = strategy.rollingUpdate.partition
    if partition ~= nil and spec.replicas ~= nil then
      local expected = spec.replicas - partition
      local updated = status.updatedReplicas or 0
      if updated < expected then
        return "Progressing", string.format(
          "Waiting for partitioned roll out to finish: %d out of %d new pods have been updated", updated, expected)
      end
    end
    return "Healthy", ""
  end

  if status.updateRevision ~= status.currentRevision then
    return "Progressing", string.format("Waiting for rolling update to finish: current revision %s, update revision %s",
      tostring(status.currentRevision), tostring(status.updateRevision))
  end

  return "Healthy", ""
end

local function daemonSetHealth(resource)
  local metadata = resource.metadata or {}
  local spec = resource.spec or {}
  local status = resource.status or {}

  if (metadata.generation or 0) > (status.observedGeneration or 0) then
    return "Progressing", "Waiting for rollout to finish: observed daemon set generation less than desired generation"
  end

  local strategy = spec.updateStrategy or {}
  if strategy.type == "OnDelete" then
    return "Healthy", ""
  end

  local desired = status.desiredNumberScheduled or 0
  local updated = status.updatedNumberScheduled or 0
  local available = status.numberAvailable or 0

  if updated < desired then
    return "Progressing", string.format(
      "Waiting for rollout to finish: %d out of %d new pods have been updated", updated, desired)
  end
  if available < desired then
    return "Progressing", string.format(
      "Waiting for rollout to finish: %d of %d updated pods are available", available, desired)
  end

  return "Healthy", ""
end

-- A Job is finished once the Job controller adds a Failed or a Complete condition. Until then it is
-- Progressing, a Job that is running is reported like Argo CD does.
local function jobHealth(resource)
  local status = resource.status or {}

  local complete = false
  local suspended = false
  for _, condition in ipairs(status.conditions or {}) do
    if condition.status == "True" then
      if condition.type == "Failed" then
        return "Degraded", string.format("Job failed: %s", condition.message or condition.reason or "unknown reason")
      elseif condition.type == "Complete" then
        complete = true
      elseif condition.type == "Suspended" then
        suspended = true
      end
    end
  end

  if complete then
    return "Healthy", ""
  end
  if suspended then
    return "Suspended", "Job is suspended"
  end

  return "Progressing", "Job has not completed yet"
end

-- A PersistentVolumeClaim whose storage class waits for the first consumer stays Pending until a Pod
-- uses it: the phase alone cannot tell it from a claim that cannot be bound.
local function persistentVolumeClaimHealth(resource)
  local phase = (resource.status or {}).phase

  if phase == "Bound" then
    return "Healthy", ""
  elseif phase == "Lost" then
    return "Degraded", "PersistentVolumeClaim lost its PersistentVolume"
  elseif phase == "Pending" then
    return "Progressing", "PersistentVolumeClaim is not bound yet"
  end

  return "Progressing", string.format("PersistentVolumeClaim has no known phase yet (%s)", tostring(phase))
end

local healthByKind = {
  Deployment = deploymentHealth,
  StatefulSet = statefulSetHealth,
  DaemonSet = daemonSetHealth,
  Job = jobHealth,
  PersistentVolumeClaim = persistentVolumeClaimHealth,
}

function evaluate()
  local hs = {}
  hs.status = "Healthy"
  hs.message = ""

  local unhealthy = {}
  -- The same object can be selected twice: by label (deployed by Sveltos) and by namespace (Helm)
  local seen = {}

  for _, resource in ipairs(resources) do
    local uid = (resource.metadata or {}).uid
    local health = healthByKind[resource.kind]

    if health ~= nil and (uid == nil or not seen[uid]) and isOwned(resource) then
      if uid ~= nil then
        seen[uid] = true
      end

      local status, message = health(resource)
      if status ~= "Healthy" then
        table.insert(unhealthy, {resource = resource, status = status, message = message})
      end
    end
  end

  if #unhealthy > 0 then
    hs.resources = unhealthy
  end

  return hs
end
