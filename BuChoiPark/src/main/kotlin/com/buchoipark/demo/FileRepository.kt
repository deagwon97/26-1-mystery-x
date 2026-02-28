package com.buchoipark.demo

import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.stereotype.Repository

@Repository
class FileRepository(
    private val jdbcTemplate: JdbcTemplate,
) {
    fun findLatestFileByUserIdAndPath(userId: String, filePath: String): FileUploadResponse? {
        return jdbcTemplate.query(
            """
                SELECT id, user_id, uploaded_at, file_name, file_path, file_size
                FROM files
                WHERE user_id = ? AND file_path = ?
                ORDER BY uploaded_at DESC
                LIMIT 1
            """.trimIndent(),
            rowMapper,
            userId,
            filePath,
        ).firstOrNull()
    }

    fun deleteFileById(id: String): Int {
        return jdbcTemplate.update(
            "DELETE FROM files WHERE id = ?",
            id,
        )
    }

    fun listFilesInPathPrefix(userId: String, pathPrefix: String): List<FileUploadResponse> {
        return jdbcTemplate.query(
            """
                SELECT id, user_id, uploaded_at, file_name, file_path, file_size
                FROM files
                WHERE user_id = ? AND file_path LIKE ?
                ORDER BY file_path ASC
            """.trimIndent(),
            rowMapper,
            userId,
            "$pathPrefix%",
        )
    }

    fun updateFilePathsForMoveFolder(fromPath: String, toPath: String): Int {
        return jdbcTemplate.update(
            """
                UPDATE files
                SET file_path = ? || substr(file_path, ?)
                WHERE file_path = ? OR file_path LIKE ?
            """.trimIndent(),
            toPath,
            fromPath.length + 1,
            fromPath,
            "$fromPath/%",
        )
    }

    fun findFileById(id: String): FileUploadResponse? {
        return jdbcTemplate.query(
            """
                SELECT id, user_id, uploaded_at, file_name, file_path, file_size
                FROM files
                WHERE id = ?
            """.trimIndent(),
            rowMapper,
            id,
        ).firstOrNull()
    }

    fun updateFilePath(id: String, filePath: String) {
        jdbcTemplate.update(
            "UPDATE files SET file_path = ? WHERE id = ?",
            filePath,
            id,
        )
    }

    fun findDownloadInfo(id: String): FileDownloadInfo? {
        return jdbcTemplate.query(
            """
                SELECT file_name, file_size
                FROM files
                WHERE id = ?
            """.trimIndent(),
            { rs, _ ->
                FileDownloadInfo(
                    fileName = rs.getString("file_name"),
                    fileSize = rs.getLong("file_size"),
                )
            },
            id,
        ).firstOrNull()
    }

    fun listFiles(userId: String?): List<FileUploadResponse> {
        val baseQuery = """
            SELECT id, user_id, uploaded_at, file_name, file_path, file_size
            FROM files
        """.trimIndent()

        return if (userId.isNullOrBlank()) {
            jdbcTemplate.query(
                "$baseQuery ORDER BY uploaded_at DESC",
                rowMapper,
            )
        } else {
            jdbcTemplate.query(
                "$baseQuery WHERE user_id = ? ORDER BY uploaded_at DESC",
                rowMapper,
                userId,
            )
        }
    }

    fun insertFile(response: FileUploadResponse) {
        jdbcTemplate.update(
            """
                INSERT INTO files (id, user_id, uploaded_at, file_name, file_path, file_size)
                VALUES (?, ?, ?, ?, ?, ?)
            """.trimIndent(),
            response.id,
            response.userId,
            response.uploadedAt,
            response.fileName,
            response.filePath,
            response.fileSize,
        )
    }

    private val rowMapper = { rs: java.sql.ResultSet, _: Int ->
        FileUploadResponse(
            id = rs.getString("id"),
            userId = rs.getString("user_id"),
            uploadedAt = rs.getString("uploaded_at"),
            fileName = rs.getString("file_name"),
            filePath = rs.getString("file_path"),
            fileSize = rs.getLong("file_size"),
        )
    }
}

data class FileDownloadInfo(
    val fileName: String,
    val fileSize: Long,
)
