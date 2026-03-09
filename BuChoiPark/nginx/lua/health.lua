local cjson = require("cjson")

local payload = {
    status = "ok",
    source = "nginx-lua",
    ts = ngx.time()
}

ngx.status = ngx.HTTP_OK
ngx.header["Content-Type"] = "application/json"
ngx.say(cjson.encode(payload))
