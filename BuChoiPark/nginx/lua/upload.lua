local cjson = require("cjson.safe")

if ngx.req.get_method() ~= "POST" then
    return ngx.exit(ngx.HTTP_NOT_ALLOWED)
end

local content_type = (ngx.req.get_headers()["Content-Type"] or ""):lower()
if content_type:find("multipart/form-data", 1, true) then
    return ngx.exec("/_legacy/upload")
end

local headers = ngx.req.get_headers()
local user_id = headers["X-User-Id"]
local file_path = headers["X-File-Path"]
local file_name = headers["X-File-Name"]

if not user_id or user_id == "" or not file_path or file_path == "" or not file_name or file_name == "" then
    ngx.status = ngx.HTTP_BAD_REQUEST
    ngx.header["Content-Type"] = "application/json"
    ngx.say(cjson.encode({ message = "X-User-Id, X-File-Path, X-File-Name headers are required" }))
    return
end

ngx.req.read_body()
local body_data = ngx.req.get_body_data()
local body_file = ngx.req.get_body_file()

if not body_data and not body_file then
    ngx.status = ngx.HTTP_BAD_REQUEST
    ngx.header["Content-Type"] = "application/json"
    ngx.say(cjson.encode({ message = "request body is required" }))
    return
end

local file_size = 0
if body_data then
    file_size = #body_data
else
    local f = io.open(body_file, "rb")
    if not f then
        return ngx.exit(ngx.HTTP_INTERNAL_SERVER_ERROR)
    end
    file_size = f:seek("end") or 0
    f:close()
end

local meta_body = cjson.encode({
    userId = user_id,
    filePath = file_path,
    fileName = file_name,
    fileSize = file_size,
})

local meta_res = ngx.location.capture("/_meta/upload", {
    method = ngx.HTTP_POST,
    body = meta_body,
})

if meta_res.status < 200 or meta_res.status >= 300 then
    ngx.status = meta_res.status
    ngx.header["Content-Type"] = "application/json"
    ngx.say(meta_res.body)
    return
end

local metadata = cjson.decode(meta_res.body)
if not metadata or not metadata.id then
    return ngx.exit(ngx.HTTP_INTERNAL_SERVER_ERROR)
end

local target_path = "/app/data/uploads/" .. metadata.id

local out = io.open(target_path, "wb")
if not out then
    return ngx.exit(ngx.HTTP_INTERNAL_SERVER_ERROR)
end

if body_data then
    out:write(body_data)
else
    local input = io.open(body_file, "rb")
    if not input then
        out:close()
        return ngx.exit(ngx.HTTP_INTERNAL_SERVER_ERROR)
    end

    while true do
        local chunk = input:read(65536)
        if not chunk then
            break
        end
        out:write(chunk)
    end

    input:close()
end

out:close()

ngx.status = ngx.HTTP_OK
ngx.header["Content-Type"] = "application/json"
ngx.say(cjson.encode(metadata))
