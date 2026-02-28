package com.buchoipark.demo

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
    fun deleteFolder(userId: String, folderPath: String): Int {
        val normalizedFolderPath = normalizeFolderPath(folderPath)
        val pathPrefix = if (normalizedFolderPath == "/") "/" else "$normalizedFolderPath/"
        val targets = fileRepository.listFilesInPathPrefix(userId, pathPrefix)
        if (targets.isEmpty()) {
            return 0
        }

        val deleted = fileRepository.deleteFilesByUserAndPathPrefix(userId, pathPrefix)

        targets.forEach { file ->
            val physicalPath = Path.of(storageDir).resolve(file.id)
            Files.deleteIfExists(physicalPath)
        }

        return deleted
    }

    fun deleteFile(userId: String, filePath: String): Boolean {
        val normalizedPath = filePath.trim()
        val existing = fileRepository.findLatestFileByUserIdAndPath(userId, normalizedPath) ?: return false

        val deleted = fileRepository.deleteFileById(existing.id)
        if (deleted == 0) {
            return false
        }

        val physicalPath = Path.of(storageDir).resolve(existing.id)
        Files.deleteIfExists(physicalPath)

        return true
    }

    fun listFolderEntries(userId: String, folderPath: String): List<FolderEntryResponse> {
        val normalizedFolderPath = normalizeFolderPath(folderPath)
        val pathPrefix = if (normalizedFolderPath == "/") "/" else "$normalizedFolderPath/"
        val files = fileRepository.listFilesInPathPrefix(userId, pathPrefix)

        val folderEntries = linkedMapOf<String, FolderEntryResponse>()
        val fileEntries = mutableListOf<FolderEntryResponse>()

        for (file in files) {
            val remainder = file.filePath.removePrefix(pathPrefix)
            if (remainder.isBlank()) {
                continue
            }

            val slashIndex = remainder.indexOf('/')
            if (slashIndex >= 0) {
                val childFolderName = remainder.substring(0, slashIndex)
                if (childFolderName.isBlank()) {
                    continue
                }

                if (!folderEntries.containsKey(childFolderName)) {
                    val childFolderPath = if (normalizedFolderPath == "/") {
                        "/$childFolderName"
                    } else {
                        "$normalizedFolderPath/$childFolderName"
                    }
                    folderEntries[childFolderName] = FolderEntryResponse(
                        type = "FOLDER",
                        name = childFolderName,
                        path = childFolderPath,
                    )
                }
            } else {
                fileEntries.add(
                    FolderEntryResponse(
                        type = "FILE",
                        name = file.fileName,
                        path = file.filePath,
                        id = file.id,
                        fileSize = file.fileSize,
                        uploadedAt = file.uploadedAt,
                    )
                )
            }
        }

        return folderEntries.values.sortedBy { it.name } + fileEntries.sortedBy { it.name }
    }

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

    private fun normalizeFolderPath(path: String): String {
        val trimmed = path.trim()
        if (trimmed == "/") {
            return "/"
        }
        return trimmed.trimEnd('/').ifBlank { "/" }
    }
}

data class FileDownload(
    val path: Path,
    val fileName: String,
    val fileSize: Long,
)

data class FolderEntryResponse(
    val type: String,
    val name: String,
    val path: String,
    val id: String? = null,
    val fileSize: Long? = null,
    val uploadedAt: String? = null,
)
