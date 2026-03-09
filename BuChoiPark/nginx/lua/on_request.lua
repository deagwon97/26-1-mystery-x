package.path = "/etc/nginx/lua/?.lua;/etc/nginx/lua/?/init.lua;" .. package.path
local meta_http = require("meta_http")

local req_id = ngx.var.request_id

if not req_id or req_id == "" then
    req_id = tostring(ngx.now())
end

ngx.ctx.lua_request_id = req_id
ngx.ctx.request_started_at = ngx.now()
ngx.req.set_header("X-Request-Id", req_id)
ngx.req.set_header("X-Lua-Request-Phase", "rewrite")

meta_http.post_json_async("/internal/nginx-hooks/request", {
    phase = "request",
    requestId = req_id,
    method = ngx.req.get_method(),
    path = ngx.var.uri,
    remoteAddr = ngx.var.remote_addr,
    timestamp = ngx.time(),
})
