package com.buchoipark.demo

import org.springframework.boot.jdbc.autoconfigure.DataSourceProperties
import org.springframework.context.annotation.Bean
import org.springframework.context.annotation.Configuration
import java.nio.file.Files
import java.nio.file.Path
import javax.sql.DataSource

@Configuration
class SqliteConfig(
    private val dataSourceProperties: DataSourceProperties
) {
    @Bean
    fun dataSource(): DataSource {
        val url = dataSourceProperties.url ?: throw IllegalStateException("DataSource URL must be provided")
        val dbPath = url.removePrefix("jdbc:sqlite:")
        if (!dbPath.startsWith(":") && !dbPath.startsWith("file:")) {
            Files.createDirectories(Path.of(dbPath).parent)
        }
        return dataSourceProperties.initializeDataSourceBuilder().build()
    }
}
