local cjson = require("cjson.safe")

local _M = {}

local function send_json(path, payload)
    local payload_json = cjson.encode(payload) or "{}"
    local sock = ngx.socket.tcp()
    sock:settimeout(1000)

    local ok, err = sock:connect("server-debug", 8080)
    if not ok then
        ngx.log(ngx.ERR, "meta_http connect failed: ", err)
        return
    end

    local request = table.concat({
        "POST ", path, " HTTP/1.1\r\n",
        "Host: server-debug\r\n",
        "Content-Type: application/json\r\n",
        "Connection: close\r\n",
        "Content-Length: ", tostring(#payload_json), "\r\n\r\n",
        payload_json,
    })

    local _, send_err = sock:send(request)
    if send_err then
        ngx.log(ngx.ERR, "meta_http send failed: ", send_err)
    end

    sock:close()
end

function _M.post_json_async(path, payload)
    local payload_copy = payload or {}
    local ok, err = ngx.timer.at(0, function(premature)
        if premature then
            return
        end
        send_json(path, payload_copy)
    end)

    if not ok then
        ngx.log(ngx.ERR, "meta_http timer failed: ", err)
    end
end

return _M
