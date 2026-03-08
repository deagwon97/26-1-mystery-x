package com.buchoipark.demo

import org.springframework.http.ContentDisposition
import org.springframework.http.HttpHeaders
import org.springframework.http.HttpStatus
import org.springframework.http.MediaType
import org.springframework.http.MediaTypeFactory
import org.springframework.http.ResponseEntity
import org.springframework.beans.factory.annotation.Value
import org.springframework.web.bind.annotation.DeleteMapping
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.PostMapping
import org.springframework.web.bind.annotation.RequestBody
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import org.springframework.web.servlet.mvc.method.annotation.StreamingResponseBody
import org.springframework.web.multipart.MultipartFile
import jakarta.servlet.http.HttpServletRequest
import java.nio.file.Files
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Semaphore
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

@RestController
class FileController(
    private val fileService: FileService,
    @Value("\${app.download.max-concurrency:80}")
    private val maxDownloadConcurrency: Int,
    @Value("\${app.download.acquire-timeout-ms:75}")
    private val downloadAcquireTimeoutMs: Long,
    @Value("\${app.download.rate-limit.enabled:true}")
    private val downloadRateLimitEnabled: Boolean,
    @Value("\${app.download.rate-limit.requests-per-second:30}")
    private val downloadRateLimitRps: Int,
    @Value("\${app.upload.max-concurrency:16}")
    private val uploadMaxConcurrency: Int,
    @Value("\${app.upload.acquire-timeout-ms:75}")
    private val uploadAcquireTimeoutMs: Long,
    @Value("\${app.upload.max-inflight-bytes:2147483648}")
    private val uploadMaxInflightBytes: Long,
) {
    // 느린 다운로드 클라이언트를 위한 벌크헤드입니다.
    // 다운로드 포화가 같은 서버의 경량 API 스레드를 잠식하지 않도록 보호합니다.
    private val downloadSemaphore by lazy { Semaphore(maxDownloadConcurrency, true) }
    private val downloadRateLimiter by lazy {
        FixedWindowRateLimiter(
            maxRequestsPerWindow = downloadRateLimitRps,
            windowMillis = 1000,
        )
    }
    private val uploadSemaphore by lazy { Semaphore(uploadMaxConcurrency, true) }
    private val uploadInFlightLimiter by lazy { InFlightByteLimiter(uploadMaxInflightBytes) }

    @GetMapping("/health")
    fun health(): ResponseEntity<Map<String, String>> {
        return ResponseEntity.ok(mapOf("status" to "UP"))
    }

    @PostMapping("/files/move-folder")
    fun moveFolder(@RequestBody request: MoveFolderRequest): ResponseEntity<Map<String, Any>> {
        val fromPath = request.fromPath.trimEnd('/')
        val toPath = request.toPath.trimEnd('/')

        if (fromPath.isBlank() || toPath.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }
        if (fromPath == toPath) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        val updated = fileService.moveFolder(fromPath, toPath)

        return ResponseEntity.ok(mapOf("updated" to updated))
    }

    @PostMapping("/files/{id}/move")
    fun moveFile(
        @PathVariable("id") id: String,
        @RequestBody request: MoveFileRequest,
    ): ResponseEntity<FileUploadResponse> {
        if (request.filePath.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        val updated = fileService.moveFile(id, request.filePath)
            ?: return ResponseEntity.status(HttpStatus.NOT_FOUND).build()

        return ResponseEntity.ok(updated)
    }

    @GetMapping("/files/{id}/download")
    fun downloadFile(
        @PathVariable("id") id: String,
        request: HttpServletRequest,
    ): ResponseEntity<StreamingResponseBody> {
        if (downloadRateLimitEnabled) {
            val key = resolveClientKey(request)
            if (!downloadRateLimiter.allow(key)) {
                return ResponseEntity.status(HttpStatus.TOO_MANY_REQUESTS)
                    .header("Retry-After", "1")
                    .build()
            }
        }

        val download = fileService.getDownloadFile(id)
            ?: return ResponseEntity.status(HttpStatus.NOT_FOUND).build()

        // 장시간 다운로드가 너무 많으면 즉시 백프레셔를 반환합니다.
        // 이는 내부 장애가 아니라 의도적인 보호 동작입니다.
        if (!downloadSemaphore.tryAcquire(downloadAcquireTimeoutMs, TimeUnit.MILLISECONDS)) {
            return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                .header("Retry-After", "1")
                .build()
        }

        val mediaType = MediaTypeFactory.getMediaType(download.fileName)
            .orElse(MediaType.APPLICATION_OCTET_STREAM)
        val body = StreamingResponseBody { outputStream ->
            try {
                // 파일 전체를 메모리에 올리지 않고 바로 스트리밍합니다.
                Files.newInputStream(download.path).use { inputStream ->
                    inputStream.copyTo(outputStream, DEFAULT_DOWNLOAD_BUFFER_SIZE)
                    outputStream.flush()
                }
            } finally {
                // 정상 종료/타임아웃/클라이언트 종료 여부와 무관하게 permit을 반드시 반납합니다.
                downloadSemaphore.release()
            }
        }

        val headers = HttpHeaders()
        headers.contentType = mediaType
        headers.contentLength = download.fileSize
        headers.contentDisposition = ContentDisposition.attachment()
            .filename(download.fileName)
            .build()

        return ResponseEntity.ok()
            .headers(headers)
            .body(body)
    }

    @GetMapping("/files")
    fun listFiles(@RequestParam("userId", required = false) userId: String?): List<FileUploadResponse> {
        return fileService.listFiles(userId)
    }

    @DeleteMapping("/files")
    fun deleteFile(
        @RequestParam("userId") userId: String,
        @RequestParam("filePath") filePath: String,
    ): ResponseEntity<Map<String, Boolean>> {
        if (userId.isBlank() || filePath.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        val deleted = fileService.deleteFile(userId, filePath)
        if (!deleted) {
            return ResponseEntity.status(HttpStatus.NOT_FOUND).build()
        }

        return ResponseEntity.ok(mapOf("deleted" to true))
    }

    @DeleteMapping("/files/folder")
    fun deleteFolder(
        @RequestParam("userId") userId: String,
        @RequestParam("folderPath") folderPath: String,
    ): ResponseEntity<Map<String, Int>> {
        if (userId.isBlank() || folderPath.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        val deleted = fileService.deleteFolder(userId, folderPath)
        if (deleted == 0) {
            return ResponseEntity.status(HttpStatus.NOT_FOUND).build()
        }

        return ResponseEntity.ok(mapOf("deleted" to deleted))
    }

    @GetMapping("/files/folder")
    fun listFolderEntries(
        @RequestParam("userId") userId: String,
        @RequestParam("folderPath") folderPath: String,
    ): ResponseEntity<List<FolderEntryResponse>> {
        if (userId.isBlank() || folderPath.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        return ResponseEntity.ok(fileService.listFolderEntries(userId, folderPath))
    }

    @PostMapping("/files/upload")
    fun uploadFile(
        @RequestParam("userId") userId: String,
        @RequestParam("filePath") filePath: String,
        @RequestParam("file") file: MultipartFile,
    ): ResponseEntity<FileUploadResponse> {
        if (userId.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }
        if (filePath.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }
        if (file.isEmpty) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        if (!uploadSemaphore.tryAcquire(uploadAcquireTimeoutMs, TimeUnit.MILLISECONDS)) {
            return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                .header("Retry-After", "1")
                .build()
        }

        val reservedBytes = if (file.size > 0) file.size else 0L
        if (!uploadInFlightLimiter.tryAcquire(reservedBytes)) {
            uploadSemaphore.release()
            return ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                .header("Retry-After", "1")
                .build()
        }

        try {
            val response = fileService.uploadFile(userId, filePath, file)

            return ResponseEntity.ok(response)
        } finally {
            uploadInFlightLimiter.release(reservedBytes)
            uploadSemaphore.release()
        }
    }
}

private fun resolveClientKey(request: HttpServletRequest): String {
    val xff = request.getHeader("X-Forwarded-For")
    if (!xff.isNullOrBlank()) {
        val first = xff.split(",")[0].trim()
        if (first.isNotBlank()) {
            return first
        }
    }
    return request.remoteAddr ?: "unknown"
}

private class FixedWindowRateLimiter(
    private val maxRequestsPerWindow: Int,
    private val windowMillis: Long,
) {
    private data class WindowCounter(
        @Volatile var windowStart: Long,
        val count: AtomicInteger,
    )

    private val counters = ConcurrentHashMap<String, WindowCounter>()

    fun allow(key: String): Boolean {
        val now = System.currentTimeMillis()
        val counter = counters.computeIfAbsent(key) {
            WindowCounter(now, AtomicInteger(0))
        }

        synchronized(counter) {
            if (now - counter.windowStart >= windowMillis) {
                counter.windowStart = now
                counter.count.set(0)
            }

            val next = counter.count.incrementAndGet()
            return next <= maxRequestsPerWindow
        }
    }
}

private class InFlightByteLimiter(
    private val maxBytes: Long,
) {
    private val inFlightBytes = AtomicLong(0)

    fun tryAcquire(bytes: Long): Boolean {
        if (bytes <= 0) {
            return true
        }

        while (true) {
            val current = inFlightBytes.get()
            val next = current + bytes
            if (next > maxBytes) {
                return false
            }
            if (inFlightBytes.compareAndSet(current, next)) {
                return true
            }
        }
    }

    fun release(bytes: Long) {
        if (bytes <= 0) {
            return
        }

        while (true) {
            val current = inFlightBytes.get()
            val next = (current - bytes).coerceAtLeast(0)
            if (inFlightBytes.compareAndSet(current, next)) {
                return
            }
        }
    }
}

// 64KB는 부하 테스트의 느린 클라이언트 청크 크기와 맞추었고,
// 대용량 순차 스트리밍에서 무난한 기본값입니다.
private const val DEFAULT_DOWNLOAD_BUFFER_SIZE = 64 * 1024

data class FileUploadResponse(
    val id: String,
    val userId: String,
    val uploadedAt: String,
    val fileName: String,
    val filePath: String,
    val fileSize: Long,
)

data class MoveFileRequest(
    val filePath: String,
)

data class MoveFolderRequest(
    val fromPath: String,
    val toPath: String,
)
