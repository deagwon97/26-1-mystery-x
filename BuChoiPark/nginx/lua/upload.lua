local cjson = require("cjson.safe")

if ngx.req.get_method() ~= "POST" then
    return ngx.exit(ngx.HTTP_NOT_ALLOWED)
end

-- Spring Boot 로 직접 프록시
return ngx.exec("/_spring_upload")
