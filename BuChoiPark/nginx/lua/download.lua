local cjson = require("cjson.safe")

if ngx.req.get_method() ~= "GET" then
    return ngx.exit(ngx.HTTP_NOT_ALLOWED)
end

local id = ngx.var.uri:match("^/files/([0-9a-fA-F-]+)/download$")
if not id then
    return ngx.exit(ngx.HTTP_BAD_REQUEST)
end

local meta_res = ngx.location.capture("/_meta/download/" .. id, {
    method = ngx.HTTP_GET,
})

if meta_res.status == ngx.HTTP_NOT_FOUND then
    return ngx.exit(ngx.HTTP_NOT_FOUND)
end
if meta_res.status < 200 or meta_res.status >= 300 then
    ngx.status = meta_res.status
    ngx.header["Content-Type"] = "application/json"
    ngx.say(meta_res.body)
    return
end

local metadata = cjson.decode(meta_res.body)
if not metadata then
    return ngx.exit(ngx.HTTP_INTERNAL_SERVER_ERROR)
end

local file_path = "/app/data/uploads/" .. id
local file = io.open(file_path, "rb")
if not file then
    return ngx.exit(ngx.HTTP_NOT_FOUND)
end

ngx.status = ngx.HTTP_OK
ngx.header["Content-Type"] = "application/octet-stream"
ngx.header["Content-Disposition"] = "attachment; filename*=UTF-8''" .. ngx.escape_uri(metadata.fileName or (id .. ".bin"))
if metadata.fileSize and metadata.fileSize >= 0 then
    ngx.header["Content-Length"] = tostring(metadata.fileSize)
end

while true do
    local chunk = file:read(65536)
    if not chunk then
        break
    end
    ngx.print(chunk)
end

file:close()
