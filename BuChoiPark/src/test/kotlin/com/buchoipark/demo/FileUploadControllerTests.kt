package com.buchoipark.demo

import com.fasterxml.jackson.module.kotlin.jacksonObjectMapper
import org.assertj.core.api.Assertions.assertThat
import org.junit.jupiter.api.Test
import org.springframework.beans.factory.annotation.Autowired
import org.springframework.boot.test.context.SpringBootTest
import org.springframework.boot.test.web.server.LocalServerPort
import org.springframework.core.io.ByteArrayResource
import org.springframework.http.HttpEntity
import org.springframework.http.HttpHeaders
import org.springframework.http.HttpMethod
import org.springframework.http.MediaType
import org.springframework.http.RequestEntity
import org.springframework.jdbc.core.JdbcTemplate
import org.springframework.web.client.DefaultResponseErrorHandler
import org.springframework.web.client.RestTemplate
import org.springframework.util.LinkedMultiValueMap
import org.springframework.http.ResponseEntity
import org.springframework.http.client.ClientHttpResponse
import java.nio.file.Files
import java.nio.file.Path
import java.net.URI

@SpringBootTest(
    webEnvironment = SpringBootTest.WebEnvironment.RANDOM_PORT,
    properties = [
        "spring.datasource.url=jdbc:sqlite:file:livid-test?mode=memory&cache=shared",
        "app.storage.dir=/tmp/livid-test-uploads"
    ]
)
class FileUploadControllerTests(
    @Autowired private val jdbcTemplate: JdbcTemplate,
) {
    @LocalServerPort
    private var port: Int = 0

    @Test
    fun `uploads file and stores metadata`() {
        val userId = "user-123"
        val fileContent = "hello livid"
        val headers = HttpHeaders()
        headers.contentType = MediaType.MULTIPART_FORM_DATA

        val fileResource = object : ByteArrayResource(fileContent.toByteArray()) {
            override fun getFilename(): String = "greeting.txt"
        }

        val body = LinkedMultiValueMap<String, Any>()
        body.add("userId", userId)
            body.add("filePath", "/virtual/greeting.txt")
        body.add("file", fileResource)

            val restTemplate = RestTemplate().apply {
                errorHandler = object : DefaultResponseErrorHandler() {
                    override fun hasError(response: ClientHttpResponse): Boolean = false
                }
            }
        val response = restTemplate.postForEntity(
            "http://localhost:$port/files/upload",
            HttpEntity(body, headers),
            String::class.java
        )

        assertThat(response.statusCode.value()).isEqualTo(200)
        val responseJson = response.body ?: ""
        val tree = jacksonObjectMapper().readTree(responseJson)
        val id = tree.get("id")?.asText()
        assertThat(id).isNotNull
        assertThat(tree.get("userId")?.asText()).isEqualTo(userId)
        assertThat(tree.get("fileName")?.asText()).isEqualTo("greeting.txt")
        assertThat(tree.get("fileSize")?.asLong()).isEqualTo(fileContent.toByteArray().size.toLong())

        val count = jdbcTemplate.queryForObject(
            "SELECT COUNT(*) FROM files WHERE id = ? AND user_id = ?",
            Long::class.java,
            id,
            userId,
        )
        assertThat(count).isEqualTo(1L)

        val storedPath = jdbcTemplate.queryForObject(
            "SELECT file_path FROM files WHERE id = ?",
            String::class.java,
            id,
        )
        assertThat(storedPath).isNotNull
        assertThat(storedPath).isEqualTo("/virtual/greeting.txt")

        val storedPathOnDisk = Path.of("/tmp/livid-test-uploads", id)
        assertThat(Files.exists(storedPathOnDisk)).isTrue()
    }

    @Test
    fun `lists uploads with optional user filter`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        fun uploadFor(userId: String, fileName: String, content: String) {
            val headers = HttpHeaders()
            headers.contentType = MediaType.MULTIPART_FORM_DATA

            val fileResource = object : ByteArrayResource(content.toByteArray()) {
                override fun getFilename(): String = fileName
            }

            val body = LinkedMultiValueMap<String, Any>()
            body.add("userId", userId)
            body.add("filePath", "/virtual/$fileName")
            body.add("file", fileResource)

            val response = restTemplate.postForEntity(
                "http://localhost:$port/files/upload",
                HttpEntity(body, headers),
                String::class.java
            )
            assertThat(response.statusCode.value()).isEqualTo(200)
        }

        uploadFor("user-a", "a.txt", "aaa")
        uploadFor("user-b", "b.txt", "bbb")

        val allResponse = restTemplate.getForEntity(
            "http://localhost:$port/files",
            String::class.java
        )
        assertThat(allResponse.statusCode.value()).isEqualTo(200)
        val allTree = jacksonObjectMapper().readTree(allResponse.body ?: "[]")
        assertThat(allTree.isArray).isTrue
        assertThat(allTree.size()).isGreaterThanOrEqualTo(2)

        val filteredResponse = restTemplate.getForEntity(
            "http://localhost:$port/files?userId=user-a",
            String::class.java
        )
        assertThat(filteredResponse.statusCode.value()).isEqualTo(200)
        val filteredTree = jacksonObjectMapper().readTree(filteredResponse.body ?: "[]")
        assertThat(filteredTree.isArray).isTrue
        assertThat(filteredTree.size()).isEqualTo(1)
        assertThat(filteredTree[0].get("userId").asText()).isEqualTo("user-a")
    }

    @Test
    fun `lists files and folders inside a folder`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        fun uploadFor(userId: String, filePath: String, fileName: String, content: String) {
            val headers = HttpHeaders()
            headers.contentType = MediaType.MULTIPART_FORM_DATA

            val fileResource = object : ByteArrayResource(content.toByteArray()) {
                override fun getFilename(): String = fileName
            }

            val body = LinkedMultiValueMap<String, Any>()
            body.add("userId", userId)
            body.add("filePath", filePath)
            body.add("file", fileResource)

            val response = restTemplate.postForEntity(
                "http://localhost:$port/files/upload",
                HttpEntity(body, headers),
                String::class.java
            )
            assertThat(response.statusCode.value()).isEqualTo(200)
        }

        uploadFor("user-folder-list", "/virtual/docs/a.txt", "a.txt", "aaa")
        uploadFor("user-folder-list", "/virtual/docs/sub/b.txt", "b.txt", "bbb")
        uploadFor("user-folder-list", "/virtual/docs/sub2/c.txt", "c.txt", "ccc")
        uploadFor("user-folder-list", "/virtual/other/d.txt", "d.txt", "ddd")
        uploadFor("another-user", "/virtual/docs/e.txt", "e.txt", "eee")

        val response = restTemplate.getForEntity(
            "http://localhost:$port/files/folder?userId=user-folder-list&folderPath=/virtual/docs",
            String::class.java
        )

        assertThat(response.statusCode.value()).isEqualTo(200)
        val tree = jacksonObjectMapper().readTree(response.body ?: "[]")
        assertThat(tree.isArray).isTrue
        assertThat(tree.size()).isEqualTo(3)

        assertThat(tree[0].get("type").asText()).isEqualTo("FOLDER")
        assertThat(tree[0].get("name").asText()).isEqualTo("sub")

        assertThat(tree[1].get("type").asText()).isEqualTo("FOLDER")
        assertThat(tree[1].get("name").asText()).isEqualTo("sub2")

        assertThat(tree[2].get("type").asText()).isEqualTo("FILE")
        assertThat(tree[2].get("name").asText()).isEqualTo("a.txt")
        assertThat(tree[2].get("path").asText()).isEqualTo("/virtual/docs/a.txt")
    }

    @Test
    fun `deletes file by user id and file path`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        val headers = HttpHeaders()
        headers.contentType = MediaType.MULTIPART_FORM_DATA

        val content = "delete me"
        val fileResource = object : ByteArrayResource(content.toByteArray()) {
            override fun getFilename(): String = "delete.txt"
        }

        val body = LinkedMultiValueMap<String, Any>()
        body.add("userId", "user-delete")
        body.add("filePath", "/virtual/delete.txt")
        body.add("file", fileResource)

        val uploadResponse = restTemplate.postForEntity(
            "http://localhost:$port/files/upload",
            HttpEntity(body, headers),
            String::class.java
        )
        assertThat(uploadResponse.statusCode.value()).isEqualTo(200)

        val uploadTree = jacksonObjectMapper().readTree(uploadResponse.body ?: "{}")
        val id = uploadTree.get("id")?.asText()
        assertThat(id).isNotNull

        val storedPathOnDisk = Path.of("/tmp/livid-test-uploads", id)
        assertThat(Files.exists(storedPathOnDisk)).isTrue()

        val deleteUri = URI.create("http://localhost:$port/files?userId=user-delete&filePath=/virtual/delete.txt")
        val deleteRequest = RequestEntity
            .method(HttpMethod.DELETE, deleteUri)
            .build()

        val deleteResponse = restTemplate.exchange(deleteRequest, String::class.java)

        assertThat(deleteResponse.statusCode.value()).isEqualTo(200)

        val count = jdbcTemplate.queryForObject(
            "SELECT COUNT(*) FROM files WHERE id = ?",
            Long::class.java,
            id,
        )
        assertThat(count).isEqualTo(0L)
        assertThat(Files.exists(storedPathOnDisk)).isFalse()
    }

    @Test
    fun `deletes folder recursively by user id and folder path`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        fun uploadFor(userId: String, filePath: String, fileName: String, content: String): String {
            val headers = HttpHeaders()
            headers.contentType = MediaType.MULTIPART_FORM_DATA

            val fileResource = object : ByteArrayResource(content.toByteArray()) {
                override fun getFilename(): String = fileName
            }

            val body = LinkedMultiValueMap<String, Any>()
            body.add("userId", userId)
            body.add("filePath", filePath)
            body.add("file", fileResource)

            val response = restTemplate.postForEntity(
                "http://localhost:$port/files/upload",
                HttpEntity(body, headers),
                String::class.java
            )
            assertThat(response.statusCode.value()).isEqualTo(200)

            val tree = jacksonObjectMapper().readTree(response.body ?: "{}")
            return tree.get("id").asText()
        }

        val deletedId1 = uploadFor("user-folder-delete", "/virtual/docs/a.txt", "a.txt", "aaa")
        val deletedId2 = uploadFor("user-folder-delete", "/virtual/docs/sub/b.txt", "b.txt", "bbb")
        val deletedId3 = uploadFor("user-folder-delete", "/virtual/docs/sub2/c.txt", "c.txt", "ccc")
        val keptId = uploadFor("user-folder-delete", "/virtual/other/d.txt", "d.txt", "ddd")
        uploadFor("another-user", "/virtual/docs/e.txt", "e.txt", "eee")

        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", deletedId1))).isTrue()
        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", deletedId2))).isTrue()
        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", deletedId3))).isTrue()
        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", keptId))).isTrue()

        val deleteUri = URI.create("http://localhost:$port/files/folder?userId=user-folder-delete&folderPath=/virtual/docs")
        val deleteRequest = RequestEntity
            .method(HttpMethod.DELETE, deleteUri)
            .build()

        val deleteResponse = restTemplate.exchange(deleteRequest, String::class.java)
        assertThat(deleteResponse.statusCode.value()).isEqualTo(200)

        val deletedCount = jdbcTemplate.queryForObject(
            "SELECT COUNT(*) FROM files WHERE user_id = ? AND file_path LIKE ?",
            Long::class.java,
            "user-folder-delete",
            "/virtual/docs/%",
        )
        assertThat(deletedCount).isEqualTo(0L)

        val keptCount = jdbcTemplate.queryForObject(
            "SELECT COUNT(*) FROM files WHERE id = ?",
            Long::class.java,
            keptId,
        )
        assertThat(keptCount).isEqualTo(1L)

        val anotherUserCount = jdbcTemplate.queryForObject(
            "SELECT COUNT(*) FROM files WHERE user_id = ? AND file_path LIKE ?",
            Long::class.java,
            "another-user",
            "/virtual/docs/%",
        )
        assertThat(anotherUserCount).isEqualTo(1L)

        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", deletedId1))).isFalse()
        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", deletedId2))).isFalse()
        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", deletedId3))).isFalse()
        assertThat(Files.exists(Path.of("/tmp/livid-test-uploads", keptId))).isTrue()
    }

    @Test
    fun `downloads file by id`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        val headers = HttpHeaders()
        headers.contentType = MediaType.MULTIPART_FORM_DATA

        val content = "download me"
        val fileResource = object : ByteArrayResource(content.toByteArray()) {
            override fun getFilename(): String = "download.txt"
        }

        val body = LinkedMultiValueMap<String, Any>()
        body.add("userId", "user-download")
        body.add("filePath", "/virtual/download.txt")
        body.add("file", fileResource)

        val uploadResponse = restTemplate.postForEntity(
            "http://localhost:$port/files/upload",
            HttpEntity(body, headers),
            String::class.java
        )
        assertThat(uploadResponse.statusCode.value()).isEqualTo(200)

        val uploadTree = jacksonObjectMapper().readTree(uploadResponse.body ?: "{}")
        val id = uploadTree.get("id")?.asText()
        assertThat(id).isNotNull

        val downloadResponse: ResponseEntity<ByteArray> = restTemplate.getForEntity(
            "http://localhost:$port/files/$id/download",
            ByteArray::class.java
        )

        assertThat(downloadResponse.statusCode.value()).isEqualTo(200)
        assertThat(downloadResponse.body).isNotNull
        assertThat(String(downloadResponse.body!!)).isEqualTo(content)
        val disposition = downloadResponse.headers.getFirst(HttpHeaders.CONTENT_DISPOSITION)
        assertThat(disposition).contains("download.txt")
    }

    @Test
    fun `moves file metadata by id`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        val headers = HttpHeaders()
        headers.contentType = MediaType.MULTIPART_FORM_DATA

        val fileResource = object : ByteArrayResource("move me".toByteArray()) {
            override fun getFilename(): String = "move.txt"
        }

        val body = LinkedMultiValueMap<String, Any>()
        body.add("userId", "user-move")
        body.add("filePath", "/virtual/original/move.txt")
        body.add("file", fileResource)

        val uploadResponse = restTemplate.postForEntity(
            "http://localhost:$port/files/upload",
            HttpEntity(body, headers),
            String::class.java
        )
        assertThat(uploadResponse.statusCode.value()).isEqualTo(200)

        val uploadTree = jacksonObjectMapper().readTree(uploadResponse.body ?: "{}")
        val id = uploadTree.get("id")?.asText()
        assertThat(id).isNotNull

        val moveHeaders = HttpHeaders()
        moveHeaders.contentType = MediaType.APPLICATION_JSON

        val newPath = "/virtual/moved/"
        val moveBody = jacksonObjectMapper().writeValueAsString(mapOf("filePath" to newPath))

        val moveResponse = restTemplate.postForEntity(
            "http://localhost:$port/files/$id/move",
            HttpEntity(moveBody, moveHeaders),
            String::class.java
        )

        assertThat(moveResponse.statusCode.value()).isEqualTo(200)
        val moveTree = jacksonObjectMapper().readTree(moveResponse.body ?: "{}")
        assertThat(moveTree.get("filePath").asText()).isEqualTo("/virtual/moved/move.txt")

        val storedPath = jdbcTemplate.queryForObject(
            "SELECT file_path FROM files WHERE id = ?",
            String::class.java,
            id,
        )
        assertThat(storedPath).isEqualTo("/virtual/moved/move.txt")
    }

    @Test
    fun `moves folder metadata by prefix`() {
        val restTemplate = RestTemplate().apply {
            errorHandler = object : DefaultResponseErrorHandler() {
                override fun hasError(response: org.springframework.http.client.ClientHttpResponse): Boolean = false
            }
        }

        fun uploadWithPath(path: String, fileName: String, content: String): String {
            val headers = HttpHeaders()
            headers.contentType = MediaType.MULTIPART_FORM_DATA

            val fileResource = object : ByteArrayResource(content.toByteArray()) {
                override fun getFilename(): String = fileName
            }

            val body = LinkedMultiValueMap<String, Any>()
            body.add("userId", "user-folder")
            body.add("filePath", path)
            body.add("file", fileResource)

            val response = restTemplate.postForEntity(
                "http://localhost:$port/files/upload",
                HttpEntity(body, headers),
                String::class.java
            )
            assertThat(response.statusCode.value()).isEqualTo(200)

            val tree = jacksonObjectMapper().readTree(response.body ?: "{}")
            return tree.get("id").asText()
        }

        val id1 = uploadWithPath("/virtual/docs/a.txt", "a.txt", "aaa")
        val id2 = uploadWithPath("/virtual/docs/sub/b.txt", "b.txt", "bbb")
        uploadWithPath("/virtual/other/c.txt", "c.txt", "ccc")

        val moveHeaders = HttpHeaders()
        moveHeaders.contentType = MediaType.APPLICATION_JSON
        val moveBody = jacksonObjectMapper().writeValueAsString(
            mapOf("fromPath" to "/virtual/docs", "toPath" to "/virtual/docs2")
        )

        val moveResponse = restTemplate.postForEntity(
            "http://localhost:$port/files/move-folder",
            HttpEntity(moveBody, moveHeaders),
            String::class.java
        )
        assertThat(moveResponse.statusCode.value()).isEqualTo(200)

        val path1 = jdbcTemplate.queryForObject(
            "SELECT file_path FROM files WHERE id = ?",
            String::class.java,
            id1,
        )
        val path2 = jdbcTemplate.queryForObject(
            "SELECT file_path FROM files WHERE id = ?",
            String::class.java,
            id2,
        )

        assertThat(path1).isEqualTo("/virtual/docs2/a.txt")
        assertThat(path2).isEqualTo("/virtual/docs2/sub/b.txt")
    }
}
