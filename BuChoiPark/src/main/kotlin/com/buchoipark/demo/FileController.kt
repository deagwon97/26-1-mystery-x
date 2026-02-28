package com.buchoipark.demo

import org.springframework.http.ContentDisposition
import org.springframework.http.HttpHeaders
import org.springframework.http.HttpStatus
import org.springframework.http.MediaType
import org.springframework.http.MediaTypeFactory
import org.springframework.http.ResponseEntity
import org.springframework.web.bind.annotation.DeleteMapping
import org.springframework.web.bind.annotation.GetMapping
import org.springframework.web.bind.annotation.PathVariable
import org.springframework.web.bind.annotation.PostMapping
import org.springframework.web.bind.annotation.RequestBody
import org.springframework.web.bind.annotation.RequestParam
import org.springframework.web.bind.annotation.RestController
import org.springframework.core.io.InputStreamResource
import org.springframework.web.multipart.MultipartFile
import java.nio.file.Files

@RestController
class FileController(
    private val fileService: FileService,
) {
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
    fun downloadFile(@PathVariable("id") id: String): ResponseEntity<InputStreamResource> {
        val download = fileService.getDownloadFile(id)
            ?: return ResponseEntity.status(HttpStatus.NOT_FOUND).build()

        val mediaType = MediaTypeFactory.getMediaType(download.fileName)
            .orElse(MediaType.APPLICATION_OCTET_STREAM)
        val resource = InputStreamResource(Files.newInputStream(download.path))

        val headers = HttpHeaders()
        headers.contentType = mediaType
        headers.contentLength = download.fileSize
        headers.contentDisposition = ContentDisposition.attachment()
            .filename(download.fileName)
            .build()

        return ResponseEntity.ok()
            .headers(headers)
            .body(resource)
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

        val response = fileService.uploadFile(userId, filePath, file)

        return ResponseEntity.ok(response)
    }
}

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
