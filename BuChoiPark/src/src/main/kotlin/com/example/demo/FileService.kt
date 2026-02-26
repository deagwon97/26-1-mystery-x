package com.example.demo

import com.github.f4b6a3.uuid.UuidCreator
import org.springframework.beans.factory.annotation.Value
import org.springframework.stereotype.Service
import org.springframework.web.multipart.MultipartFile
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardCopyOption
import java.time.Instant

@Service
class FileService(
    private val fileRepository: FileRepository,
    @Value("\${app.storage.dir}")
    private val storageDir: String,
) {
    fun moveFolder(fromPath: String, toPath: String): Int {
        return fileRepository.updateFilePathsForMoveFolder(fromPath, toPath)
    }

    fun moveFile(id: String, filePath: String): FileUploadResponse? {
        val existing = fileRepository.findFileById(id) ?: return null
        val normalizedPath = normalizeFilePath(filePath, existing.fileName)

        fileRepository.updateFilePath(id, normalizedPath)

        return existing.copy(filePath = normalizedPath)
    }

    fun getDownloadFile(id: String): FileDownload? {
        val info = fileRepository.findDownloadInfo(id) ?: return null
        val path = Path.of(storageDir).resolve(id)
        if (!Files.exists(path)) {
            return null
        }
        return FileDownload(
            path = path,
            fileName = info.fileName,
            fileSize = info.fileSize,
        )
    }

    fun listFiles(userId: String?): List<FileUploadResponse> {
        return fileRepository.listFiles(userId)
    }

    fun uploadFile(userId: String, filePath: String, file: MultipartFile): FileUploadResponse {
        val fileId = UuidCreator.getTimeOrderedEpoch()
        val uploadedAt = Instant.now()

        val targetDir = Path.of(storageDir)
        Files.createDirectories(targetDir)

        val targetPath = targetDir.resolve(fileId.toString())
        file.inputStream.use { input ->
            Files.copy(input, targetPath, StandardCopyOption.REPLACE_EXISTING)
        }

        val originalName = file.originalFilename ?: "unknown"
        val normalizedPath = normalizeFilePath(filePath, originalName)

        val response = FileUploadResponse(
            id = fileId.toString(),
            userId = userId,
            uploadedAt = uploadedAt.toString(),
            fileName = originalName,
            filePath = normalizedPath,
            fileSize = file.size,
        )

        fileRepository.insertFile(response)

        return response
    }

    private fun normalizeFilePath(path: String, fileName: String): String {
        val trimmed = path.trim()
        if (trimmed.isBlank()) {
            return trimmed
        }
        return if (trimmed.endsWith("/")) {
            trimmed + fileName
        } else {
            trimmed
        }
    }
}

data class FileDownload(
    val path: Path,
    val fileName: String,
    val fileSize: Long,
)
