-- This method evaluates the health of a Deployment. Deployment is healthy
-- if number of available replicas matches number of requested replicas
-- obj represents a Deployment instance
-- hs is a struct with two fields:
-- healthy is a boolean indicating whether obj is healthy (true) or not (false)
-- message is a string. It is optional and can contain a message. In case resource
-- is not healthy, message is printed in the ClusterSummary Status
function evaluate()
  hs = {}
  hs.healthy = false
  hs.message = "available replicas not matching requested replicas"
  if obj.status ~= nil then
    if obj.status.availableReplicas ~= nil then
      if obj.status.availableReplicas == obj.spec.replicas then
        hs.healthy = true
        hs.message = ""
      end
    end
  end
  return hs
end
