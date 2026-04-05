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
import org.springframework.web.bind.annotation.RequestHeader
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import org.springframework.web.context.request.async.DeferredResult
import org.springframework.web.servlet.mvc.method.annotation.StreamingResponseBody
import org.springframework.web.multipart.MultipartFile
import jakarta.servlet.http.HttpServletRequest
import java.nio.file.Files
import java.util.ArrayDeque
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Semaphore
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong

@RestController
class FileController(
    private val fileService: FileService,
    @Value("\${app.download.max-concurrency:80}")
    private val maxDownloadConcurrency: Int,
    @Value("\${app.download.rate-limit.enabled:true}")
    private val downloadRateLimitEnabled: Boolean,
    @Value("\${app.download.rate-limit.requests-per-second:30}")
    private val downloadRateLimitRps: Int,
    @Value("\${app.download.max-queue-bytes:1073741824}")
    private val downloadMaxQueueBytes: Long,
    @Value("\${app.download.max-queue-requests:1000}")
    private val downloadMaxQueueRequests: Int,
    @Value("\${app.download.queue-timeout-ms:10000}")
    private val downloadQueueTimeoutMs: Long,
    @Value("\${app.upload.max-concurrency:16}")
    private val uploadMaxConcurrency: Int,
    @Value("\${app.upload.max-inflight-bytes:2147483648}")
    private val uploadMaxInflightBytes: Long,
    @Value("\${app.upload.max-queue-bytes:1073741824}")
    private val uploadMaxQueueBytes: Long,
    @Value("\${app.upload.max-queue-requests:1000}")
    private val uploadMaxQueueRequests: Int,
    @Value("\${app.upload.queue-timeout-ms:10000}")
    private val uploadQueueTimeoutMs: Long,
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
    private val uploadQueueLock = Any()
    private val uploadQueue = ArrayDeque<QueuedUploadRequest>()
    private val uploadQueuedCount = AtomicInteger(0)
    private val uploadQueueBytesLimiter by lazy { InFlightByteLimiter(uploadMaxQueueBytes) }
    private val uploadDrainInProgress = AtomicBoolean(false)
    private val downloadQueue = ConcurrentLinkedQueue<QueuedDownloadRequest>()
    private val downloadQueuedCount = AtomicInteger(0)
    private val downloadQueueBytesLimiter by lazy { InFlightByteLimiter(downloadMaxQueueBytes) }
    private val downloadDrainInProgress = AtomicBoolean(false)

    @GetMapping("/health")
    fun health(): ResponseEntity<Map<String, String>> {
        return ResponseEntity.ok(mapOf("status" to "UP"))
    }

    @PostMapping("/internal/files/upload-metadata")
    fun createUploadMetadata(@RequestBody request: UploadMetadataRequest): ResponseEntity<FileUploadResponse> {
        if (request.userId.isBlank() || request.filePath.isBlank() || request.fileName.isBlank()) {
            return ResponseEntity.status(HttpStatus.BAD_REQUEST).build()
        }

        val response = fileService.createUploadMetadata(
            userId = request.userId,
            filePath = request.filePath,
            fileName = request.fileName,
            fileSize = request.fileSize.coerceAtLeast(0),
        )
        return ResponseEntity.ok(response)
    }

    @GetMapping("/internal/files/{id}/download-metadata")
    fun getDownloadMetadata(
        @PathVariable("id") id: String,
    ): ResponseEntity<DownloadMetadataResponse> {
        val metadata = fileService.getDownloadMetadata(id)
            ?: return ResponseEntity.status(HttpStatus.NOT_FOUND).build()
        return ResponseEntity.ok(metadata)
    }

    @PostMapping("/internal/nginx-hooks/request")
    fun receiveRequestHook(@RequestBody request: NginxHookRequest): ResponseEntity<Void> {
        fileService.recordNginxHook(request)
        return ResponseEntity.noContent().build()
    }

    @PostMapping("/internal/nginx-hooks/response")
    fun receiveResponseHook(@RequestBody request: NginxHookRequest): ResponseEntity<Void> {
        fileService.recordNginxHook(request)
        return ResponseEntity.noContent().build()
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
    ): DeferredResult<ResponseEntity<StreamingResponseBody>> {
        val deferredResult = DeferredResult<ResponseEntity<StreamingResponseBody>>(downloadQueueTimeoutMs)

        if (downloadRateLimitEnabled) {
            val key = resolveClientKey(request)
            if (!downloadRateLimiter.allow(key)) {
                deferredResult.setResult(
                    ResponseEntity.status(HttpStatus.TOO_MANY_REQUESTS)
                        .header("Retry-After", "1")
                        .build()
                )
                return deferredResult
            }
        }

        val download = fileService.getDownloadFile(id)
            ?: run {
                deferredResult.setResult(ResponseEntity.status(HttpStatus.NOT_FOUND).build())
                return deferredResult
            }

        // 큐가 비어 있고 permit이 즉시 가능할 때만 바로 실행해 대기 지연을 줄입니다.
        if (downloadQueuedCount.get() == 0 && downloadSemaphore.tryAcquire()) {
            completeDownload(deferredResult, download)
            return deferredResult
        }

        val queueReservedBytes = download.fileSize.coerceAtLeast(0)
        if (!downloadQueueBytesLimiter.tryAcquire(queueReservedBytes)) {
            setDownloadUnavailable(deferredResult)
            return deferredResult
        }

        if (downloadQueuedCount.incrementAndGet() > downloadMaxQueueRequests) {
            downloadQueuedCount.decrementAndGet()
            downloadQueueBytesLimiter.release(queueReservedBytes)
            setDownloadUnavailable(deferredResult)
            return deferredResult
        }

        val queuedRequest = QueuedDownloadRequest(
            download = download,
            deferredResult = deferredResult,
            reservedBytes = queueReservedBytes,
        )
        downloadQueue.offer(queuedRequest)

        deferredResult.onTimeout {
            if (cancelQueuedRequest(queuedRequest)) {
                setDownloadUnavailable(deferredResult)
            }
        }
        deferredResult.onError {
            cancelQueuedRequest(queuedRequest)
        }

        drainDownloadQueue()
        return deferredResult
    }

    private fun cancelQueuedRequest(request: QueuedDownloadRequest): Boolean {
        if (!request.active.compareAndSet(true, false)) {
            return false
        }

        val removed = downloadQueue.remove(request)
        if (removed) {
            downloadQueuedCount.decrementAndGet()
            downloadQueueBytesLimiter.release(request.reservedBytes)
        }
        return removed
    }

    private fun drainDownloadQueue() {
        if (!downloadDrainInProgress.compareAndSet(false, true)) {
            return
        }

        try {
            while (true) {
                if (!downloadSemaphore.tryAcquire()) {
                    return
                }

                val next = pollNextQueuedDownload()
                if (next == null) {
                    downloadSemaphore.release()
                    return
                }

                completeDownload(next.deferredResult, next.download)
            }
        } finally {
            downloadDrainInProgress.set(false)
            // drain loop 종료 직전에 새 요청이 들어온 경우를 놓치지 않도록 한 번 더 확인합니다.
            if (downloadQueuedCount.get() > 0 && downloadSemaphore.availablePermits() > 0) {
                drainDownloadQueue()
            }
        }
    }

    private fun pollNextQueuedDownload(): QueuedDownloadRequest? {
        while (true) {
            val next = downloadQueue.poll() ?: return null
            if (!next.active.compareAndSet(true, false)) {
                continue
            }

            downloadQueuedCount.decrementAndGet()
            downloadQueueBytesLimiter.release(next.reservedBytes)
            return next
        }
    }

    private fun completeDownload(
        deferredResult: DeferredResult<ResponseEntity<StreamingResponseBody>>,
        download: FileDownload,
    ) {

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

        val response = ResponseEntity.ok()
            .headers(headers)
            .body(body)

        val accepted = deferredResult.setResult(response)
        if (!accepted) {
            // timeout/취소로 결과 전달에 실패했다면 permit을 즉시 반환합니다.
            downloadSemaphore.release()
            drainDownloadQueue()
        }
    }

    private fun setDownloadUnavailable(deferredResult: DeferredResult<ResponseEntity<StreamingResponseBody>>) {
        if (!deferredResult.hasResult()) {
            deferredResult.setResult(
                ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                    .header("Retry-After", "1")
                    .build()
            )
        }
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
        @RequestParam("userId") userId: String? = null,
        @RequestParam("filePath") filePath: String? = null,
        @RequestParam("file") file: MultipartFile? = null,
        @RequestHeader("X-User-Id") userIdHeader: String? = null,
        @RequestHeader("X-File-Path") filePathHeader: String? = null,
        @RequestHeader("X-File-Name") fileNameHeader: String? = null,
    ): DeferredResult<ResponseEntity<FileUploadResponse>> {
        // 헤더에서 파라미터 받기 (nginx 프록시 시)
        val effectiveUserId = userId ?: userIdHeader
        val effectiveFilePath = filePath ?: filePathHeader
        val effectiveFile = file
        val deferredResult = DeferredResult<ResponseEntity<FileUploadResponse>>(uploadQueueTimeoutMs)

        if (effectiveUserId.isNullOrBlank()) {
            deferredResult.setResult(ResponseEntity.status(HttpStatus.BAD_REQUEST).build())
            return deferredResult
        }
        if (effectiveFilePath.isNullOrBlank()) {
            deferredResult.setResult(ResponseEntity.status(HttpStatus.BAD_REQUEST).build())
            return deferredResult
        }
        if (effectiveFile == null || effectiveFile.isEmpty) {
            deferredResult.setResult(ResponseEntity.status(HttpStatus.BAD_REQUEST).build())
            return deferredResult
        }

        val inFlightBytes = effectiveFile.size.coerceAtLeast(0)

        // 대기열이 없을 때만 즉시 실행해 불필요한 큐잉 지연을 피합니다.
        if (uploadQueuedCount.get() == 0 && uploadSemaphore.tryAcquire() && uploadInFlightLimiter.tryAcquire(inFlightBytes)) {
            completeUpload(deferredResult, effectiveUserId, effectiveFilePath, effectiveFile, inFlightBytes)
            return deferredResult
        }

        val queueReservedBytes = inFlightBytes
        if (!uploadQueueBytesLimiter.tryAcquire(queueReservedBytes)) {
            setUploadUnavailable(deferredResult)
            return deferredResult
        }

        if (uploadQueuedCount.incrementAndGet() > uploadMaxQueueRequests) {
            uploadQueuedCount.decrementAndGet()
            uploadQueueBytesLimiter.release(queueReservedBytes)
            setUploadUnavailable(deferredResult)
            return deferredResult
        }

        val queuedRequest = QueuedUploadRequest(
            userId = effectiveUserId,
            filePath = effectiveFilePath,
            file = effectiveFile,
            deferredResult = deferredResult,
            reservedBytes = queueReservedBytes,
            inFlightBytes = inFlightBytes,
        )

        synchronized(uploadQueueLock) {
            uploadQueue.addLast(queuedRequest)
        }

        deferredResult.onTimeout {
            if (cancelQueuedUpload(queuedRequest)) {
                setUploadUnavailable(deferredResult)
            }
        }
        deferredResult.onError {
            cancelQueuedUpload(queuedRequest)
        }

        drainUploadQueue()
        return deferredResult
    }

    private fun cancelQueuedUpload(request: QueuedUploadRequest): Boolean {
        if (!request.active.compareAndSet(true, false)) {
            return false
        }

        val removed = synchronized(uploadQueueLock) {
            uploadQueue.remove(request)
        }
        if (removed) {
            uploadQueuedCount.decrementAndGet()
            uploadQueueBytesLimiter.release(request.reservedBytes)
        }
        return removed
    }

    private fun drainUploadQueue() {
        if (!uploadDrainInProgress.compareAndSet(false, true)) {
            return
        }

        try {
            while (true) {
                val next = pollNextQueuedUploadReady() ?: return
                completeUpload(
                    deferredResult = next.deferredResult,
                    userId = next.userId,
                    filePath = next.filePath,
                    file = next.file,
                    inFlightBytes = next.inFlightBytes,
                )
            }
        } finally {
            uploadDrainInProgress.set(false)
            if (uploadQueuedCount.get() > 0 && uploadSemaphore.availablePermits() > 0) {
                drainUploadQueue()
            }
        }
    }

    private fun pollNextQueuedUploadReady(): QueuedUploadRequest? {
        synchronized(uploadQueueLock) {
            while (uploadQueue.isNotEmpty()) {
                val next = uploadQueue.first()
                if (!next.active.get()) {
                    uploadQueue.removeFirst()
                    continue
                }

                if (!uploadSemaphore.tryAcquire()) {
                    return null
                }
                if (!uploadInFlightLimiter.tryAcquire(next.inFlightBytes)) {
                    uploadSemaphore.release()
                    return null
                }

                uploadQueue.removeFirst()
                if (!next.active.compareAndSet(true, false)) {
                    uploadSemaphore.release()
                    uploadInFlightLimiter.release(next.inFlightBytes)
                    continue
                }

                uploadQueuedCount.decrementAndGet()
                uploadQueueBytesLimiter.release(next.reservedBytes)
                return next
            }
        }
        return null
    }

    private fun completeUpload(
        deferredResult: DeferredResult<ResponseEntity<FileUploadResponse>>,
        userId: String,
        filePath: String,
        file: MultipartFile,
        inFlightBytes: Long,
    ) {
        try {
            val response = fileService.uploadFile(userId, filePath, file)

            deferredResult.setResult(ResponseEntity.ok(response))
        } catch (_: Exception) {
            if (!deferredResult.hasResult()) {
                deferredResult.setResult(ResponseEntity.status(HttpStatus.INTERNAL_SERVER_ERROR).build())
            }
        } finally {
            uploadInFlightLimiter.release(inFlightBytes)
            uploadSemaphore.release()
            drainUploadQueue()
        }
    }

    private fun setUploadUnavailable(deferredResult: DeferredResult<ResponseEntity<FileUploadResponse>>) {
        if (!deferredResult.hasResult()) {
            deferredResult.setResult(
                ResponseEntity.status(HttpStatus.SERVICE_UNAVAILABLE)
                    .header("Retry-After", "1")
                    .build()
            )
        }
    }
}

private data class QueuedDownloadRequest(
    val download: FileDownload,
    val deferredResult: DeferredResult<ResponseEntity<StreamingResponseBody>>,
    val reservedBytes: Long,
    val active: AtomicBoolean = AtomicBoolean(true),
)

private data class QueuedUploadRequest(
    val userId: String,
    val filePath: String,
    val file: MultipartFile,
    val deferredResult: DeferredResult<ResponseEntity<FileUploadResponse>>,
    val reservedBytes: Long,
    val inFlightBytes: Long,
    val active: AtomicBoolean = AtomicBoolean(true),
)

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

data class UploadMetadataRequest(
    val userId: String,
    val filePath: String,
    val fileName: String,
    val fileSize: Long,
)

data class DownloadMetadataResponse(
    val id: String,
    val fileName: String,
    val fileSize: Long,
    val storagePath: String,
)

data class NginxHookRequest(
    val phase: String? = null,
    val requestId: String? = null,
    val method: String? = null,
    val path: String? = null,
    val status: Int? = null,
    val elapsedMs: Long? = null,
    val remoteAddr: String? = null,
    val timestamp: Long? = null,
)
