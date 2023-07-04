# This Lua policies validate that in namespace "foo"
# each deployment has an associated HorizontalPodAutoscaler

function evaluate()
    local hs = {}
    hs.valid = true
    hs.message = ""

    local deployments = {}
    local autoscalers = {}

    -- Separate deployments and services from the resources
    for _, resource in ipairs(resources) do
        local kind = resource.kind
        if resource.metadata.namespace == "foo" then
            if kind == "Deployment" then
                table.insert(deployments, resource)
            elseif kind == "HorizontalPodAutoscaler" then
                table.insert(autoscalers, resource)
            end
        end
    end

    -- Check for each deployment if there is a matching HorizontalPodAutoscaler
    for _, deployment in ipairs(deployments) do
        local deploymentName = deployment.metadata.name
        local matchingAutoscaler = false

        for _, autoscaler in ipairs(autoscalers) do
            if autoscaler.spec.scaleTargetRef.name == deployment.metadata.name then
                matchingAutoscaler = true
                break
            end
        end

        if not matchingAutoscaler then
            hs.valid = false
            hs.message = "No matching autoscaler found for deployment: " .. deploymentName
            break
        end
    end

    return hs
end