package.path = "/etc/nginx/lua/?.lua;/etc/nginx/lua/?/init.lua;" .. package.path
local meta_http = require("meta_http")

ngx.header["X-Lua-Response-Phase"] = "header_filter"

local req_id = ngx.ctx.lua_request_id
if req_id and req_id ~= "" then
    ngx.header["X-Request-Id"] = req_id
end

local started = ngx.ctx.request_started_at
local elapsed_ms = nil
if started then
    elapsed_ms = math.floor((ngx.now() - started) * 1000)
end

meta_http.post_json_async("/internal/nginx-hooks/response", {
    phase = "response",
    requestId = req_id,
    method = ngx.req.get_method(),
    path = ngx.var.uri,
    status = ngx.status,
    elapsedMs = elapsed_ms,
    timestamp = ngx.time(),
})
