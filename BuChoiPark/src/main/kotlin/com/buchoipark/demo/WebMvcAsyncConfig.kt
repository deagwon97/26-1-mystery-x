package com.buchoipark.demo

import org.springframework.beans.factory.annotation.Value
import org.springframework.context.annotation.Configuration
import org.springframework.scheduling.concurrent.ThreadPoolTaskExecutor
import org.springframework.web.servlet.config.annotation.AsyncSupportConfigurer
import org.springframework.web.servlet.config.annotation.WebMvcConfigurer

@Configuration
class WebMvcAsyncConfig(
    @Value("\${app.download.async.core-pool-size:16}")
    private val corePoolSize: Int,
    @Value("\${app.download.async.max-pool-size:64}")
    private val maxPoolSize: Int,
    @Value("\${app.download.async.queue-capacity:200}")
    private val queueCapacity: Int,
    @Value("\${app.download.async.request-timeout-ms:120000}")
    private val requestTimeoutMs: Long,
) : WebMvcConfigurer {
    override fun configureAsyncSupport(configurer: AsyncSupportConfigurer) {
        // StreamingResponseBody는 MVC async 처리로 실행됩니다.
        // 다운로드 트래픽이 커져도 스레드가 무한정 늘지 않도록 bounded executor를 사용합니다.
        val executor = ThreadPoolTaskExecutor().apply {
            setCorePoolSize(corePoolSize)
            setMaxPoolSize(maxPoolSize)
            setQueueCapacity(queueCapacity)
            setThreadNamePrefix("mvc-async-")
            initialize()
        }

        // MVC async 응답에 사용할 executor와 timeout을 공통 적용합니다.
        configurer.setTaskExecutor(executor)
        configurer.setDefaultTimeout(requestTimeoutMs)
    }
}